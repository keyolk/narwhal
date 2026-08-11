// monitor.go implements `narwhal monitor`, a live view of a running Narwhal
// run: the task DAG as it progresses and the radio traffic as it happens.
//
// Narwhal's own experiments showed that the interesting behavior — workers
// cross-correcting each other mid-flight, a split-request arriving, a task
// failing and retrying — all happens while the run is in flight. Reading a
// snapshot after the fact loses the sequencing that makes it legible. The
// monitor exists so an operator can watch it unfold.
//
// It polls the broker's read-only /monitor endpoint. Polling (rather than
// streaming) keeps the monitor decoupled: it can attach to a run already in
// progress, detach freely, and survive a broker restart.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/store"
)

// ANSI codes. Kept minimal and disabled when stdout is not a terminal.
const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
	ansiCyan   = "\033[36m"
	ansiClear  = "\033[H\033[2J"
)

type monitorView struct {
	color    bool
	lastSeq  int64
	seenMsgs map[int64]bool
}

// daemonRunLister asks the daemon which runs it is hosting. Returns an
// error when no daemon is running, which Discover treats as "no daemon
// runs" rather than a failure — monitoring a batch run must still work
// when the daemon was never started.
func daemonRunLister() (string, []string, error) {
	info, err := nd.Status()
	if err != nil {
		return "", nil, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(info.URL + "/api/v1/control/status")
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("daemon status %d", resp.StatusCode)
	}
	var payload struct {
		Runs []struct {
			RunID string `json:"run_id"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(payload.Runs))
	for _, r := range payload.Runs {
		ids = append(ids, r.RunID)
	}
	return info.URL, ids, nil
}

func monitorCmd(args []string) {
	fs := flag.NewFlagSet("monitor", flag.ExitOnError)
	runID := fs.String("run", "", "run id to monitor (default: newest live run)")
	interval := fs.Duration("interval", 1*time.Second, "poll interval")
	follow := fs.Bool("follow", true, "keep polling until the run finishes")
	noColor := fs.Bool("no-color", false, "disable ANSI colors")
	fs.Parse(args)

	// Discover both batch runs (registry file) and daemon-hosted runs
	// (spawned through MCP). Only the batch CLI writes to the registry, so
	// looking there alone would miss every interactive run.
	entries := store.Discover(daemonRunLister)
	live, ok := store.FindLiveIn(entries, *runID)
	if !ok {
		if *runID == "" {
			fmt.Fprintf(os.Stderr, "no live runs.\n%s", store.SummarizeRuns(entries))
		} else {
			fmt.Fprintf(os.Stderr, "run %q is not live.\n%s", *runID, store.SummarizeRuns(entries))
			fmt.Fprintf(os.Stderr, "\nFor a finished run, use: narwhal show %s\n", *runID)
		}
		os.Exit(1)
	}

	v := &monitorView{
		color:    !*noColor,
		seenMsgs: make(map[int64]bool),
	}

	origin := fmt.Sprintf("pid %d", live.PID)
	if live.PID == 0 {
		origin = "daemon"
	}
	fmt.Fprintf(os.Stderr, "%s monitoring %s (%s) at %s%s\n",
		v.dim("[narwhal]"), live.RunID, origin, live.BrokerURL, v.reset())

	client := &http.Client{Timeout: 5 * time.Second}
	for {
		snap, agents, err := fetchMonitor(client, live.BrokerURL, live.RunID)
		if err != nil {
			// The broker is gone: the run finished or the process died.
			fmt.Fprintf(os.Stderr, "\n%s broker unreachable (%v) — run likely finished%s\n",
				v.dim("[narwhal]"), err, v.reset())
			fmt.Fprintf(os.Stderr, "%s narwhal show %s%s\n", v.dim("try:"), live.RunID, v.reset())
			return
		}

		v.render(snap, agents, live)

		if !*follow {
			return
		}
		if snap.State == broker.RunDone || snap.State == broker.RunFailed {
			fmt.Printf("\n%s run finished: %s%s\n", v.bold(""), v.stateColor(snap.State), v.reset())
			return
		}
		time.Sleep(*interval)
	}
}

func fetchMonitor(client *http.Client, brokerURL, runID string) (broker.Snapshot, []string, error) {
	resp, err := client.Get(brokerURL + "/api/v1/monitor/" + runID)
	if err != nil {
		return broker.Snapshot{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return broker.Snapshot{}, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var payload struct {
		Snapshot broker.Snapshot `json:"snapshot"`
		Agents   []string        `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return broker.Snapshot{}, nil, err
	}
	return payload.Snapshot, payload.Agents, nil
}

// render redraws the DAG panel and appends any radio messages not yet shown.
// The DAG is redrawn in place; messages scroll, because their order is the
// thing worth preserving.
func (v *monitorView) render(s broker.Snapshot, agents []string, live store.LiveRun) {
	var b strings.Builder

	fmt.Fprint(&b, ansiClear)
	fmt.Fprintf(&b, "%sNarwhal%s  %s  %s\n", v.bold(""), v.reset(), s.RunID, v.stateColor(s.State))
	if s.Prompt != "" {
		prompt := s.Prompt
		if len(prompt) > 100 {
			prompt = prompt[:97] + "..."
		}
		fmt.Fprintf(&b, "%s%s%s\n", v.dim(""), prompt, v.reset())
	}
	fmt.Fprintln(&b)

	// Task DAG, sorted so the display is stable across polls.
	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	counts := map[broker.TaskState]int{}
	for _, t := range tasks {
		counts[t.State]++
	}
	fmt.Fprintf(&b, "%sTasks%s  %d total   %s%d done%s  %s%d running%s  %s%d ready%s  %s%d pending%s  %s%d failed%s\n",
		v.bold(""), v.reset(), len(tasks),
		v.green(), counts[broker.TaskCompleted], v.reset(),
		v.cyan(), counts[broker.TaskDispatched], v.reset(),
		v.yellow(), counts[broker.TaskReady], v.reset(),
		v.dimCode(), counts[broker.TaskPending], v.reset(),
		v.red(), counts[broker.TaskFailed], v.reset())
	fmt.Fprintln(&b)

	for _, t := range tasks {
		icon, color := v.taskIcon(t.State)
		deps := ""
		if len(t.Deps) > 0 {
			deps = v.dim(fmt.Sprintf("  ← %s", strings.Join(t.Deps, ", ")))
		}
		retries := ""
		if t.Dispatches > 1 {
			retries = v.yellow() + fmt.Sprintf("  (attempt %d)", t.Dispatches) + v.reset()
		}
		name := t.Name
		if name == "" {
			name = t.ID
		}
		fmt.Fprintf(&b, "  %s%s%s %-14s %s%s%s%s\n",
			color, icon, v.reset(), t.ID, name, deps, retries, v.reset())
	}

	if len(agents) > 0 {
		sort.Strings(agents)
		fmt.Fprintf(&b, "\n%sAgents%s  %s\n", v.bold(""), v.reset(), strings.Join(agents, ", "))
	}

	// Radio traffic. Only messages we have not printed before, so the
	// conversation reads as a transcript rather than being redrawn.
	msgs := s.Messages
	var fresh []*broker.Message
	for _, m := range msgs {
		if !v.seenMsgs[m.Seq] {
			fresh = append(fresh, m)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Seq < fresh[j].Seq })

	fmt.Fprintf(&b, "\n%sRadio%s  %d messages\n", v.bold(""), v.reset(), len(msgs))

	// Show the tail of the log so the panel stays readable, plus every
	// fresh message.
	tail := msgs
	const maxShown = 12
	if len(tail) > maxShown {
		tail = tail[len(tail)-maxShown:]
	}
	sort.Slice(tail, func(i, j int) bool { return tail[i].Seq < tail[j].Seq })
	for _, m := range tail {
		v.writeMessage(&b, m)
	}
	for _, m := range fresh {
		v.seenMsgs[m.Seq] = true
	}

	fmt.Print(b.String())
}

func (v *monitorView) writeMessage(b *strings.Builder, m *broker.Message) {
	prio := ""
	switch m.Priority {
	case broker.PriorityUrgent:
		prio = v.red() + "URGENT" + v.reset()
	case broker.PriorityFYI:
		prio = v.dim("fyi")
	default:
		prio = v.dim("·")
	}
	to := ""
	if len(m.Mentions) > 0 {
		to = v.dim(" → " + strings.Join(m.Mentions, ","))
	}
	content := strings.ReplaceAll(m.Content, "\n", " ")
	if len(content) > 88 {
		content = content[:85] + "..."
	}
	// Split-requests are structural, not conversational: call them out.
	if _, _, _, _, ok := broker.ParseSplitRequest(m.Content); ok {
		fmt.Fprintf(b, "  %s%3d%s %s%-16s%s %sSPLIT%s %s\n",
			v.dimCode(), m.Seq, v.reset(),
			v.blue(), m.Sender, v.reset(),
			v.yellow(), v.reset(), content)
		return
	}
	fmt.Fprintf(b, "  %s%3d%s %s%-16s%s %s%s %s\n",
		v.dimCode(), m.Seq, v.reset(),
		v.blue(), m.Sender, v.reset(),
		prio, to, content)
}

func (v *monitorView) taskIcon(s broker.TaskState) (string, string) {
	switch s {
	case broker.TaskCompleted:
		return "✓", v.green()
	case broker.TaskDispatched:
		return "▶", v.cyan()
	case broker.TaskReady:
		return "○", v.yellow()
	case broker.TaskFailed:
		return "✗", v.red()
	case broker.TaskBlocked:
		return "⊘", v.red()
	default:
		return "·", v.dimCode()
	}
}

func (v *monitorView) stateColor(s broker.RunState) string {
	switch s {
	case broker.RunDone:
		return v.green() + string(s) + v.reset()
	case broker.RunFailed:
		return v.red() + string(s) + v.reset()
	default:
		return v.cyan() + string(s) + v.reset()
	}
}

// Color helpers degrade to empty strings when color is disabled.
func (v *monitorView) code(c string) string {
	if !v.color {
		return ""
	}
	return c
}
func (v *monitorView) reset() string   { return v.code(ansiReset) }
func (v *monitorView) dimCode() string { return v.code(ansiDim) }
func (v *monitorView) red() string     { return v.code(ansiRed) }
func (v *monitorView) green() string   { return v.code(ansiGreen) }
func (v *monitorView) yellow() string  { return v.code(ansiYellow) }
func (v *monitorView) blue() string    { return v.code(ansiBlue) }
func (v *monitorView) cyan() string    { return v.code(ansiCyan) }
func (v *monitorView) bold(s string) string {
	return v.code(ansiBold) + s
}
func (v *monitorView) dim(s string) string {
	if !v.color {
		return s
	}
	return ansiDim + s + ansiReset
}
