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
		if m.flowOf(r) == flowID && !m.removedRuns[r.RunID] {
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
	m.removedRuns = map[string]bool{}
	for _, f := range flows {
		m.flows[f.ID] = f
		for _, s := range f.Steps {
			m.runFlow[s.RunID] = f.ID
		}
		for _, r := range f.Removed {
			m.removedRuns[r] = true
		}
	}
	m.mergeRuns(m.runs)
}

// openFlow shows a flow, with the step holding runID selected.
func (m *tuiModel) openFlow(flowID, runID string) {
	m.flowView, m.flowID = true, flowID
	m.flowSel, m.flowScroll, m.flowDetail, m.flowZoom, m.flowOutSel = 0, 0, false, false, -1
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
	if m.flowDetail {
		if next, cmd, ok := m.handleStepOutputKey(msg); ok {
			return next, cmd
		}
	}
	if msg.String() != "d" {
		// Any other key keeps a step a d asked to remove.
		m.removeStep = ""
	}
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
		// Into the step's outputs, where j/k choose one to read.
		m.flowDetail = true
		return m, m.fetchStepDetails()
	case "shift+tab", "h", "left":
		m.flowDetail = false
	case "z":
		// The detail across the whole width, for a long result.
		m.flowZoom = !m.flowZoom
		m.flowDetail = m.flowZoom || m.flowDetail
	case "j", "down":
		if m.flowSel < len(steps)-1 {
			m.flowSel++
			m.flowScroll, m.flowOutSel = 0, -1
		}
	case "k", "up":
		if m.flowSel > 0 {
			m.flowSel--
			m.flowScroll, m.flowOutSel = 0, -1
		}
	case "ctrl+d", "pgdown", "J", " ":
		m.scrollFlow(m.flowDetailRows() / 2)
	case "ctrl+u", "pgup", "K":
		m.scrollFlow(-m.flowDetailRows() / 2)
	case "g", "home":
		m.flowSel, m.flowScroll, m.flowOutSel = 0, 0, -1
	case "G", "end":
		m.flowSel, m.flowScroll, m.flowOutSel = max(0, len(steps)-1), 0, -1
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
	case "d":
		// Take the step out of the flow, on a second d: the run's record
		// stays, but the flow and every later plan stop reading it.
		if m.flowSel >= len(steps) {
			return m, nil
		}
		st := steps[m.flowSel]
		if m.removeStep != st.RunID {
			m.removeStep = st.RunID
			m.setNotice(fmt.Sprintf("remove step %d from the flow? d again to remove, any other key to keep it "+
				"(the run's record stays on disk)", m.flowSel+1), nil)
			return m, nil
		}
		m.removeStep = ""
		// Only a step with workers still at it is refused: one left
		// "active" with nobody working — a planner that exited, a daemon
		// that restarted — is exactly what a person wants gone.
		if st.Running > 0 {
			m.setNotice("", fmt.Errorf("step %d has %d workers running — cancel it first", m.flowSel+1, st.Running))
			return m, nil
		}
		f, removed, err := store.RemoveFlowStep(m.flowID, st.RunID)
		if err != nil {
			m.setNotice("", err)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("removed step %d: %s", m.flowSel+1, truncate(oneLine(removed.Request), 60)), nil)
		if len(f.Steps) == 0 {
			m.flowView, m.picker = false, true
		}
		m.flowSel = max(0, min(m.flowSel, len(steps)-2))
		m.flowScroll, m.flowOutSel = 0, -1
		return m, refreshFlows()
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

// handleStepOutputKey drives the step's outputs once the detail has the
// keys: j/k choose an output, J/K and pages read it, and o, e, y and enter
// do what suits its kind — the Outputs view's keys, on one step.
func (m tuiModel) handleStepOutputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	outs := m.stepOutputs()
	sel := m.stepOutputSel(outs)
	switch msg.String() {
	case "j", "down":
		if sel < len(outs)-1 {
			m.flowOutSel, m.flowScroll = sel+1, 0
		}
		return m, m.fetchStepDetails(), true
	case "k", "up":
		if sel > 0 {
			m.flowOutSel, m.flowScroll = sel-1, 0
		}
		return m, nil, true
	case "g", "home":
		m.flowScroll = 0
		return m, nil, true
	case "G", "end":
		m.scrollFlow(1 << 20)
		return m, nil, true
	}
	if sel >= len(outs) {
		return m, nil, false
	}
	o := outs[sel]
	switch msg.String() {
	case "o":
		return m, openOutput(o), true
	case "e":
		if o.kind == outFile {
			return m, editOutput(o.path), true
		}
	case "y":
		text, what := copyable(o)
		return m, copyCmd(text, what), true
	case "enter":
		if o.kind != outAnswer && o.kind != outReport {
			return m, openOutput(o), true
		}
		// To the task that gave it, in its run.
		cmd := m.openRun(o.run)
		m.openTask(o.task, time.Time{})
		return m, cmd, true
	}
	return m, nil, false
}

// fetchStepDetails asks gh and git about the selected step's PRs, branches
// and commits, the way the Outputs view does.
func (m *tuiModel) fetchStepDetails() tea.Cmd {
	if m.outputDetails == nil {
		m.outputDetails = map[string]outputDetail{}
	}
	var cmds []tea.Cmd
	for _, o := range m.stepOutputs() {
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
	keys := "j/k step · i resume the step · n next step · l/tab read · z widen · enter open run · o outputs · r result · e notes · x export · d remove step · c close · esc home"
	if m.flowDetail {
		keys = "j/k choose output · J/K space read · o open · e edit · y copy · enter its task · h back to steps · z widen · esc"
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

// flowDetailRows is how many lines of the shown output are on screen: the
// pane less its title, the step's head and the output's rule.
func (m tuiModel) flowDetailRows() int {
	g := m.flowGeometry()
	outs := m.stepOutputs()
	if len(m.flowRuns(m.flowID)) <= m.flowSel {
		return 1
	}
	head := len(m.flowStepHead(g.rw, outs, m.stepOutputSel(outs)))
	return max(1, g.bodyH-1-1-head-1)
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

// stepOutputs is the selected step's outputs, as the Outputs view lists
// them: what was produced first, then reports, then answers.
//
// The step's request leads the list, so a long one can still be read in
// full — the head shows only its first line.
func (m tuiModel) stepOutputs() []output {
	steps := m.flowRuns(m.flowID)
	if m.flowSel >= len(steps) {
		return nil
	}
	r := steps[m.flowSel]
	snap := m.runSnapshot(r.RunID)
	text := m.stepRequestText(m.flows[m.flowID], m.flowSel, r)
	req := output{kind: outRequest, run: r.RunID, step: m.flowSel, cwd: snap.CWD, task: "you",
		title: titleOf(text), text: text, ref: "request"}
	return append([]output{req}, runOutputs(snap, m.flowSel)...)
}

// stepOutputSel is the output shown: the chosen one, or before one is
// chosen, the step's answer — what the detail showed when it was a single
// block, so opening a step still reads its conclusion first.
func (m tuiModel) stepOutputSel(outs []output) int {
	if m.flowOutSel >= 0 && m.flowOutSel < len(outs) {
		return m.flowOutSel
	}
	for i, o := range outs {
		if o.kind == outAnswer {
			return i
		}
	}
	// Nothing answered yet: the first thing produced, else the request.
	return min(1, len(outs)-1)
}

// flowListCap bounds the output list above the reading pane, so a step
// that produced many things still leaves room to read one: a third of the
// pane, and never fewer than four rows.
func (m tuiModel) flowListCap() int {
	return max(4, (m.flowGeometry().bodyH-2)/3)
}

// flowStepHead is the top of the step's pane: its run, and the list of
// what it was asked and produced, the shown one marked.
func (m tuiModel) flowStepHead(w int, outs []output, sel int) []string {
	steps := m.flowRuns(m.flowID)
	r := steps[m.flowSel]
	lines := []string{styDim.Render(fmt.Sprintf("run %s · %s · %s", r.RunID, plainOutcome(r), ago(runStartTime(r))+" ago"))}
	// A window of the list around the shown output.
	limit := m.flowListCap()
	top := 0
	if len(outs) > limit {
		top = min(max(0, sel-limit/2), len(outs)-limit)
	}
	end := min(len(outs), top+limit)
	for i := top; i < end; i++ {
		o := outs[i]
		kind := outputKindStyle(o.kind).Render(padRight(outputLabels[o.kind], 7))
		row := kind + styDim.Render(padRight(truncate(o.task, 10), 11)) + truncate(o.title, max(4, w-20))
		if i == sel {
			mark := styCyanBold.Render("▸ ")
			if m.flowDetail {
				row = mark + stySel.Render(padRight(ansi.Strip(row), w-2))
			} else {
				row = mark + row
			}
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}
	if len(outs) > limit {
		lines = append(lines, styDim.Render(fmt.Sprintf("  %d-%d of %d", top+1, end, len(outs))))
	}
	if len(outs) <= 1 {
		lines = append(lines, styDim.Render("  nothing produced yet"))
	}
	return lines
}

// flowStepLines is the shown output's body, rendered to w: what scrolls.
func (m tuiModel) flowStepLines(w int) []string {
	outs := m.stepOutputs()
	if len(outs) == 0 {
		return nil
	}
	return m.outputBody(outs[m.stepOutputSel(outs)], w)
}

// flowStepDetail is the selected step's pane h rows tall: the list of
// what it produced over the shown output, which scrolls on its own.
func (m tuiModel) flowStepDetail(w, h int) string {
	steps := m.flowRuns(m.flowID)
	if m.flowSel >= len(steps) {
		return padRows([]string{paneTitle("Step", m.flowDetail, w)}, w, h)
	}
	outs := m.stepOutputs()
	sel := m.stepOutputSel(outs)
	head := m.flowStepHead(w, outs, sel)
	title := fmt.Sprintf("Step %d", m.flowSel+1)
	pane := []string{paneTitle(title, m.flowDetail, w)}
	pane = append(pane, head...)
	if len(outs) == 0 {
		return padRows(pane, w, h)
	}
	lines := m.flowStepLines(w)
	rows := max(1, h-len(pane)-1)
	scroll := min(m.flowScroll, max(0, len(lines)-rows))
	end := min(len(lines), scroll+rows)
	label := truncate(outs[sel].title, max(4, w-20))
	if len(lines) > rows {
		label += fmt.Sprintf("  %d-%d/%d", scroll+1, end, len(lines))
		if end < len(lines) {
			label += " ↓"
		}
	}
	pane = append(pane, styDim.Render("─ ")+styTitle.Render(label)+" "+styDim.Render(strings.Repeat("─", max(0, w-ansi.StringWidth(label)-3))))
	pane = append(pane, lines[scroll:end]...)
	return padRows(pane, w, h)
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
