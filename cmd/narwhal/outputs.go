// outputs.go is every output of a run in one place, each readable where it
// is listed.
//
// A run's real product was rarely where its result pointed. The plan run
// on disk wrote a 22KB PLAN.md and its answer was a sentence naming it;
// three of its four investigators answered "see the results thread" and
// put the report on the radio. Reading any of it meant knowing which of
// the task's outcome, its artifacts, its radio posts or a file on disk to
// open, and the route to each was different.
//
// The Outputs view lists them together — files the tasks recorded, the
// reports they posted to the results thread, and each task's answer — and
// shows the selected one rendered beside the list. o opens it from a run,
// its result, a flow or a task's Summary; O from anywhere in a task.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// outputKind orders and labels outputs.
type outputKind int

const (
	outFile   outputKind = iota // a file a task recorded
	outReport                   // a post to the results thread
	outAnswer                   // a task's outcome
)

var outputLabels = map[outputKind]string{outFile: "file", outReport: "report", outAnswer: "answer"}

// output is one readable thing a run produced.
type output struct {
	kind  outputKind
	task  string // the task that produced it
	title string
	path  string // for a file
	text  string // for a report or an answer
}

// body is the output's content, read when it is shown.
func (o output) body() (string, error) {
	if o.kind != outFile {
		return o.text, nil
	}
	b, err := os.ReadFile(o.path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// runOutputs collects a run's outputs: files first, since a file a task
// recorded is usually the thing it was for; then reports; then answers,
// the synthesis's first.
func runOutputs(s broker.Snapshot) []output {
	var files, reports, answers []output
	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	seenFile := map[string]bool{}
	for _, t := range tasks {
		if t.Artifacts == nil {
			continue
		}
		for _, f := range t.Artifacts.Files {
			p := f
			if !filepath.IsAbs(p) && s.CWD != "" {
				p = filepath.Join(s.CWD, p)
			}
			if seenFile[p] {
				continue
			}
			seenFile[p] = true
			files = append(files, output{kind: outFile, task: t.ID, title: filepath.Base(p), path: p})
		}
	}
	for _, m := range s.Messages {
		if m == nil || m.ThreadID != broker.ResultsThread || broker.IsProtocol(m.Content) {
			continue
		}
		reports = append(reports, output{kind: outReport, task: strings.TrimPrefix(m.Sender, "worker-"),
			title: titleOf(m.Content), text: m.Content})
	}
	for _, t := range tasks {
		if strings.TrimSpace(t.Outcome) == "" {
			continue
		}
		o := output{kind: outAnswer, task: t.ID, title: titleOf(t.Outcome), text: t.Outcome}
		if isSynthesisTask(t) {
			answers = append([]output{o}, answers...)
		} else {
			answers = append(answers, o)
		}
	}
	return append(append(files, reports...), answers...)
}

func isSynthesisTask(t broker.TaskSnapshot) bool {
	return strings.Contains(strings.ToLower(t.Name+" "+t.ID), "synthesis")
}

// titleOf is a body's first line, flattened, for a list.
func titleOf(text string) string {
	return flattenMarkdown(firstLine(strings.TrimSpace(text)))
}

// outputsSnap is the run the Outputs view shows.
func (m tuiModel) outputsSnap() broker.Snapshot {
	if m.outputsRun != "" {
		return m.runSnapshot(m.outputsRun)
	}
	return m.snap
}

// openOutputs shows the outputs of the watched run, the first of task
// selected when task is set.
func (m *tuiModel) openOutputs(task string) {
	m.outputsView, m.outputSel, m.outputScroll, m.outputsRun = true, 0, 0, ""
	for i, o := range runOutputs(m.outputsSnap()) {
		if task != "" && o.task == task {
			m.outputSel = i
			break
		}
	}
}

// handleOutputsKey drives the Outputs view: j/k choose, J/K and pages
// read, o hands the output to $PAGER whole, esc goes back.
func (m tuiModel) handleOutputsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	outs := runOutputs(m.outputsSnap())
	rows := m.outputRows()
	switch msg.String() {
	case "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "esc", "q", "O":
		m.outputsView, m.outputsRun = false, ""
	case "j", "down":
		if m.outputSel < len(outs)-1 {
			m.outputSel++
			m.outputScroll = 0
		}
	case "k", "up":
		if m.outputSel > 0 {
			m.outputSel--
			m.outputScroll = 0
		}
	case "J", " ", "ctrl+d", "pgdown":
		m.outputScroll += rows / 2
	case "K", "ctrl+u", "pgup":
		m.outputScroll = max(0, m.outputScroll-rows/2)
	case "g", "home":
		m.outputScroll = 0
	case "G", "end":
		m.outputScroll = 1 << 20
	case "o", "enter":
		if m.outputSel < len(outs) {
			return m, pageOutput(outs[m.outputSel])
		}
	case "y":
		if m.outputSel < len(outs) {
			text, err := outs[m.outputSel].body()
			if err != nil {
				m.setNotice("", err)
				return m, nil
			}
			return m, copyCmd(text)
		}
	}
	return m, nil
}

// pageOutput opens an output whole in $PAGER: a file where it is, text
// through a temporary file.
func pageOutput(o output) tea.Cmd {
	if o.kind == outFile {
		if _, err := os.Stat(o.path); err != nil {
			return func() tea.Msg { return noticeMsg{err: err} }
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

// outputRows is the reading pane's height.
func (m tuiModel) outputRows() int { return max(3, m.height-6) }

// viewOutputs draws the list on the left and the selected output on the
// right, rendered.
func (m tuiModel) viewOutputs() string {
	w := max(m.width, 1)
	snap := m.outputsSnap()
	outs := runOutputs(snap)
	head := styDim.Render("‹ ") + styCyanBold.Render("Outputs") + styDim.Render(fmt.Sprintf("   %d from %s", len(outs), snap.RunID))
	title := "  " + truncate(oneLine(snap.Prompt), w-3)
	keys := styDim.Render(ansi.Truncate("j/k choose · J/K space page · g/G · o/enter $PAGER · y copy · esc back", w, "…"))
	if n := m.noticeLine(w); n != "" {
		keys = n + "\n" + keys
	}
	bodyH := max(3, m.height-2-lipgloss.Height(keys))
	lw := min(max(w/3, 28), 48)
	rw := max(10, w-lw-bandBorderCols)

	left := []string{paneTitle("Outputs", false, lw)}
	if len(outs) == 0 {
		left = append(left, styDim.Render("  nothing yet"))
	}
	for i, o := range outs {
		kind := outputLabels[o.kind]
		line := fmt.Sprintf("%-6s %-10s %s", kind, truncate(o.task, 10), o.title)
		if i == m.outputSel {
			left = append(left, stySel.Render(padRight(truncate(line, lw), lw)))
			continue
		}
		style := styDim
		if o.kind == outFile {
			style = styCyan
		}
		left = append(left, style.Render(padRight(kind, 6))+" "+styBlue.Render(padRight(truncate(o.task, 10), 10))+" "+
			truncate(o.title, max(4, lw-18)))
	}

	right := []string{paneTitle("", false, rw)}
	if m.outputSel < len(outs) {
		o := outs[m.outputSel]
		text, err := o.body()
		var lines []string
		src := o.task
		if o.kind == outFile {
			src = o.path
		}
		lines = append(lines, styDim.Render(outputLabels[o.kind]+" · "+src))
		if err != nil {
			lines = append(lines, "", styRed.Render(err.Error()))
		} else {
			lines = append(lines, "")
			lines = append(lines, renderMarkdown(text, rw, lipgloss.NewStyle())...)
		}
		rows := bodyH - 3
		scroll := min(m.outputScroll, max(0, len(lines)-rows))
		end := min(len(lines), scroll+rows)
		label := truncate(o.title, max(4, rw-20))
		if len(lines) > rows {
			label += fmt.Sprintf("  %d-%d/%d", scroll+1, end, len(lines))
			if end < len(lines) {
				label += " ↓"
			}
		}
		right = append([]string{paneTitle(label, true, rw)}, lines[scroll:end]...)
	}
	body := framedBand(lw, rw, bodyH-3, padRows(left, lw, bodyH-2), padRows(right, rw, bodyH-2))
	return pinFooter(head+"\n"+title+"\n"+body, keys, m.height)
}
