package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/toast"
)

// Mouse is an augmentation of the keyboard, not a second command set: every
// gesture below resolves to an action the keymap already names and runs it
// through the same do* function the key's binding does.
//
//   - home: click a run to select it; a second click on the selected run is
//     enter (home.open). The wheel moves the selection three runs.
//   - run view: click a graph row to focus the graph and select that task; a
//     second click on the selected task is enter (run.open). A click on a
//     tab of the right column, or anywhere in it, focuses that pane (keys 2,
//     3, 4). The wheel focuses the pane under it and moves three lines.
//   - help and palette: they own the mouse while open. The wheel scrolls
//     help or moves the palette selection; a click outside closes either
//     (esc); a click on a palette match runs it (enter on that match).
//   - every other screen, and any open text line (request, steer, answer):
//     ignored. A click never types.
//   - a drag, on any screen, selects text inside the pane under the press
//     (drag_select.go); a double or triple click in a text pane selects a
//     word or a line. Neither ever types into an open text line.
func (m tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.quit || m.width == 0 {
		return m, nil
	}
	if mouse.IsPress(msg) {
		// A click is a keystroke for what a key press clears: a toast
		// that was read, a remove that was asked for.
		m.toasts = toast.OnKey(m.toasts)
		if m.removeStep != "" {
			m.removeStep = ""
			m.toasts = toast.Dismiss(m.toasts, "remove")
		}
	}
	// The selection sees every event before any click handling, release and
	// motion included; a press that does not move stays the click below.
	m, selCmd, consumed := m.selectMouse(msg)
	if consumed {
		return m, selCmd
	}
	next, cmd := m.clickMouse(msg)
	return next, tea.Batch(selCmd, cmd)
}

// clickMouse is the click and wheel handling for an event the selection
// left to the app.
func (m tuiModel) clickMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.palette:
		return m.paletteMouse(msg)
	case m.help:
		return m.helpMouse(msg)
	case m.steering, m.composing, m.questionsView && m.answering:
		return m, nil
	case m.picker:
		return m.homeMouse(msg)
	case m.questionsView, m.outputsView, m.flowView, m.detail != detailClosed:
		return m, nil
	}
	next, cmd := m.runMouse(msg)
	if nm := next.(tuiModel); nm.focus != focusTasks {
		// What Update does after a key: the right column keeps showing the
		// pane last read when the graph takes the keys.
		nm.reader = nm.focus
		return nm, cmd
	}
	return next, cmd
}

func (m tuiModel) clock() time.Time {
	if m.mouseNow != nil {
		return m.mouseNow()
	}
	return time.Now()
}

// repeatAction runs do for |d| notches of the wheel: down for a positive d,
// up for a negative one.
func (m tuiModel) repeatAction(do func(tuiModel, string) (tea.Model, tea.Cmd), down, up string, d int) (tea.Model, tea.Cmd) {
	act := down
	if d < 0 {
		act, d = up, -d
	}
	var cmds []tea.Cmd
	for range d {
		next, c := do(m, act)
		m = next.(tuiModel)
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

// homeMouse is the run list.
func (m tuiModel) homeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		return m.repeatAction(tuiModel.doHome, "home.down", "home.up", d)
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	top, room := m.homeTop()
	lines, runAt, selTop := m.homeLines(m.width)
	start, end := homeWindow(len(lines), selTop, room)
	list := mouse.Rect{X: 0, Y: lineCount(top), W: m.width, H: end - start}
	hit := mouse.RowHit(list, start, len(lines), msg)
	if !hit.HasItem || runAt[hit.Index] < 0 {
		m.clicks = mouse.Clicks{} // a heading or blank click breaks a double
		return m, nil
	}
	i := runAt[hit.Index]
	wasSelected := m.runCur == i
	m.runCur = i
	var double bool
	m.clicks, double = m.clicks.Press("run:"+m.runs[i].RunID, m.clock())
	if double && wasSelected {
		return m.doHome("home.open")
	}
	return m, nil
}

// lineCount is how many rows s fills when each "\n" starts a new one and s
// ends with one, as homeTop does.
func lineCount(s string) int { return lipgloss.Height(s) - 1 }

// runGeom is where View draws the run view's panes. graph and reader are the
// panes' own cells, title row first, and are zero for a pane not on screen.
// leftCol and rightCol are the columns including the title edge, for clicks
// that land beside the pane's text.
type runGeom struct {
	zoom              focusPane
	top               int
	graph, reader     mouse.Rect
	leftCol, rightCol mouse.Rect
	tabX              int // first cell of the right column's tab title
}

// runGeom reads the same layout and heights viewScreen and viewBands do.
func (m tuiModel) runGeom() runGeom {
	header, footer := m.viewHeader(), m.viewFooter()
	bh := m.bodyHeightFor(header, footer)
	g := runGeom{top: lipgloss.Height(header)}
	l := m.layout()
	g.zoom = l.zoom
	if l.zoom >= 0 {
		body := mouse.Rect{X: 0, Y: g.top, W: m.width, H: bh}
		if l.zoom == focusTasks {
			g.graph, g.leftCol = body, body
			g.graph.H = bh
		} else {
			g.reader, g.rightCol = body, body
		}
		return g
	}
	h := l.top + 1
	g.graph = mouse.Rect{X: 2, Y: g.top, W: l.left, H: m.graphHeight(l.left, h)}
	g.leftCol = mouse.Rect{X: 1, Y: g.top, W: l.left + 1, H: h}
	g.reader = mouse.Rect{X: l.left + 4, Y: g.top, W: l.right, H: h}
	g.rightCol = mouse.Rect{X: l.left + 3, Y: g.top, W: l.right + 1, H: h}
	g.tabX = l.left + 5
	return g
}

// paneAction is the action of the key that focuses p.
func paneAction(p focusPane) string {
	switch p {
	case focusNode:
		return "pane.2"
	case focusTimeline:
		return "pane.3"
	case focusRadio:
		return "pane.4"
	}
	return "pane.1"
}

// focusPaneBy focuses p the way its number key does. Pane 4's key also
// turns its tab when it already has focus, which a click must not do, so a
// pane that has focus is left alone.
func (m tuiModel) focusPaneBy(p focusPane) tuiModel {
	if m.focus == p {
		return m
	}
	next, _ := m.doRun(paneAction(p))
	return next.(tuiModel)
}

// runMouse is the run view: the graph on the left, the pane being read on the
// right, or one pane across the body when zoomed or on a narrow terminal.
func (m tuiModel) runMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	g := m.runGeom()
	inGraph := g.leftCol.Contains(msg.X, msg.Y)
	inReader := g.rightCol.Contains(msg.X, msg.Y)
	if !inGraph && !inReader {
		return m, nil
	}
	pane := focusTasks
	if inReader {
		pane = m.readerPane()
		if g.zoom >= 0 {
			pane = g.zoom
		}
	}

	if d := mouse.Delta(msg); d != 0 {
		m = m.focusPaneBy(pane)
		switch pane {
		case focusTasks:
			return m.repeatAction(tuiModel.doRun, "graph.down", "graph.up", d)
		case focusNode:
			return m.repeatAction(tuiModel.doRun, "node.down", "node.up", d)
		}
		return m.repeatAction(tuiModel.doRun, "cursor.down", "cursor.up", d)
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}

	if inReader {
		m.clicks = mouse.Clicks{}
		if g.zoom < 0 && msg.Y == g.top {
			// The title row names the panes; a click on a tab is its key.
			x := g.tabX
			for _, t := range m.readerTabList() {
				w := lipgloss.Width(t.n + " " + t.label)
				if msg.X >= x && msg.X < x+w {
					pane = t.pane
					break
				}
				x += w + lipgloss.Width(" · ")
			}
		}
		return m.focusPaneBy(pane), nil
	}

	node, ok := m.graphNodeAt(g.graph, msg)
	if !ok {
		m.clicks = mouse.Clicks{} // the title or empty space breaks a double
		return m.focusPaneBy(focusTasks), nil
	}
	wasSelected := m.focus == focusTasks && m.taskCur == node
	m = m.focusPaneBy(focusTasks)
	m.selectNode(node)
	var double bool
	m.clicks, double = m.clicks.Press("task:"+m.taskByIndex(node).ID, m.clock())
	if double && wasSelected {
		return m.doRun("run.open")
	}
	return m, nil
}

// graphNodeAt is the task, as an index in layout order, drawn under the click
// in the graph pane drawn in r. It reads the window viewTasks and
// viewTasksBoxed draw, so it agrees with them about what is on screen.
func (m tuiModel) graphNodeAt(r mouse.Rect, msg tea.MouseMsg) (int, bool) {
	body := mouse.Rect{X: r.X, Y: r.Y + 1, W: r.W, H: r.H - 1}
	if !m.boxMode {
		rows := m.graphRows()
		hit := mouse.RowHit(body, m.laneStart(r.H, len(rows)), len(rows), msg)
		return hit.Index, hit.HasItem
	}
	rows := m.boxRows(r.W)
	hit := mouse.RowHit(body, m.boxStart(rows, r.H), len(rows), msg)
	if !hit.HasItem {
		return 0, false
	}
	col := msg.X - r.X
	for _, s := range rows[hit.Index].spans {
		if col >= s.x0 && col < s.x1 {
			return s.node, true
		}
	}
	return 0, false
}

// helpMouse keeps the help overlay's ownership of the mouse: nothing reaches
// the screen under it.
func (m tuiModel) helpMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		m.helpOff += d
		m.clampHelp()
		return m, nil
	}
	lines, _ := m.helpBox()
	if mouse.IsLeftPress(msg) && !m.overlayRect(lines).Contains(msg.X, msg.Y) {
		m.help = false // esc
	}
	return m, nil
}

// paletteMouse is helpMouse for the palette; a click on a match runs it.
func (m tuiModel) paletteMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	es := m.paletteEntries()
	if d := mouse.Delta(msg); d != 0 {
		m.paletteSel = max(0, min(m.paletteSel+d, len(es)-1))
		return m, nil
	}
	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	lines, g := m.paletteBox()
	box := m.overlayRect(lines)
	if !box.Contains(msg.X, msg.Y) {
		m.palette = false // esc
		return m, nil
	}
	// The matches start on the box's second line, inside the border and the
	// padding; on a terminal too small for a frame they start on the screen's.
	rows := mouse.Rect{X: box.X + 2, Y: box.Y + 2, W: g.innerW, H: 0}
	if m.width < 24 || m.height < 8 {
		rows.X, rows.Y = box.X, box.Y+1
	}
	start, end := m.paletteWindow(len(es), g)
	rows.H = end - start
	if hit := mouse.RowHit(rows, start, len(es), msg); hit.HasItem {
		m.paletteSel = hit.Index
		return m.runPaletteSel()
	}
	return m, nil
}
