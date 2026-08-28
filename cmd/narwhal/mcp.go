// mcp.go implements `narwhal mcp`, the stdio MCP server Claude Code
// launches as a subprocess.
//
// It auto-starts the daemon on first use. Requiring the user to run
// `narwhal daemon start` before their first spawn would make the MCP
// server fail in a way the model cannot fix, so instead the first tool
// call that needs a daemon brings one up.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/mcp"
)

func mcpCmd(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	noAutoStart := fs.Bool("no-auto-start", false, "fail instead of starting the daemon on demand")
	fs.Parse(args)

	resolve := func() (string, error) {
		if info, err := nd.Status(); err == nil {
			// A running daemon is used whatever build it is — killing
			// one out from under live workers to pick up a new binary
			// would be worse than serving a run on the old code. But say
			// so, because the alternative is what happened on
			// s1787888345056-2: a four-worker run served by a daemon
			// four days older than the installed binary, recording no
			// accounting because the code that measures it was not the
			// code that was running. The stamp on the finished run is
			// the only other evidence, and it arrives after the run.
			if stale, running := nd.Stale(version); stale {
				fmt.Fprintf(os.Stderr,
					"[narwhal-mcp] warning: daemon is build %s, this binary is %s.\n"+
						"[narwhal-mcp] runs will be served by the old code until "+
						"`make daemon-restart`.\n",
					orUnknownBuild(running), version)
			}
			return info.URL, nil
		}
		if *noAutoStart {
			return "", fmt.Errorf("daemon not running")
		}
		return autoStartDaemon()
	}

	srv := mcp.New(os.Stdin, os.Stdout, resolve)
	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "[narwhal-mcp] %v\n", err)
		os.Exit(1)
	}
}

// orUnknownBuild names a daemon that recorded no version — one predating
// the field, which is itself old.
func orUnknownBuild(v string) string {
	if v == "" {
		return "(unstamped)"
	}
	return v
}

// autoStartDaemon launches a detached daemon and waits for it to publish
// its URL. The child is deliberately not tied to this process: Claude Code
// restarts MCP servers freely, and the daemon must outlive those restarts
// so in-flight runs are not orphaned.
func autoStartDaemon() (string, error) {
	self, err := os.Executable()
	if err != nil {
		self = "narwhal"
	}
	cmd := exec.Command(self, "daemon", "start")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach into its own process group so terminal signals aimed at the
	// MCP server do not take the daemon down with it.
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start daemon: %w", err)
	}
	// Do not wait for the child; just release it.
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := nd.Status(); err == nil {
			return info.URL, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return "", fmt.Errorf("daemon did not become ready within 10s")
}
