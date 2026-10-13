// flow.go persists flows: a piece of work done in several runs.
//
// A request big enough to need several rounds — plan it, build it, verify
// it — was several unrelated runs. Each started from its prompt alone, so
// what the plan concluded reached the build only if someone pasted it in,
// and nothing on disk said the runs belonged together or what the work as a
// whole had produced so far.
//
// A flow is that work as a unit with its own input and output:
//
//	input   the goal it was started with, each step's request, and notes —
//	        decisions and constraints the person keeps up to date, which
//	        every later step is planned with
//	output  each step's run, whose results and artifacts are read from the
//	        run snapshots rather than copied here, so there is one record
//	        of what a run produced
//
// Stored as ~/.narwhal/flows/<id>.json, beside the runs. Both the daemon
// (appending steps) and the monitor (editing notes) write it, so every
// change is a locked read-modify-write.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Flow is a piece of work done in several runs.
type Flow struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CWD       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
	// Goal is the request the flow was started with.
	Goal string `json:"goal"`
	// Notes are what the person wants every later step to know: what was
	// decided, what is out of scope, what to be careful of.
	Notes string `json:"notes,omitempty"`
	// Steps are the runs, in order.
	Steps []FlowStep `json:"steps"`
	// Closed marks the work finished; a closed flow takes no more steps.
	Closed bool `json:"closed,omitempty"`
	// Removed are runs taken out of the flow. A run records its flow too,
	// so dropping it from Steps alone would let it claim its place back;
	// the run's own record is kept, and the views leave these out.
	Removed []string `json:"removed,omitempty"`
}

// FlowStep is one run of a flow and what it was asked.
type FlowStep struct {
	RunID     string    `json:"run_id"`
	Request   string    `json:"request"`
	StartedAt time.Time `json:"started_at"`
}

// FlowsDir is where flows are stored.
func FlowsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".narwhal", "flows")
}

func flowPath(id string) string { return filepath.Join(FlowsDir(), id+".json") }

// LoadFlow reads one flow.
func LoadFlow(id string) (Flow, error) {
	data, err := os.ReadFile(flowPath(id))
	if err != nil {
		return Flow{}, fmt.Errorf("read flow %s: %w", id, err)
	}
	var f Flow
	if err := json.Unmarshal(data, &f); err != nil {
		return Flow{}, fmt.Errorf("parse flow %s: %w", id, err)
	}
	return f, nil
}

// ListFlows reads every flow, newest first.
func ListFlows() []Flow {
	entries, err := os.ReadDir(FlowsDir())
	if err != nil {
		return nil
	}
	var out []Flow
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if f, err := LoadFlow(id); err == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// UpdateFlow applies change to a flow under a lock and writes it back; with
// create set, a flow that does not exist starts from the zero value.
func UpdateFlow(id string, create bool, change func(*Flow) error) (Flow, error) {
	dir := FlowsDir()
	if err := checkTestIsolation(dir); err != nil {
		return Flow{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Flow{}, fmt.Errorf("create flows dir: %w", err)
	}
	lock, err := os.OpenFile(flowPath(id)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Flow{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Flow{}, fmt.Errorf("lock flow %s: %w", id, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	f, err := LoadFlow(id)
	if err != nil {
		if !create || !os.IsNotExist(unwrapPathErr(err)) {
			return Flow{}, err
		}
		f = Flow{ID: id, CreatedAt: time.Now()}
	}
	if err := change(&f); err != nil {
		return Flow{}, err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return Flow{}, err
	}
	tmp := flowPath(id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return Flow{}, err
	}
	return f, os.Rename(tmp, flowPath(id))
}

func unwrapPathErr(err error) error {
	for e := err; e != nil; {
		if pe, ok := e.(*os.PathError); ok {
			return pe
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return err
}

// StartFlow records a new flow whose first step is runID.
func StartFlow(runID, cwd, goal string) (Flow, error) {
	return UpdateFlow(runID, true, func(f *Flow) error {
		f.CWD, f.Goal, f.Title = cwd, goal, flowTitle(goal)
		f.Steps = []FlowStep{{RunID: runID, Request: goal, StartedAt: time.Now()}}
		return nil
	})
}

// AddFlowStep appends a step to a flow.
func AddFlowStep(flowID, runID, request string) (Flow, error) {
	return UpdateFlow(flowID, false, func(f *Flow) error {
		if f.Closed {
			return fmt.Errorf("flow %s is closed", flowID)
		}
		f.Steps = append(f.Steps, FlowStep{RunID: runID, Request: request, StartedAt: time.Now()})
		return nil
	})
}

// SetFlowNotes replaces a flow's notes.
func SetFlowNotes(flowID, notes string) (Flow, error) {
	return UpdateFlow(flowID, false, func(f *Flow) error {
		f.Notes = strings.TrimSpace(notes)
		return nil
	})
}

// SetFlowClosed opens or closes a flow.
func SetFlowClosed(flowID string, closed bool) (Flow, error) {
	return UpdateFlow(flowID, false, func(f *Flow) error {
		f.Closed = closed
		return nil
	})
}

// RemoveFlowStep takes a step out of a flow: it is no longer listed, read
// into later steps' plans or counted in the flow's output. The run's own
// record stays on disk. ref is a 1-based step number or a run id. Removing
// the last step removes the flow.
func RemoveFlowStep(flowID, ref string) (Flow, FlowStep, error) {
	var removed FlowStep
	f, err := UpdateFlow(flowID, false, func(f *Flow) error {
		i := f.stepIndex(ref)
		if i < 0 {
			return fmt.Errorf("flow %s has no step %q", flowID, ref)
		}
		removed = f.Steps[i]
		f.Steps = append(f.Steps[:i:i], f.Steps[i+1:]...)
		f.Removed = append(f.Removed, removed.RunID)
		return nil
	})
	if err != nil {
		return f, removed, err
	}
	if len(f.Steps) == 0 {
		if err := os.Remove(flowPath(flowID)); err != nil && !os.IsNotExist(err) {
			return f, removed, fmt.Errorf("remove empty flow %s: %w", flowID, err)
		}
	}
	return f, removed, nil
}

// stepIndex resolves a 1-based step number or a run id.
func (f Flow) stepIndex(ref string) int {
	ref = strings.TrimSpace(ref)
	for i, s := range f.Steps {
		if s.RunID == ref || fmt.Sprint(i+1) == ref {
			return i
		}
	}
	return -1
}

// IsRemoved reports whether runID was taken out of the flow.
func (f Flow) IsRemoved(runID string) bool {
	for _, r := range f.Removed {
		if r == runID {
			return true
		}
	}
	return false
}

// flowTitle is a goal's first line, short enough for a list.
func flowTitle(goal string) string {
	t := firstProse(goal)
	if r := []rune(t); len(r) > 80 {
		t = string(r[:79]) + "…"
	}
	return t
}

// LastRun is the flow's most recent step's run id.
func (f Flow) LastRun() string {
	if len(f.Steps) == 0 {
		return ""
	}
	return f.Steps[len(f.Steps)-1].RunID
}

// StepOf is the 0-based position of runID in the flow, or -1.
func (f Flow) StepOf(runID string) int {
	for i, s := range f.Steps {
		if s.RunID == runID {
			return i
		}
	}
	return -1
}

// FlowOutput is what a flow has produced: each step's run snapshot, read
// fresh, and their artifacts together.
type FlowOutput struct {
	Runs      []broker.Snapshot
	Artifacts broker.Artifacts
}

// Output reads the flow's runs from disk.
func (f Flow) Output() FlowOutput {
	var out FlowOutput
	for _, s := range f.Steps {
		snap, err := LoadRun(s.RunID)
		if err != nil {
			snap = broker.Snapshot{RunID: s.RunID, Prompt: s.Request}
		}
		out.Runs = append(out.Runs, snap)
		out.Artifacts.Merge(snap.Result().Artifacts)
	}
	return out
}

// FlowBrief is what the next step of a flow is planned with: the goal, the
// notes, and what each earlier step was asked and concluded. Answers are
// clipped — a planner needs the conclusions, and the full text is on disk
// for a worker that needs more, at the path the brief names.
func FlowBrief(f Flow, steps []broker.Snapshot, fullPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## This request continues earlier work\n\n")
	fmt.Fprintf(&b, "It is step %d of a piece of work done in several runs, in %s.\n\n", len(steps)+1, f.CWD)
	fmt.Fprintf(&b, "### The goal of the whole work\n\n%s\n\n", strings.TrimSpace(f.Goal))
	if strings.TrimSpace(f.Notes) != "" {
		fmt.Fprintf(&b, "### Notes from the operator — decisions and constraints that hold for every step\n\n%s\n\n", f.Notes)
	}
	fmt.Fprintf(&b, "### What the earlier steps were asked and concluded\n\n")
	all := broker.Artifacts{}
	for i, s := range steps {
		req := s.Prompt
		if i < len(f.Steps) {
			req = f.Steps[i].Request
		}
		res := s.Result()
		all.Merge(res.Artifacts)
		fmt.Fprintf(&b, "#### Step %d (run %s, %s)\n\nAsked: %s\n\n", i+1, s.RunID, s.State, clip(strings.TrimSpace(req), 600))
		if res.Answer != "" {
			fmt.Fprintf(&b, "Concluded:\n\n%s\n\n", clip(res.Answer, 4000))
		} else {
			b.WriteString("Concluded: nothing recorded.\n\n")
		}
	}
	if !all.Empty() {
		b.WriteString("### What the work has produced so far\n\n")
		writeArtifacts(&b, all)
	}
	if fullPath != "" {
		fmt.Fprintf(&b, "The earlier steps' full results are in %s. Tell workers to read it when their task builds on them.\n\n", fullPath)
	}
	b.WriteString("Plan this step to build on those results. Do not redo what an earlier step already settled unless this request says to.\n")
	return b.String()
}

// FlowMarkdown renders a flow as one document: the goal, the notes, and
// each step's request, answer and artifacts.
func FlowMarkdown(f Flow, steps []broker.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", f.Title)
	fmt.Fprintf(&b, "Flow: `%s`  CWD: `%s`  Steps: %d\n\n", f.ID, f.CWD, len(f.Steps))
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n", strings.TrimSpace(f.Goal))
	if f.Notes != "" {
		fmt.Fprintf(&b, "## Notes\n\n%s\n\n", f.Notes)
	}
	all := broker.Artifacts{}
	for i, s := range steps {
		res := s.Result()
		all.Merge(res.Artifacts)
		req := s.Prompt
		if i < len(f.Steps) {
			req = f.Steps[i].Request
		}
		fmt.Fprintf(&b, "## Step %d — %s\n\nRun `%s` · %s\n\n**Asked:** %s\n\n", i+1, headline(req), s.RunID, s.State, strings.TrimSpace(req))
		if res.Answer != "" {
			fmt.Fprintf(&b, "%s\n\n", res.Answer)
		}
	}
	if !all.Empty() {
		b.WriteString("## Artifacts\n\n")
		writeArtifacts(&b, all)
	}
	return b.String()
}

func writeArtifacts(b *strings.Builder, a broker.Artifacts) {
	for _, p := range []struct {
		name string
		vals []string
	}{{"PRs", a.PRs}, {"Branches", a.Branches}, {"Commits", a.Commits}, {"Files", a.Files}} {
		if len(p.vals) == 0 {
			continue
		}
		fmt.Fprintf(b, "- %s: %s\n", p.name, strings.Join(p.vals, ", "))
	}
	b.WriteString("\n")
}
