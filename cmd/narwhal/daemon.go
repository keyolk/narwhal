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
		daemonStop(args[1:])
	case "status":
		out, code := daemonStatus()
		fmt.Println(out)
		if code != 0 {
			os.Exit(code)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// daemonStatus renders the daemon's status and the exit code to leave with.
//
// The code is the point. Status always exited 0, so `if narwhal daemon
// status` was always true: make daemon-restart read a dead daemon as a live
// one, tried to stop it, and aborted the whole restart on that failure —
// leaving no daemon at all after an install.
func daemonStatus() (string, int) {
	out, _ := nd.StatusJSONFor(version)
	if _, err := nd.Status(); err != nil {
		return string(out), 1
	}
	return string(out), 0
}

// daemonStop asks the running daemon to shut down.
//
// It refuses while workers are in flight, because stopping the broker does
// not stop them: they are detached processes that keep working and then
// report to a closed port. --force is for a wedged daemon, where the
// alternative is worse.
func daemonStop(args []string) {
	fs := flag.NewFlagSet("daemon stop", flag.ExitOnError)
	force := fs.Bool("force", false, "stop even while workers are running")
	fs.Parse(args)

	if err := nd.Stop(*force); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("daemon stopped")
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

	// Reload runs that were in flight when this daemon last stopped. This
	// must happen before the dispatcher starts: its reap treats a
	// dispatched task with no tracked worker as a failed dispatch, so a
	// run adopted after the first tick would have its still-running work
	// launched a second time.
	adopted, stillRunning := nd.AdoptRuns(sess)
	for _, r := range adopted {
		fmt.Fprintf(os.Stderr, "[narwhal] adopted %s\n", r.Summary())
	}

	// Watch for tasks whose deps have completed. Without this loop a task
	// created with unmet deps becomes ready and then never launches,
	// because nothing on the daemon path dispatches it.
	dispatcher := nd.NewDispatcher(sess)
	dispatcher.AdoptRunning(stillRunning)
	dispatcher.Start()
	defer dispatcher.Stop()

	if err := nd.WriteState(lock, os.Getpid(), addr); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write daemon state: %v\n", err)
	}
	// Which build is serving. A daemon outlives installs by design, so
	// without this the only record of the gap is a finished run's
	// harness_version — read after the run it affected, which is too
	// late to act on.
	if err := nd.WriteVersion(version); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write daemon version: %v\n", err)
	}
	defer nd.ClearState()

	fmt.Fprintf(os.Stderr, "[narwhal] daemon listening on %s (pid %d, build %s)\n",
		addr, os.Getpid(), version)
	if !*foreground {
		fmt.Printf("%s\n", addr)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "[narwhal] received %s, shutting down\n", sig)

	// Write every run down before letting go of the process. The dispatch
	// loop already persists on change, but a run that was mid-flight when
	// the signal arrived should not lose its last state to the shutdown —
	// that is exactly the moment the record matters most.
	if n := dispatcher.PersistAll(); n > 0 {
		fmt.Fprintf(os.Stderr, "[narwhal] saved %d run(s)\n", n)
	}
	srv.Shutdown()
}
