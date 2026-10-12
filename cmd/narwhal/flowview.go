// flowview.go is the flow layer of the monitor: home groups runs into the
// flows they belong to, and the flow view shows one piece of work — its
// goal and notes, the runs it took, and what each was asked and produced.
//
//	home  →  flow  →  run  →  task
//
// A flow is read from ~/.narwhal/flows; a run that belongs to none (every
// run before flows, and batch runs) is a flow of one, and is adopted into
// a real one the first time someone continues it.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// flowsMsg carries the flows read from disk.
type flowsMsg []store.Flow

func refreshFlows() tea.Cmd {
	return func() tea.Msg { return flowsMsg(store.ListFlows()) }
}

// flowOf is the flow a run belongs to: its recorded one, or a flow file
// whose steps name it, or itself.
func (m tuiModel) flowOf(r store.LiveRun) string {
	if r.Flow != "" {
		return r.Flow
	}
	if id, ok := m.runFlow[r.RunID]; ok {
		return id
	}
	return r.RunID
}

// flowRuns is a flow's runs in step order: the order its file lists them,
// then any others claiming it, by start.
func (m tuiModel) flowRuns(flowID string) []store.LiveRun {
	byID := map[string]store.LiveRun{}
	for _, r := range m.runs {
		if m.flowOf(r) == flowID {
			byID[r.RunID] = r
		}
	}
	var out []store.LiveRun
	if f, ok := m.flows[flowID]; ok {
		for _, s := range f.Steps {
			if r, ok := byID[s.RunID]; ok {
				out = append(out, r)
				delete(byID, s.RunID)
			}
		}
	}
	var rest []store.LiveRun
	for _, r := range byID {
		rest = append(rest, r)
	}
	sort.Slice(rest, func(i, j int) bool { return runStartTime(rest[i]).Before(runStartTime(rest[j])) })
	return append(out, rest...)
}

// flowGroup is the home group of a flow: that of its latest step.
func (m tuiModel) flowGroup(flowID string) int {
	rs := m.flowRuns(flowID)
	if len(rs) == 0 {
		return 2
	}
	return runGroup(rs[len(rs)-1])
}

// flowTitle is how a flow is named: its title, or its first run's label.
func (m tuiModel) flowTitle(flowID string) string {
	if f, ok := m.flows[flowID]; ok && f.Title != "" {
		return f.Title
	}
	if rs := m.flowRuns(flowID); len(rs) > 0 {
		return runLabel(rs[0])
	}
	return flowID
}

// applyFlows takes the flows read from disk.
func (m *tuiModel) applyFlows(flows []store.Flow) {
	m.flows = map[string]store.Flow{}
	m.runFlow = map[string]string{}
	for _, f := range flows {
		m.flows[f.ID] = f
		for _, s := range f.Steps {
			m.runFlow[s.RunID] = f.ID
		}
	}
	m.mergeRuns(m.runs)
}

// openFlow shows a flow, with the step holding runID selected.
func (m *tuiModel) openFlow(flowID, runID string) {
	m.flowView, m.flowID = true, flowID
	m.flowSel, m.flowScroll, m.flowDetail, m.flowZoom = 0, 0, false, false
	for i, r := range m.flowRuns(flowID) {
		if r.RunID == runID {
			m.flowSel = i
		}
	}
	m.picker = false
}

// handleFlowKey drives the flow view.
func (m tuiModel) handleFlowKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	steps := m.flowRuns(m.flowID)
	switch msg.String() {
	case "q", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "esc":
		// Back out one level: the widened detail, then the step list,
		// then home.
		switch {
		case m.flowZoom:
			m.flowZoom = false
		case m.flowDetail:
			m.flowDetail = false
		default:
			m.flowView, m.picker = false, true
		}
	case "tab", "l", "right":
		// Into the step's detail, where j/k read rather than select.
		m.flowDetail = true
	case "shift+tab", "h", "left":
		m.flowDetail = false
	case "z":
		// The detail across the whole width, for a long result.
		m.flowZoom = !m.flowZoom
		m.flowDetail = m.flowZoom || m.flowDetail
	case "j", "down":
		if m.flowDetail {
			m.scrollFlow(1)
		} else if m.flowSel < len(steps)-1 {
			m.flowSel++
			m.flowScroll = 0
		}
	case "k", "up":
		if m.flowDetail {
			m.scrollFlow(-1)
		} else if m.flowSel > 0 {
			m.flowSel--
			m.flowScroll = 0
		}
	case "ctrl+d", "pgdown", "J", " ":
		m.scrollFlow(m.flowDetailRows() / 2)
	case "ctrl+u", "pgup", "K":
		m.scrollFlow(-m.flowDetailRows() / 2)
	case "g", "home":
		if m.flowDetail {
			m.flowScroll = 0
		} else {
			m.flowSel, m.flowScroll = 0, 0
		}
	case "G", "end":
		if m.flowDetail {
			m.scrollFlow(1 << 20)
		} else {
			m.flowSel, m.flowScroll = max(0, len(steps)-1), 0
		}
	case "enter":
		if m.flowSel < len(steps) {
			cmd := m.openRun(steps[m.flowSel].RunID)
			return m, cmd
		}
	case "o", "O":
		if m.flowSel < len(steps) {
			// Every step's outputs, from this step's: the work's product so
			// far, not one run's.
			runs := make([]string, len(steps))
			for i, s := range steps {
				runs[i] = s.RunID
			}
			return m, m.openFlowOutputs(runs, steps[m.flowSel].RunID)
		}
	case "r":
		if m.flowSel < len(steps) {
			cmd := m.openRun(steps[m.flowSel].RunID)
			m.detail = detailResult
			m.detailScroll = 0
			return m, tea.Batch(cmd, m.prStatusCmd())
		}
	case "i":
		// An instruction for the selected step: its workers resume with
		// it, where n plans a new step on top. See steer.go.
		if m.flowSel < len(steps) {
			m.startStepSteer(steps[m.flowSel], m.flowSel+1)
		}
	case "n":
		m.startCompose(m.flowID)
	case "e":
		cmd := m.openNotesEditor(m.flowID)
		return m, cmd
	case "x":
		return m, m.exportFlow(m.flowID)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if m.flowSel < len(steps) {
			snap := m.runSnapshot(steps[m.flowSel].RunID)
			return m, openArtifact(snap.Result().Artifacts, int(msg.String()[0]-'0'), snap.CWD)
		}
	case "c":
		f, ok := m.flows[m.flowID]
		if ok {
			if nf, err := store.SetFlowClosed(f.ID, !f.Closed); err != nil {
				m.homeErr = err
			} else {
				m.flows[f.ID] = nf
			}
		}
	}
	return m, nil
}

// openRun switches to a run by id and closes the flow view over it.
func (m *tuiModel) openRun(runID string) tea.Cmd {
	for i, r := range m.runs {
		if r.RunID == runID {
			m.flowView, m.picker = false, false
			return m.selectRun(i)
		}
	}
	return nil
}

// flowGeom is the flow view's layout, shared by drawing and scrolling so
// a scroll step moves exactly what is on screen.
type flowGeom struct {
	head, title, footer string
	lw, rw, bodyH       int
}

func (m tuiModel) flowGeometry() flowGeom {
	f := m.flows[m.flowID]
	steps := m.flowRuns(m.flowID)
	w := max(m.width, 1)
	state := styCyan.Render("open")
	if f.Closed {
		state = styDim.Render("closed")
	}
	all := broker.Artifacts{}
	for _, r := range steps {
		all.Merge(m.runSnapshot(r.RunID).Result().Artifacts)
	}
	g := flowGeom{}
	g.head = styDim.Render("‹ ") + styCyanBold.Render("Narwhal") + styDim.Render(" › flow   ") + state +
		styDim.Render(fmt.Sprintf("   %d steps · %d artifacts", len(steps), all.Count()))
	if ref := styDim.Render(m.flowID); ansi.StringWidth(g.head)+ansi.StringWidth(ref)+3 < w {
		g.head += strings.Repeat(" ", w-ansi.StringWidth(g.head)-ansi.StringWidth(ref)) + ref
	}
	g.title = "  " + styTitle.Render(truncate(m.flowTitle(m.flowID), w-3))
	keys := "j/k step · i resume the step · n next step · l/tab read · z widen · enter open run · o outputs · r result · e notes · x export · c close · esc home"
	if m.flowDetail {
		keys = "j/k scroll · space/ctrl-d ctrl-u page · g/G top/end · 1-9 open a PR or file · h/tab back to steps · z widen · n next step · esc home"
	}
	g.footer = styDim.Render(ansi.Truncate(keys, w, "…"))
	if m.composing {
		g.footer = m.composeLine(w) + "\n" + styDim.Render("enter start · ctrl-e open in $EDITOR · esc cancel")
	} else if m.steering || (m.steerMsg != "" && time.Since(m.steerAt) < steerNoticeFor) {
		g.footer = m.steerBar() + "\n" + g.footer
	} else if n := m.noticeLine(w); n != "" {
		g.footer = n + "\n" + g.footer
	}
	if m.homeErr != nil {
		g.footer = styRedBold.Render("✗ ") + styRed.Render(truncate(m.homeErr.Error(), w-3)) + "\n" + g.footer
	}
	g.bodyH = max(3, m.height-lipgloss.Height(g.head+"\n"+g.title)-lipgloss.Height(g.footer))
	if m.flowZoom {
		g.lw, g.rw = 0, max(10, w-4)
	} else {
		g.lw = min(max(w*2/5, 30), w-24)
		g.rw = max(10, w-g.lw-bandBorderCols)
	}
	return g
}

// flowDetailRows is how many lines of the step's detail are on screen.
func (m tuiModel) flowDetailRows() int {
	return max(1, m.flowGeometry().bodyH-3)
}

// scrollFlow moves the detail, clamped to its length: past the end, and
// a step back up took as many presses as the overshoot.
func (m *tuiModel) scrollFlow(delta int) {
	g := m.flowGeometry()
	total := len(m.flowStepLines(g.rw))
	m.flowScroll = min(max(0, m.flowScroll+delta), max(0, total-m.flowDetailRows()))
}

// viewFlow draws the flow view: the work on the left — goal, notes, steps
// — and the selected step's input and result on the right.
func (m tuiModel) viewFlow() string {
	f := m.flows[m.flowID]
	steps := m.flowRuns(m.flowID)
	g := m.flowGeometry()
	right := m.flowStepDetail(g.rw, g.bodyH-1)
	var body string
	if m.flowZoom {
		body = framedSingle(g.rw, g.bodyH-2, right, "")
	} else {
		body = framedBand(g.lw, g.rw, g.bodyH-2, m.flowLeft(f, steps, g.lw, g.bodyH-1), right)
	}
	return pinFooter(g.head+"\n"+g.title+"\n"+body, g.footer, m.height)
}

// flowLeft is the work's input and its runs.
func (m tuiModel) flowLeft(f store.Flow, steps []store.LiveRun, w, h int) string {
	rows := []string{paneTitle("Work", !m.flowDetail, w)}
	section := func(s string) { rows = append(rows, styTitle.Render(s)) }
	section("Goal")
	goal := f.Goal
	if goal == "" && len(steps) > 0 {
		goal = steps[0].Prompt
	}
	g := renderMarkdown(goal, w-2, lipgloss.NewStyle())
	if len(g) > 4 {
		g = append(g[:4], styDim.Render("…"))
	}
	for _, l := range g {
		rows = append(rows, "  "+l)
	}
	rows = append(rows, "")
	section("Notes")
	if strings.TrimSpace(f.Notes) == "" {
		rows = append(rows, styDim.Render("  none — e to write what every step should know"))
	} else {
		n := renderMarkdown(f.Notes, w-2, styYellow)
		if len(n) > 6 {
			n = append(n[:6], styDim.Render("  … e to see all"))
		}
		for _, l := range n {
			rows = append(rows, "  "+l)
		}
	}
	rows = append(rows, "")
	section("Steps")
	for i, r := range steps {
		icon, style := runIcon(r, m.frame)
		label := fmt.Sprintf("%d %s %s", i+1, style.Render(icon), truncate(m.stepRequest(f, i, r), w-12))
		prog := styDim.Render(fmt.Sprintf("%d/%d", r.Done, r.Tasks))
		line := fitCells(label, w-6) + prog
		if i == m.flowSel {
			line = stySel.Render(padRight(ansi.Strip(line), w))
		}
		rows = append(rows, line)
	}
	if len(steps) == 0 {
		rows = append(rows, styDim.Render("  (no runs found)"))
	}
	return padRows(rows, w, h)
}

// stepRequest is a step's request on one line, for a list.
func (m tuiModel) stepRequest(f store.Flow, i int, r store.LiveRun) string {
	return flattenMarkdown(m.stepRequestText(f, i, r))
}

// stepRequestText is what a step was asked, as written: the flow's record,
// else the run's prompt. Flattening it to one line, as the list needs,
// ran a paragraphed request together and lost its markdown.
func (m tuiModel) stepRequestText(f store.Flow, i int, r store.LiveRun) string {
	if i < len(f.Steps) && f.Steps[i].RunID == r.RunID && f.Steps[i].Request != "" {
		return f.Steps[i].Request
	}
	if strings.TrimSpace(r.Prompt) != "" {
		return r.Prompt
	}
	return runLabel(r)
}

// flowStepLines is the selected step's input and result, rendered to w.
func (m tuiModel) flowStepLines(w int) []string {
	f := m.flows[m.flowID]
	steps := m.flowRuns(m.flowID)
	if m.flowSel >= len(steps) {
		return nil
	}
	r := steps[m.flowSel]
	res := m.runSnapshot(r.RunID).Result()
	var lines []string
	add := func(name string) { lines = append(lines, "", styTitle.Render(name)) }
	lines = append(lines, styDim.Render(fmt.Sprintf("run %s · %s · %s", r.RunID, plainOutcome(r), ago(runStartTime(r))+" ago")))
	add("Asked")
	lines = append(lines, renderMarkdown(m.stepRequestText(f, m.flowSel, r), w, lipgloss.NewStyle())...)
	add("Result")
	if res.Answer == "" {
		lines = append(lines, styDim.Render("nothing yet"))
	} else {
		if res.AnswerFrom != "" {
			lines = append(lines, styDim.Render("from "+res.AnswerFrom))
		}
		lines = append(lines, renderMarkdown(res.Answer, w, lipgloss.NewStyle())...)
	}
	if !res.Artifacts.Empty() {
		add("Artifacts")
		lines = append(lines, m.artifactLines(res, w, true)...)
	}
	return lines
}

// flowStepDetail is the selected step's detail as a pane h rows tall,
// scrolled; its title says where in it the pane is.
func (m tuiModel) flowStepDetail(w, h int) string {
	lines := m.flowStepLines(w)
	if lines == nil {
		return padRows([]string{paneTitle("Step", m.flowDetail, w)}, w, h)
	}
	rows := max(1, h-1)
	scroll := min(m.flowScroll, max(0, len(lines)-rows))
	end := min(len(lines), scroll+rows)
	title := fmt.Sprintf("Step %d", m.flowSel+1)
	if len(lines) > rows {
		title += fmt.Sprintf("  %d-%d/%d", scroll+1, end, len(lines))
		if end < len(lines) {
			title += " ↓"
		}
	}
	return padRows(append([]string{paneTitle(title, m.flowDetail, w)}, lines[scroll:end]...), w, h)
}

// runIcon is a run's state glyph and colour.
func runIcon(r store.LiveRun, frame int) (string, lipgloss.Style) {
	switch {
	case runIsWorking(r):
		return spinnerAt(frame), styCyan
	case runIsOrphaned(r):
		return icons.runFailed, styYellow
	case r.Failed > 0 || r.State == "failed":
		return icons.runFailed, styRed
	case r.State == "canceled":
		return icons.runCanceled, styDim
	}
	return icons.runDone, styGreen
}

// runSnapshot is a run's snapshot: the watched one live, any other from
// disk, cached per poll.
func (m tuiModel) runSnapshot(runID string) broker.Snapshot {
	if runID == m.snap.RunID {
		return m.snap
	}
	if s, ok := m.snapCache[runID]; ok && time.Since(s.at) < 5*time.Second {
		return s.snap
	}
	s, err := store.LoadRun(runID)
	if err != nil {
		return broker.Snapshot{RunID: runID}
	}
	if m.snapCache != nil {
		m.snapCache[runID] = cachedSnap{s, time.Now()}
	}
	return s
}

type cachedSnap struct {
	snap broker.Snapshot
	at   time.Time
}

// flowStages is the header's strip of a multi-step flow: each step's state
// and progress, the watched one marked. "" for a run that is its flow.
func (m tuiModel) flowStages(width int) string {
	steps := m.flowRuns(m.flowOf(m.live))
	if len(steps) < 2 {
		return ""
	}
	var parts []string
	for i, r := range steps {
		icon, style := runIcon(r, m.frame)
		label := fmt.Sprintf("%d %s %s %d/%d", i+1, icon, truncate(runLabel(r), 18), r.Done, r.Tasks)
		if r.RunID == m.live.RunID {
			parts = append(parts, style.Bold(true).Render("["+label+"]"))
		} else {
			parts = append(parts, styDim.Render(label))
		}
	}
	return ansi.Truncate("  "+strings.Join(parts, styDim.Render("  →  ")), width, "…")
}

// stepRun moves to the previous or next step of the watched run's flow.
func (m *tuiModel) stepRun(delta int) (tea.Cmd, bool) {
	steps := m.flowRuns(m.flowOf(m.live))
	if len(steps) < 2 {
		return nil, false
	}
	for i, r := range steps {
		if r.RunID == m.live.RunID {
			j := i + delta
			if j < 0 || j >= len(steps) {
				return nil, true
			}
			return m.openRun(steps[j].RunID), true
		}
	}
	return nil, false
}

// exportFlow writes the flow's report and says where.
func (m tuiModel) exportFlow(flowID string) tea.Cmd {
	f, ok := m.flows[flowID]
	return func() tea.Msg {
		if !ok {
			var err error
			if f, err = store.LoadFlow(flowID); err != nil {
				return noticeMsg{err: err}
			}
		}
		path, err := store.ExportFlow(store.DefaultExportDir(), f)
		return noticeMsg{text: "flow report written to " + path, err: err}
	}
}

// adoptFlow makes sure a flow file exists for flowID, so a run from before
// flows — or a batch run — can be continued: the run becomes the first
// step of a flow named after it.
func (m tuiModel) adoptFlow(flowID string) error {
	if _, ok := m.flows[flowID]; ok {
		return nil
	}
	if _, err := store.LoadFlow(flowID); err == nil {
		return nil
	}
	for _, r := range m.runs {
		if r.RunID == flowID {
			_, err := store.StartFlow(r.RunID, r.CWD, r.Prompt)
			return err
		}
	}
	return fmt.Errorf("no run %s to continue", flowID)
}
