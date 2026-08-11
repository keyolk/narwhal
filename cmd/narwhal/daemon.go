// daemon.go implements `narwhal daemon`, the long-lived broker that backs
// interactive use. The MCP server (narwhal mcp) talks to it over HTTP, so
// the user's Claude Code session can spawn workers across many turns without
// the broker dying between them.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/server"
)

func daemonCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: narwhal daemon <start|stop|status>")
		os.Exit(1)
	}
	switch args[0] {
	case "start":
		daemonStart(args[1:])
	case "stop":
		if err := nd.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("daemon stopped")
	case "status":
		out, _ := nd.StatusJSON()
		fmt.Println(string(out))
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func daemonStart(args []string) {
	fs := flag.NewFlagSet("daemon start", flag.ExitOnError)
	foreground := fs.Bool("foreground", false, "run in the foreground instead of detaching")
	fs.Parse(args)

	lock, err := nd.AcquireLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}()

	sess := nd.NewSession()
	srv := server.New(sess.Broker, sess.Registry)
	// The daemon owns worker lifecycle, so it backs the /control routes the
	// MCP server calls. The batch CLI leaves this unset.
	srv.SetController(sess)
	addr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start broker: %v\n", err)
		os.Exit(1)
	}
	sess.URL = addr

	// Watch for tasks whose deps have completed. Without this loop a task
	// created with unmet deps becomes ready and then never launches,
	// because nothing on the daemon path dispatches it.
	dispatcher := nd.NewDispatcher(sess)
	dispatcher.Start()
	defer dispatcher.Stop()

	if err := nd.WriteState(lock, os.Getpid(), addr); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write daemon state: %v\n", err)
	}
	defer nd.ClearState()

	fmt.Fprintf(os.Stderr, "[narwhal] daemon listening on %s (pid %d)\n", addr, os.Getpid())
	if !*foreground {
		fmt.Printf("%s\n", addr)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "[narwhal] received %s, shutting down\n", sig)
	srv.Shutdown()
}
