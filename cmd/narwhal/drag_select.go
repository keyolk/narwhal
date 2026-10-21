package main

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/selection"
)

// Drag selection, the way Claude Code's mod engine selects: a press picks the
// pane under it, a drag selects text inside that pane only (never a frame
// line, a column title or the other column), releasing copies, and the
// highlight stays until the next press or key. Mouse capture turned the
// terminal's own drag-select off, so without this nothing could be copied.
//
// Text panes with their whole content, which a drag past the edge scrolls:
// the result, a message and a task's tabs (detailScroll), the selected
// output on Outputs, and the selected step's output on the flow view.
//
// Text panes read from the frame, which cannot scroll by drag: the run
// view's right column (task, timeline, radio) and the result under the
// graph, the zoomed panes, Questions, and the flow's Work column. They draw
// a window chosen around a cursor or a live tail rather than one list of
// lines, so there is no whole content to hand over without redrawing them.
//
// List panes keep the second click as Enter: the home run list, the graph,
// the Outputs list and the palette.

// Pane IDs. Only the text panes that scroll are named for it.
const (
	paneDetail     = "detail"
	paneOutput     = "output"
	paneFlowStep   = "flow-step"
	paneOverlay    = "overlay"
	paneList       = "list"
	paneGraph      = "graph"
	paneReader     = "reader"
	paneSummary    = "summary"
	paneFlowWork   = "flow-work"
	paneOutputList = "output-list"
	paneQuestions  = "questions"
)

// selectionPanes lists what a drag can select on the screen view draws, from
// the geometry view and the mouse hit tests use.
func (m tuiModel) selectionPanes() []selection.Pane {
	if m.quit || m.width == 0 {
		return nil
	}
	// An overlay owns the screen: only its box is selectable.
	switch {
	case m.palette:
		lines, _ := m.paletteBox()
		return []selection.Pane{{ID: paneOverlay, Kind: selection.List, Rect: m.overlayInner(lines)}}
	case m.help:
		lines, _ := m.helpBox()
		return []selection.Pane{{ID: paneOverlay, Kind: selection.Text, Rect: m.overlayInner(lines)}}
	}
	switch {
	case m.picker:
		return m.homePanes()
	case m.questionsView:
		return m.questionsPanes()
	case m.outputsView:
		return m.outputsPanes()
	case m.flowView:
		return m.flowPanes()
	}
	if p, ok := m.detailPartsOpen(); ok {
		return []selection.Pane{{ID: paneDetail, Kind: selection.Text,
			Rect: p.rect(m.width, m.height), Lines: p.body, Offset: first(p.window())}}
	}
	if m.detail != detailClosed {
		return nil
	}
	return m.runPanes()
}

func first(a, _ int, _ string) int { return a }

// detailPartsOpen is the open detail screen's parts, when one is open and
// has something to show (the views draw a one-line notice otherwise).
func (m tuiModel) detailPartsOpen() (detailParts, bool) {
	switch m.detail {
	case detailResult:
		return m.resultParts(), true
	case detailMessage:
		if len(m.snap.Messages) > 0 {
			return m.messageParts(), true
		}
	case detailSession:
		if _, ok := m.selectedTask(); ok {
			return m.taskParts(), true
		}
	}
	return detailParts{}, false
}

// overlayInner is the box overlayRect places, inside its border and the one
// cell of padding each side; the whole screen when it is drawn without one.
func (m tuiModel) overlayInner(lines []string) mouse.Rect {
	r := m.overlayRect(lines)
	if m.width < 24 || m.height < 8 {
		return r
	}
	return r.InsetStyle(overlayBox)
}

func (m tuiModel) homePanes() []selection.Pane {
	if len(m.runs) == 0 {
		return nil
	}
	top, room := m.homeTop()
	lines, _, selTop := m.homeLines(m.width)
	start, end := homeWindow(len(lines), selTop, room)
	return []selection.Pane{{ID: paneList, Kind: selection.List,
		Rect: mouse.Rect{X: 0, Y: lineCount(top), W: m.width, H: end - start}}}
}

// runPanes is the run view: the graph and, beside it, the pane being read;
// under the graph, the result. A pane's first row is its title, which is
// the frame's top edge (or, zoomed, a title line), and is never selected.
func (m tuiModel) runPanes() []selection.Pane {
	g := m.runGeom()
	below := func(r mouse.Rect) mouse.Rect {
		if r.H < 1 {
			return mouse.Rect{}
		}
		return mouse.Rect{X: r.X, Y: r.Y + 1, W: r.W, H: r.H - 1}
	}
	var panes []selection.Pane
	if g.graph.H > 0 {
		panes = append(panes, selection.Pane{ID: paneGraph, Kind: selection.List, Rect: below(g.graph)})
	}
	if g.reader.H > 0 {
		panes = append(panes, selection.Pane{ID: paneReader, Kind: selection.Text, Rect: below(g.reader)})
	}
	if g.zoom < 0 {
		// The result under the graph fills the left column below it
		// (viewLeft); a zero rect when the graph takes the whole column.
		l := m.layout()
		h := l.top + 1
		if rest := h - g.graph.H; rest > 0 {
			panes = append(panes, selection.Pane{ID: paneSummary, Kind: selection.Text,
				Rect: mouse.Rect{X: g.graph.X, Y: g.top + g.graph.H, W: g.graph.W, H: rest}})
		}
	}
	return panes
}

// questionsPanes is the questions list as drawn below the head and prompt
// lines. It is kept on the selected question, not on an offset a drag could
// move, so it is read from the frame.
func (m tuiModel) questionsPanes() []selection.Pane {
	return []selection.Pane{{ID: paneQuestions, Kind: selection.Text,
		Rect: mouse.Rect{X: 0, Y: 2, W: m.width, H: max(m.height-2-lipgloss.Height(m.questionsFooter()), 0)}}}
}

func (m tuiModel) outputsPanes() []selection.Pane {
	g := m.outputsGeom()
	// The band's top edge is its titles; the columns start one row under it.
	y := g.top + 1
	panes := []selection.Pane{{ID: paneOutputList, Kind: selection.List,
		Rect: mouse.Rect{X: 2, Y: y, W: g.lw, H: g.rows}}}
	if g.lines != nil {
		panes = append(panes, selection.Pane{ID: paneOutput, Kind: selection.Text,
			Rect:  mouse.Rect{X: g.lw + 4, Y: y, W: g.rw, H: min(g.rows, len(g.lines)-g.scroll)},
			Lines: g.lines, Offset: g.scroll})
	}
	return panes
}

func (m tuiModel) flowPanes() []selection.Pane {
	g := m.flowGeometry()
	top := lipgloss.Height(g.head + "\n" + g.title)
	inner := g.bodyH - 2
	var panes []selection.Pane
	rx := 2
	if !m.flowZoom {
		rx = g.lw + 4
		panes = append(panes, selection.Pane{ID: paneFlowWork, Kind: selection.Text,
			Rect: mouse.Rect{X: 2, Y: top + 1, W: g.lw, H: inner}})
	}
	steps := m.flowRuns(m.flowID)
	outs := m.stepOutputs()
	if m.flowSel >= len(steps) || len(outs) == 0 {
		return panes
	}
	headRows := 1 + len(m.flowStepHead(g.rw, outs, m.stepOutputSel(outs)))
	s := m.flowStepScroll(g.rw, g.bodyH-1, headRows)
	h := min(s.rows, len(s.lines)-s.scroll, inner-(s.above-1))
	return append(panes, selection.Pane{ID: paneFlowStep, Kind: selection.Text,
		Rect:  mouse.Rect{X: rx, Y: top + s.above, W: g.rw, H: max(h, 0)},
		Lines: s.lines, Offset: s.scroll})
}

// selectMouse feeds a mouse event or an auto-scroll tick to the selection
// before any click handling. handled means the selection consumed it and it
// is not also a click.
func (m tuiModel) selectMouse(msg tea.Msg) (tuiModel, tea.Cmd, bool) {
	// view is the frame panes without whole content are read from, before
	// the highlight is drawn, so a highlight style never reaches a copy. It
	// is pure (a value receiver), so calling it here changes nothing.
	r, cmd := m.sel.Update(msg, m.selectionPanes, m.view, m.clock())
	if r.Scroll != 0 {
		m = m.scrollPaneTo(r.ScrollPane, r.Offset)
	}
	return m, cmd, r.Consumed
}

// scrollPaneTo puts line off of a text pane at its top, through the state
// its keys scroll. off is the pane's drawn offset plus the drag's scroll,
// so an offset parked past the end (G) never swallows a scroll up.
func (m tuiModel) scrollPaneTo(id string, off int) tuiModel {
	switch id {
	case paneDetail:
		if _, ok := m.detailPartsOpen(); !ok {
			return m
		}
		// What j and k do besides moving: a scrolled task view stops
		// following its newest output.
		m.detailScroll = max(off, 0)
		m.sessionTail = false
	case paneOutput:
		m.outputScroll = max(off, 0)
	case paneFlowStep:
		m.flowScroll = off
		m.scrollFlow(0) // clamps to the step's lines
	}
	return m
}

// noteCopied says how a copy went: its length, or why it failed (an error
// stays until a key: a copy that silently failed pastes the old clipboard).
func (m *tuiModel) noteCopied(msg selection.CopiedMsg) {
	var err error
	if msg.Err != nil {
		err = errors.New(msg.Note())
	}
	m.notifyFrom(srcCopy, msg.Note(), err)
}
