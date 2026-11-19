// Command narwhal is the CLI entrypoint for the Narwhal multi-agent runtime.
//
// Usage:
//
//	narwhal run --workers auto --prompt "analyze this repo's auth flow"
//	narwhal run --workers 3 --prompt "..." --cwd ~/src/myrepo
//	narwhal show <run-id>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/config"
	"github.com/keyolk/narwhal/internal/coordinator"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
	"github.com/keyolk/narwhal/internal/store"
	"github.com/keyolk/narwhal/internal/usage"
)

// version is stamped at build time by the Makefile:
//
//	-ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// A plain `go build` leaves it as "dev", which is the honest answer for a
// binary built without that stamp.
var version = "dev"

func main() {
	// Every snapshot taken by this process records which build produced
	// it. Done once here rather than at each place a run is created: one
	// setter is what keeps the two plan paths from drifting again (#39).
	//
	// "dev" is deliberately not recorded. An unstamped build cannot say
	// which code it is, and a field that claims "dev" reads as a known
	// answer where absent correctly reads as unknown.
	if version != "dev" {
		broker.SetHarnessVersion(version)
	}

	// With no command, the home screen: every request and a line to start
	// one. Usage was what this printed, which is the one thing a person
	// starting the program does not need first.
	// Settings come from ~/.narwhal/config.json for every command, the
	// daemon included; see internal/config.
	config.UseFile(config.DefaultPath())

	if len(os.Args) < 2 {
		homeCmd()
		return
	}
	if metaCmd(os.Stdout, os.Args[1]) {
		return
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "plan":
		planCmd(os.Args[2:])
	case "show":
		showCmd(os.Args[2:])
	case "export":
		exportCmd(os.Args[2:])
	case "backfill":
		backfillCmd(os.Args[2:])
	case "monitor":
		monitorCmd(os.Args[2:])
	case "daemon":
		daemonCmd(os.Args[2:])
	case "mcp":
		mcpCmd(os.Args[2:])
	case "experiment":
		experimentCmd(os.Args[2:])
	case "config":
		configCmd(os.Args[2:])
	case "flow":
		flowCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(1)
	}
}

// metaCmd answers the arguments that ask about narwhal rather than run it,
// and reports whether arg was one of them.
//
// The flag spellings are here because they are what an agent types by
// reflex: `narwhal --version` answered "unknown command" and then the whole
// usage, which reads as a broken install rather than a wrong spelling.
// Asked-for help goes to stdout so `narwhal --help | head` works; the usage
// printed after a mistake stays on stderr.
func metaCmd(w io.Writer, arg string) bool {
	switch arg {
	case "version", "--version", "-v", "-V":
		fmt.Fprintln(w, "narwhal "+version)
	case "help", "-h", "--help", "-help":
		printUsage(w)
	default:
		return false
	}
	return true
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	prompt := fs.String("prompt", "", "task prompt for the run")
	workers := fs.String("workers", "auto", "number of workers (auto or a number)")
	cwd := fs.String("cwd", "", "working directory (defaults to current dir)")
	timeout := fs.Duration("timeout", 30*time.Minute, "max time to wait for workers")
	fs.Parse(args)

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: --prompt is required")
		os.Exit(1)
	}

	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}

	runID := generateRunID()

	b := usage.NewBroker()
	reg := broker.NewAgentRegistry()

	// Create the run with "main" as the coordinator.
	mainAgent := reg.Register("main", runID, true)
	r := b.CreateRun(runID, *prompt, *cwd, mainAgent.ID)

	// Start the broker HTTP server.
	srv := server.New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start broker: %v\n", err)
		os.Exit(1)
	}
	defer srv.Shutdown()

	// Advertise this run so `narwhal monitor` in another terminal can find
	// its broker while the run is still in flight.
	_ = store.RegisterLive(store.LiveRun{
		RunID:     runID,
		PID:       os.Getpid(),
		BrokerURL: addr,
		CWD:       *cwd,
		Prompt:    *prompt,
	})
	defer store.DeregisterLive(os.Getpid())

	fmt.Fprintf(os.Stderr, "[narwhal] run %s started\n", runID)
	fmt.Fprintf(os.Stderr, "[narwhal] broker: %s\n", addr)
	fmt.Fprintf(os.Stderr, "[narwhal] cwd: %s\n", *cwd)
	fmt.Fprintf(os.Stderr, "[narwhal] monitor: narwhal monitor --run %s\n", runID)

	workerCount := 1
	if *workers != "auto" {
		fmt.Sscanf(*workers, "%d", &workerCount)
	}

	l := launcher.New(addr, runID, *cwd)

	// Radio threads: planning carries decisions, worklog carries in-flight
	// findings. Keeping them separate mirrors AgentRadio's protocol so
	// decision traffic does not drown out live discovery sharing.
	r.CreateStandardThreads()

	// Phase 2: the caller declares a flat set of independent tasks. A
	// coordinating agent will later build a real DAG and add tasks mid-run
	// via split-request; the coordinator loop already handles both.
	for i := 1; i <= workerCount; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		r.AddTask(taskID, fmt.Sprintf("worker-%d", i), *prompt, nil)
	}

	cfg := coordinator.DefaultConfig()
	cfg.Timeout = *timeout
	if workerCount < cfg.MaxConcurrency {
		cfg.MaxConcurrency = workerCount
	}

	coord := coordinator.New(r, reg, l, cfg)
	res := coord.Run()

	// Persist the final snapshot so `narwhal show` can read it after exit.
	if err := store.SaveRun(r.Snapshot()); err != nil {
		fmt.Fprintf(os.Stderr, "[narwhal] warning: save run: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "[narwhal] completed=%d failed=%d unreached=%d timed_out=%v\n",
		len(res.Completed), len(res.Failed), len(res.Unreached), res.TimedOut)

	out := map[string]any{
		"result":      res,
		"snapshot":    r.Snapshot(),
		"session_dir": l.SessionDir(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// exportCmd renders the run history as markdown.
//
// The search index over this machine's notes takes directories of markdown
// files, so that is how a past run becomes findable — a file in the shape
// the indexer already reads, rather than another process to talk to. Add
// the output directory as a collection and past runs join the same corpus
// as the wiki and the operational notes.
func exportCmd(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dir := fs.String("dir", defaultExportDir(), "directory to write markdown into")
	all := fs.Bool("all", false, "include runs with nothing substantial to index")
	fs.Parse(args)

	n, err := store.ExportRuns(*dir, *all)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: export: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("exported %d runs to %s\n", n, *dir)
}

// defaultExportDir keeps the markdown beside the JSON it is rendered from.
func defaultExportDir() string { return store.DefaultExportDir() }

func showCmd(args []string) {
	if code := runShow(os.Stdout, os.Stderr, args); code != 0 {
		os.Exit(code)
	}
}

// runSummary is one line of `narwhal show`'s list, and one element of
// `narwhal show --json`'s: the same fields feed both, so the two cannot
// disagree about what a run did.
type runSummary struct {
	RunID     string          `json:"run_id"`
	Prompt    string          `json:"prompt,omitempty"`
	State     broker.RunState `json:"state,omitempty"`
	StartedAt int64           `json:"started_at,omitempty"`
	Tasks     int             `json:"tasks"`
	Done      int             `json:"done"`
	Failed    int             `json:"failed"`
	Messages  int             `json:"messages"`
	Usage     string          `json:"usage,omitempty"`
	Check     string          `json:"check,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func summarizeRun(id string) runSummary {
	sum := runSummary{RunID: id}
	s, err := store.LoadRun(id)
	if err != nil {
		sum.Error = err.Error()
		return sum
	}
	sum.Prompt, sum.State, sum.StartedAt = listPrompt(s.Prompt), s.State, s.StartedAt
	sum.Tasks, sum.Messages = len(s.Tasks), len(s.Messages)
	for _, t := range s.Tasks {
		switch t.State {
		case broker.TaskCompleted:
			sum.Done++
		case broker.TaskFailed:
			sum.Failed++
		}
	}
	// What a run cost belongs next to whether it worked. The README's
	// economics claim — frontier planner, cheap workers — is about this
	// number, and until it was printed the claim could only be checked
	// against the price list rather than against what the harness actually
	// spent.
	sum.Usage = usageLine(s)
	// Whether a run's answers were tested belongs beside what they cost: a
	// cheap wrong answer is the expensive kind.
	sum.Check = checkLine(s)
	return sum
}

// listPrompt is the opening of a prompt, for telling runs apart in a list.
// A benchmark prompt runs to pages; the whole of it is one
// `narwhal show RUN --json` away.
func listPrompt(p string) string {
	p = firstLine(p)
	if r := []rune(p); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return p
}

// runShow is `narwhal show [run-id] [--json]`, returning the exit code.
//
// --json exists for agents: the text form puts the usage and check reports
// in front of the snapshot, so its output was never one parseable document.
// The flag is accepted on either side of the run id, which flag.Parse alone
// would not do.
func runShow(stdout, stderr io.Writer, args []string) int {
	asJSON := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		default:
			rest = append(rest, a)
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")

	if len(rest) < 1 {
		// No run-id: list recent runs.
		ids, err := store.ListRuns()
		if err != nil {
			fmt.Fprintf(stderr, "error: list runs: %v\n", err)
			return 1
		}
		runs := make([]runSummary, 0, len(ids))
		for _, id := range ids {
			runs = append(runs, summarizeRun(id))
		}
		if asJSON {
			if err := enc.Encode(runs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			return 0
		}
		if len(runs) == 0 {
			fmt.Fprintln(stdout, "(no persisted runs)")
			return 0
		}
		fmt.Fprintf(stdout, "Recent runs (newest first):\n")
		for _, r := range runs {
			if r.Error != "" {
				fmt.Fprintf(stdout, "  %s  (unreadable: %s)\n", r.RunID, r.Error)
				continue
			}
			fmt.Fprintf(stdout, "  %s  state=%s tasks=%d (done=%d fail=%d) msgs=%d\n",
				r.RunID, r.State, r.Tasks, r.Done, r.Failed, r.Messages)
			if r.Usage != "" {
				fmt.Fprintf(stdout, "      %s\n", r.Usage)
			}
			if r.Check != "" {
				fmt.Fprintf(stdout, "      %s\n", r.Check)
			}
		}
		return 0
	}

	s, err := store.LoadRun(rest[0])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	// The accounting first, because it is the question a finished run is
	// usually opened to answer and the JSON below is 200 lines deep.
	if !asJSON {
		if r := UsageReport(s); r != "" {
			fmt.Fprint(stdout, r)
			fmt.Fprintln(stdout)
		}
		if r := CheckReport(s); r != "" {
			fmt.Fprint(stdout, r)
			fmt.Fprintln(stdout)
		}
	}
	if err := enc.Encode(s); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func generateRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano()/1e6)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `narwhal — graph-engineered multi-agent runtime with passive awareness

Usage:
  narwhal       Home: every request, newest and in-progress first, and a line
                to start a new one. Open a request to see its graph and steer it.

  narwhal run   --prompt "..." [--workers N] [--cwd DIR] [--timeout DUR]
                Flat parallel workers on one prompt.

  narwhal plan  --prompt "..." [--cwd DIR] [--concurrency N] [--timeout DUR]
                A planner agent decomposes the request into a task DAG,
                then the coordinator dispatches workers in dependency order.

  narwhal monitor [--run ID] [--interval DUR] [--no-color] [--list]
                Live view of a running run: DAG progress and radio traffic.
                Defaults to the newest live run. Needs a terminal; --list
                prints the live runs and exits.

  narwhal show  [run-id] [--json]
                List finished runs, or print one run's full snapshot.
                --json prints the list, or the snapshot alone, as JSON.

  narwhal backfill [--dry-run]
                Fill in token accounting for runs that finished before it
                was recorded, by reading each worker's Claude transcript.

  narwhal export [--dir DIR]
                Write every persisted run to DIR as markdown, one file per
                run, for a search index to pick up. Re-run to refresh.

  narwhal daemon <start|stop|status>
                Long-lived broker for interactive use. Started on demand by
                the MCP server; you rarely need to run this yourself.

  narwhal mcp   [--no-auto-start]
                MCP server over stdio. Register it with Claude Code:
                  claude mcp add --scope user narwhal narwhal mcp

  narwhal experiment [--cwd DIR]
                Two-worker passive-awareness validation scenario.

  narwhal flow [list | show FLOW | rm FLOW STEP]
                Flows and their steps. rm takes a step out of its flow (by
                number or run id); the run's record is kept.

  narwhal config [list [PREFIX] | get KEY | set KEY VALUE | unset KEY | path]
                Settings in ~/.narwhal/config.json: which Jev judgements are
                consulted and their thresholds, scene notes. A change applies
                to the running daemon without a restart.

  narwhal version | --version | -v

Common options:
  --prompt       task prompt (required for run/plan)
  --cwd          working directory (default: current directory)
  --timeout      max wait time for the run (default: 30m)
  --workers      worker count for run (default: 1)
  --concurrency  max parallel workers for plan (default: 3)`)
}
