// outputs.go is every output of a run — or of a whole flow — in one place,
// each readable where it is listed.
//
// A run's real product was rarely where its result pointed. The plan run
// on disk wrote a 22KB PLAN.md and its answer was a sentence naming it;
// three of its four investigators answered "see the results thread" and
// put the report on the radio. Reading any of it meant knowing which of
// the task's outcome, its artifacts, its radio posts or a file on disk to
// open, and the route to each was different.
//
// The Outputs view lists them together and shows the selected one beside
// the list. What "shows" means depends on what it is, so each kind is read
// its own way (outputs_view.go): a markdown file rendered, code with line
// numbers, a PR as gh describes it, a commit as git shows it, a branch as
// the commits it adds. The keys follow the kind too — o opens a PR in the
// browser but a commit's diff in $PAGER, e edits a file, enter on an
// answer goes to the task that gave it.
//
// o opens it from a run, its result, a task's Summary, or a flow, where it
// lists every step's outputs; O from anywhere in a task.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
)

// outputKind orders and labels outputs.
type outputKind int

const (
	outFile    outputKind = iota // a file a task recorded
	outPR                        // a pull request
	outBranch                    // a branch pushed
	outCommit                    // a commit made
	outReport                    // a post to the results thread
	outAnswer                    // a task's outcome
	outRequest                   // what a flow step was asked, listed in the flow view
)

var outputLabels = map[outputKind]string{
	outFile: "file", outPR: "PR", outBranch: "branch", outCommit: "commit", outReport: "report", outAnswer: "answer",
	outRequest: "asked",
}

// output is one readable thing a run produced.
type output struct {
	kind  outputKind
	run   string // the run it came from
	step  int    // its run's step in the flow, 0-based; 0 for a lone run
	cwd   string // its run's directory, where git commands run
	task  string // the task that produced it
	title string
	path  string // a file's path
	ref   string // a PR's URL, a branch's or a commit's name
	text  string // a report's or an answer's text
	from  string // a report's sender and time, an answer's task state
}

// key identifies an output for the details fetched for it.
func (o output) key() string { return o.cwd + "|" + outputLabels[o.kind] + "|" + o.ref }

// fetched reports whether an output's content comes from a command.
func (o output) fetched() bool { return o.kind == outPR || o.kind == outBranch || o.kind == outCommit }

// runOutputs collects a run's outputs, in the order they are listed: what
// a person acts on first — files, PRs, branches, commits — then the
// reports, then the answers, the synthesis's first.
func runOutputs(s broker.Snapshot, step int) []output {
	byKind := map[outputKind][]output{}
	add := func(o output) {
		o.run, o.step, o.cwd = s.RunID, step, s.CWD
		byKind[o.kind] = append(byKind[o.kind], o)
	}
	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	seen := map[string]bool{}
	once := func(k string) bool {
		if seen[k] {
			return false
		}
		seen[k] = true
		return true
	}
	for _, t := range tasks {
		var a broker.Artifacts
		if t.Artifacts != nil {
			a.Merge(*t.Artifacts)
		}
		if t.State == broker.TaskCompleted {
			a.Merge(broker.ExtractArtifacts(t.Outcome))
		}
		for _, f := range a.Files {
			p := f
			if !filepath.IsAbs(p) && s.CWD != "" {
				p = filepath.Join(s.CWD, p)
			}
			if once("f" + p) {
				add(output{kind: outFile, task: t.ID, title: filepath.Base(p), path: p, ref: p})
			}
		}
		for _, u := range a.PRs {
			if once("p" + u) {
				add(output{kind: outPR, task: t.ID, title: prShort(u), ref: u})
			}
		}
		for _, b := range a.Branches {
			if once("b" + b) {
				add(output{kind: outBranch, task: t.ID, title: b, ref: b})
			}
		}
		for _, c := range a.Commits {
			if once("c" + c) {
				add(output{kind: outCommit, task: t.ID, title: c, ref: c})
			}
		}
	}
	for _, m := range s.Messages {
		if m == nil || m.ThreadID != broker.ResultsThread || broker.IsProtocol(m.Content) {
			continue
		}
		add(output{kind: outReport, task: strings.TrimPrefix(m.Sender, "worker-"), title: titleOf(m.Content),
			text: m.Content, from: m.Sender + " · " + clock(m.CreatedAt) + " · results thread"})
	}
	var answers []output
	for _, t := range tasks {
		if strings.TrimSpace(t.Outcome) == "" {
			continue
		}
		o := output{kind: outAnswer, task: t.ID, title: titleOf(t.Outcome), text: t.Outcome, from: string(t.State)}
		if !t.EndedAt.IsZero() {
			o.from += " · " + clock(t.EndedAt)
		}
		if isSynthesisTask(t) {
			answers = append([]output{o}, answers...)
		} else {
			answers = append(answers, o)
		}
	}
	for _, o := range answers {
		add(o)
	}
	var out []output
	for k := outFile; k <= outAnswer; k++ {
		out = append(out, byKind[k]...)
	}
	return out
}

func isSynthesisTask(t broker.TaskSnapshot) bool {
	return strings.Contains(strings.ToLower(t.Name+" "+t.ID), "synthesis")
}

// titleOf is a body's first line, flattened, for a list.
func titleOf(text string) string {
	return flattenMarkdown(firstLine(strings.TrimSpace(text)))
}

// outputs is what the Outputs view lists: the watched run's, or each step's
// of the flow it was opened from, step by step.
func (m tuiModel) outputs() []output {
	if len(m.outputsRuns) == 0 {
		return runOutputs(m.snap, 0)
	}
	var out []output
	for i, id := range m.outputsRuns {
		out = append(out, runOutputs(m.runSnapshot(id), i)...)
	}
	return out
}

// openOutputs shows the watched run's outputs, the first of task selected
// when task is set.
func (m *tuiModel) openOutputs(task string) tea.Cmd {
	m.outputsView, m.outputSel, m.outputScroll, m.outputsRuns = true, 0, 0, nil
	for i, o := range m.outputs() {
		if task != "" && o.task == task {
			m.outputSel = i
			break
		}
	}
	return m.fetchOutputDetails()
}

// openFlowOutputs shows every step's outputs, from the first of run's.
func (m *tuiModel) openFlowOutputs(runs []string, run string) tea.Cmd {
	m.outputsView, m.outputSel, m.outputScroll = true, 0, 0
	m.outputsRuns = append([]string(nil), runs...)
	for i, o := range m.outputs() {
		if o.run == run {
			m.outputSel = i
			break
		}
	}
	return m.fetchOutputDetails()
}

// outputDetail is what a command said about a PR, a branch or a commit.
type outputDetail struct {
	text    string
	err     error
	pending bool
}

type outputDetailMsg struct {
	key  string
	text string
	err  error
}

// fetchOutputDetails asks git and gh about every listed output they
// describe and that has not been asked about yet.
func (m *tuiModel) fetchOutputDetails() tea.Cmd {
	if m.outputDetails == nil {
		m.outputDetails = map[string]outputDetail{}
	}
	var cmds []tea.Cmd
	for _, o := range m.outputs() {
		if !o.fetched() {
			continue
		}
		if _, ok := m.outputDetails[o.key()]; ok {
			continue
		}
		m.outputDetails[o.key()] = outputDetail{pending: true}
		cmds = append(cmds, detailCmd(o))
	}
	return tea.Batch(cmds...)
}

// doOutputs runs the Outputs view's keys: j/k choose, J/K and pages read;
// o, e, y and enter do what suits the selected kind; esc goes back.
func (m tuiModel) doOutputs(action string) (tea.Model, tea.Cmd) {
	outs := m.outputs()
	rows := m.outputRows()
	sel := m.selectedOutput()
	switch action {
	case "outputs.back":
		m.outputsView, m.outputsRuns = false, nil
	case "outputs.down":
		if m.outputSel < len(outs)-1 {
			m.outputSel++
			m.outputScroll = 0
		}
		// A live run records more as it goes.
		cmd := m.fetchOutputDetails()
		return m, cmd
	case "outputs.up":
		if m.outputSel > 0 {
			m.outputSel--
			m.outputScroll = 0
		}
	case "outputs.pagedown":
		m.outputScroll += rows / 2
	case "outputs.pageup":
		m.outputScroll = max(0, m.outputScroll-rows/2)
	case "outputs.top":
		m.outputScroll = 0
	case "outputs.bottom":
		m.outputScroll = 1 << 20
	case "outputs.open":
		if sel != nil {
			return m, openOutput(*sel)
		}
	case "outputs.edit":
		if sel != nil && sel.kind == outFile {
			return m, editOutput(sel.path)
		}
	case "outputs.copy":
		if sel != nil {
			text, what := copyable(*sel)
			return m, copyCmd(text, what)
		}
	case "outputs.enter":
		if sel == nil {
			return m, nil
		}
		if sel.kind != outAnswer && sel.kind != outReport {
			return m, openOutput(*sel)
		}
		// To the task that gave it, in its run.
		m.outputsView, m.outputsRuns = false, nil
		m.flowView = false
		if sel.run != m.snap.RunID {
			cmd := m.openRun(sel.run)
			return m, cmd
		}
		m.openTask(sel.task, time.Time{})
	}
	return m, nil
}

// copyable is what y copies, and what to call it: the content for
// something read, the name for something referred to.
func copyable(o output) (string, string) {
	switch o.kind {
	case outFile:
		if b, err := os.ReadFile(o.path); err == nil {
			return string(b), filepath.Base(o.path)
		}
		return o.path, "the path"
	case outPR:
		return o.ref, "the PR's URL"
	case outBranch:
		return o.ref, "the branch name"
	case outCommit:
		return o.ref, "the commit hash"
	}
	return o.text, "the " + outputLabels[o.kind]
}

// openOutput opens an output whole, the way its kind is opened: a PR in the
// browser, a commit's diff or a branch's log in $PAGER, a file where it is,
// text through a temporary file.
func openOutput(o output) tea.Cmd {
	switch o.kind {
	case outPR:
		return openURLCmd(o.ref)
	case outCommit:
		return gitPagerCmd(o.cwd, "show", "--color=always", o.ref)
	case outBranch:
		return gitPagerCmd(o.cwd, "log", "--color=always", "--stat", branchRange(o.cwd, o.ref))
	case outFile:
		fi, err := os.Stat(o.path)
		if err != nil {
			return func() tea.Msg { return noticeMsg{err: err} }
		}
		if fi.IsDir() {
			return func() tea.Msg { return noticeMsg{err: fmt.Errorf("%s is a directory", o.path)} }
		}
		return pagerCmd(o.path, false)
	}
	f, err := os.CreateTemp("", "narwhal-output-*.md")
	if err != nil {
		return func() tea.Msg { return noticeMsg{err: err} }
	}
	path := f.Name()
	_, werr := f.WriteString(o.text + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return func() tea.Msg { return noticeMsg{err: werr} }
	}
	return pagerCmd(path, true)
}

// gitPagerCmd runs git in dir and pages what it prints.
func gitPagerCmd(dir string, args ...string) tea.Cmd {
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	script := `dir=$1; shift; git -C "$dir" --no-pager "$@" | ` + pager
	c := exec.Command("sh", append([]string{"-c", script, "narwhal-git", dir}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{err: fmt.Errorf("git %s: %w", args[0], err)}
		}
		return nil
	})
}

// editOutput opens a file in $EDITOR.
func editOutput(path string) tea.Cmd {
	return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{err: fmt.Errorf("editor: %w", err)}
		}
		return noticeMsg{text: "edited " + abbreviatePath(path)}
	})
}
