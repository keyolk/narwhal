package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The monitor had two panes at fixed sizes. The node pane could not be
// focused, so it showed three lines of "recent" — enough to see a worker
// is alive, never enough to see what it is doing, which is the reason to
// open the monitor at all. And nothing could be made bigger: a long
// assignment or a busy channel had to be read through whatever fraction of
// the screen the layout had decided on.

func flexModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 120, 40
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskCompleted, Dispatches: 1},
		{ID: "task-2", State: broker.TaskDispatched, Dispatches: 1, Deps: []string{"task-1"}},
	}
	return m
}

func TestZoomGivesAPaneTheWholeBody(t *testing.T) {
	m := flexModel(t)
	m.focus = focusTasks
	body := m.height - 3 // header and footer, roughly

	m = press(m, "z")
	if m.zoom != focusTasks {
		t.Fatalf("z did not zoom the focused pane: %v", m.zoom)
	}
	if got := m.graphPaneWidth(); got != m.width {
		t.Errorf("a zoomed graph is %d columns wide, want the full %d", got, m.width)
	}
	if got := m.inspectorHeight(body); got != 0 {
		t.Errorf("the node pane still has %d rows while the graph is zoomed", got)
	}

	m = press(m, "z")
	if m.zoom != -1 {
		t.Fatalf("a second z did not restore the split: %v", m.zoom)
	}
	if got := m.graphPaneWidth(); got >= m.width {
		t.Errorf("the graph kept the full width after unzoom: %d", got)
	}
}

func TestNavigationAgreesWithAZoomedLayout(t *testing.T) {
	// This is the invariant the whole change turns on. graphPaneWidth is
	// what the graph is drawn at AND what navigation asks which boxes
	// share a row — so if zoom widened the pane without going through it,
	// `l` would step to a box that is not where the user sees it.
	m := flexModel(t)
	m.boxMode = true
	m.focus = focusTasks
	m.snap.Tasks = fanShape()

	m.zoom = focusTasks
	positions := m.boxPositions()
	if len(positions) == 0 {
		t.Fatal("no boxes were laid out")
	}
	widest := 0
	for _, p := range positions {
		if p.x1 > widest {
			widest = p.x1
		}
	}
	// A zoomed graph is drawn across the terminal, so the layout must use
	// that width — at the unzoomed cap of 52 the row would wrap.
	if widest <= 52 {
		t.Errorf("the zoomed layout is still %d columns wide; navigation is "+
			"computing against the unzoomed pane", widest)
	}

	// And movement still lands on real boxes.
	selectTask(t, &m, "task-1")
	m.moveHorizontal(1)
	if got := currentID(m); got != "task-2" {
		t.Errorf("right from task-1 went to %s while zoomed", got)
	}
}

func TestZoomFollowsTheFocus(t *testing.T) {
	// Moving focus while zoomed and leaving the old pane on screen would
	// put the keys somewhere invisible, which reads as the keyboard having
	// stopped working.
	m := flexModel(t)
	m.focus = focusTasks
	m = press(m, "z", "3")

	if m.focus != focusRadio {
		t.Fatalf("3 did not focus the radio: %v", m.focus)
	}
	if m.zoom != focusRadio {
		t.Errorf("the zoom stayed on %v after the focus moved to the radio", m.zoom)
	}
}

func TestWidthKeysResizeTheGraph(t *testing.T) {
	m := flexModel(t)
	before := m.graphPaneWidth()

	m = press(m, ">")
	if m.graphPaneWidth() <= before {
		t.Errorf("> did not widen the graph: %d -> %d", before, m.graphPaneWidth())
	}
	m = press(m, "<", "<")
	if m.graphPaneWidth() >= before {
		t.Errorf("< did not narrow the graph below %d: %d", before, m.graphPaneWidth())
	}
}

func TestResizeCannotStarveTheOtherPane(t *testing.T) {
	// Held down, a resize key must stop somewhere usable rather than
	// leaving one pane a sliver.
	m := flexModel(t)
	for i := 0; i < 50; i++ {
		m = press(m, ">")
	}
	if got := m.graphPaneWidth(); got > m.width-20 {
		t.Errorf("the graph grew to %d of %d columns, leaving nothing beside it",
			got, m.width)
	}

	m = flexModel(t)
	for i := 0; i < 50; i++ {
		m = press(m, "<")
	}
	if got := m.graphPaneWidth(); got < 20 {
		t.Errorf("the graph shrank to %d columns", got)
	}
}

func TestHeightKeysResizeTheNodePane(t *testing.T) {
	m := flexModel(t)
	body := 30
	before := m.inspectorHeight(body)
	if before == 0 {
		t.Fatal("setup: the node pane is not shown at this size")
	}

	m = press(m, "+")
	if m.inspectorHeight(body) <= before {
		t.Errorf("+ did not grow the node pane: %d", m.inspectorHeight(body))
	}
	m = press(m, "-", "-")
	if m.inspectorHeight(body) >= before {
		t.Errorf("- did not shrink the node pane: %d", m.inspectorHeight(body))
	}
}

func TestHeightResizeLeavesTheRadioReadable(t *testing.T) {
	m := flexModel(t)
	body := 30
	for i := 0; i < 40; i++ {
		m = press(m, "+")
	}
	if got := m.inspectorHeight(body); got > body-5 {
		t.Errorf("the node pane took %d of %d rows, leaving no radio", got, body)
	}
}

func TestTheNodePaneScrolls(t *testing.T) {
	// The pane holds the worker's activity now, so it has to be readable
	// past the bottom of the pane.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 40)

	if m.nodeScroll != nodeScrollTail {
		t.Fatalf("setup: the pane does not start at the tail: %d", m.nodeScroll)
	}
	m = press(m, "k")
	if m.nodeScroll == nodeScrollTail {
		t.Fatal("k did not leave the tail")
	}
	up := m.nodeScroll
	m = press(m, "g")
	if m.nodeScroll != 0 {
		t.Errorf("g did not go to the start: %d", m.nodeScroll)
	}
	m = press(m, "G")
	if m.nodeScroll != nodeScrollTail {
		t.Errorf("G did not return to the tail: %d", m.nodeScroll)
	}
	if up <= 0 {
		t.Errorf("scrolling up landed at %d", up)
	}
}

func TestScrollingTheNodePaneDoesNotMoveTheGraph(t *testing.T) {
	// Each pane owns j/k while it has the focus. Scrolling the node pane
	// and having the graph cursor move underneath would change what the
	// pane is showing while you read it.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 1

	m = press(m, "j", "k", "j")
	if m.taskCur != 1 {
		t.Errorf("the graph cursor moved to %d while the node pane had focus", m.taskCur)
	}
}

func TestTheNodePaneShowsWhereItIs(t *testing.T) {
	// A window into 200 lines that says nothing about being a window
	// reads as the whole story.
	m := flexModel(t)
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 40)

	out := stripEscapes(m.viewInspector(60, 12))
	if !strings.Contains(out, "/") {
		t.Errorf("the activity heading does not say how much there is:\n%s", out)
	}
}

func TestFooterHintsFollowTheFocus(t *testing.T) {
	// One fixed line could not cover three panes, zoom and resize without
	// becoming a wall nobody reads.
	m := flexModel(t)

	m.focus = focusTasks
	if got := m.footerKeys(); !strings.Contains(got, "width") {
		t.Errorf("the graph hints do not mention resizing: %q", got)
	}
	m.focus = focusNode
	if got := m.footerKeys(); !strings.Contains(got, "scroll") {
		t.Errorf("the node hints do not mention scrolling: %q", got)
	}
	m.focus = focusRadio
	if got := m.footerKeys(); !strings.Contains(got, "follow") {
		t.Errorf("the radio hints do not mention following: %q", got)
	}
}

func TestTheFooterSaysHowToUnzoom(t *testing.T) {
	// A zoomed pane hides the others; the way back has to be on screen.
	m := flexModel(t)
	m.zoom = focusRadio
	if got := stripEscapes(m.footerKeys()); !strings.Contains(got, "unzoom") {
		t.Errorf("a zoomed view does not say how to get out: %q", got)
	}
}

// giveNodeActivity records a session for a task and files a transcript
// with n entries, so the node pane has real length to scroll through.
func giveNodeActivity(t *testing.T, m *tuiModel, taskID string, n int) {
	t.Helper()
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"type":"assistant","timestamp":"2026-08-15T10:%02d:00Z",`+
				`"message":{"content":[{"type":"text","text":"finding number %d"}]}}`,
			i%60, i))
	}
	// writeTranscript sets HOME and returns the cwd it filed under, so the
	// model has to point at both.
	m.live.CWD = writeTranscript(t, sid, lines)
	writeSessionID(t, *m, taskID, sid)
}

func TestMovingToAnotherNodeRewindsTheActivity(t *testing.T) {
	// The scroll offset belongs to the node you were reading. Carried to
	// the next one it lands you in the middle of a different worker's
	// transcript, at a position that means nothing there.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 40)

	m = press(m, "k", "k", "k")
	if m.nodeScroll == nodeScrollTail {
		t.Fatal("setup: the pane is still at the tail")
	}

	m.focus = focusTasks
	m = press(m, "j")
	if m.taskCur == 0 {
		t.Skip("j did not move the cursor in this layout")
	}
	if m.nodeScroll != nodeScrollTail {
		t.Errorf("the new node opened at offset %d, inherited from the last one",
			m.nodeScroll)
	}
}

func TestEachPressScrollsOneLine(t *testing.T) {
	// Leaving the tail resolved the offset to the line count — the
	// window's END, not its start — so every press inside the visible
	// window moved the number without moving the screen. On a 14-row pane
	// against 708 lines, eight presses scrolled two lines.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 200)

	rows := m.nodeActivityRows()
	if rows < 3 {
		t.Fatalf("setup: the pane shows %d activity rows", rows)
	}
	total := m.nodeLineCount()
	firstStart, _ := activityWindow(nodeScrollTail, rows, total)

	const presses = 5
	for i := 0; i < presses; i++ {
		m = press(m, "k")
	}
	start, _ := activityWindow(m.nodeScroll, rows, total)

	if got := firstStart - start; got != presses {
		t.Errorf("%d presses moved the window %d lines, want %d", presses, got, presses)
	}
}

func TestScrollingReachesTheStart(t *testing.T) {
	// The end of the same defect: if presses are being eaten, the top of
	// a long feed is a lot further away than it looks.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 60)

	total := m.nodeLineCount()
	for i := 0; i < total+10; i++ {
		m = press(m, "k")
	}
	if m.nodeScroll != 0 {
		t.Errorf("scrolling up past the start parked at %d", m.nodeScroll)
	}
}

func TestScrollingBackDownReturnsToTheTail(t *testing.T) {
	// And re-arms following, so the pane keeps up with a live worker
	// again rather than pinning to a line number it has outgrown.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 60)

	for i := 0; i < 10; i++ {
		m = press(m, "k")
	}
	if m.nodeScroll == nodeScrollTail {
		t.Fatal("setup: still following")
	}
	for i := 0; i < 40; i++ {
		m = press(m, "j")
	}
	if m.nodeScroll != nodeScrollTail {
		t.Errorf("scrolling back down parked at %d instead of following", m.nodeScroll)
	}
}
