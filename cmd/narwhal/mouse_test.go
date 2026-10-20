package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/store"
)

// The coordinates below are found in View's output, not recomputed from the
// layout code: a test that used the code's own arithmetic would pass with
// the arithmetic wrong.

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// cellOf is where needle starts in the rendered view, in cells.
func cellOf(t *testing.T, view, needle string) (x, y int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(view), "\n") {
		if i := strings.Index(line, needle); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q is not on screen:\n%s", needle, ansi.Strip(view))
	return 0, 0
}

func mouseMsg(x, y int, b tea.MouseButton, a tea.MouseAction) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: a}
}

func send(m tuiModel, msg tea.MouseMsg) tuiModel {
	next, _ := m.Update(msg)
	return next.(tuiModel)
}

// clickAt presses the left button at the first cell of needle.
func clickAt(t *testing.T, m tuiModel, needle string) tuiModel {
	t.Helper()
	x, y := cellOf(t, m.View(), needle)
	return send(m, mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
}

func wheelAt(t *testing.T, m tuiModel, needle string, b tea.MouseButton) tuiModel {
	t.Helper()
	x, y := cellOf(t, m.View(), needle)
	return send(m, mouseMsg(x, y, b, tea.MouseActionPress))
}

func steady(m tuiModel) tuiModel {
	m.mouseNow = func() time.Time { return t0 }
	return m
}

func homeMouseModel(t *testing.T) tuiModel {
	now := time.Now().Unix()
	var runs []store.LiveRun
	for _, p := range []string{"alpha job", "bravo job", "charlie job", "delta job", "echo job", "foxtrot job"} {
		runs = append(runs, store.LiveRun{RunID: "run-" + p[:1], BrokerURL: "http://x", CWD: "/src/repo", Prompt: p,
			StartedAt: now - 600, State: "active", Tasks: 5, Done: 2, Running: 2})
	}
	return steady(homeModel(t, runs...))
}

func selectedRun(m tuiModel) string { return m.runs[m.runCur].RunID }

func TestMouseHomeClickSelectsAndSecondClickOpens(t *testing.T) {
	m := homeMouseModel(t)
	if selectedRun(m) == "run-c" {
		t.Fatal("setup: charlie is already selected")
	}
	m = clickAt(t, m, "charlie job")
	if selectedRun(m) != "run-c" {
		t.Fatalf("click selected %q, want run-c", selectedRun(m))
	}
	if !m.picker {
		t.Fatal("a single click opened the run")
	}
	m = clickAt(t, m, "charlie job")
	if m.picker || m.live.RunID != "run-c" {
		t.Fatalf("second click did not open run-c (picker=%v live=%q)", m.picker, m.live.RunID)
	}
}

// A key between two clicks breaks the pair. Click a run, j, k back to it,
// click again inside the double-click window: that is a fresh first click,
// so it selects and must not open the run.
func TestMouseHomeKeyBetweenClicksBreaksTheDoubleClick(t *testing.T) {
	m := homeMouseModel(t)
	m = clickAt(t, m, "charlie job")
	for _, k := range []string{"j", "k"} {
		next, _ := m.Update(keyMsg(k))
		m = next.(tuiModel)
	}
	if selectedRun(m) != "run-c" {
		t.Fatalf("selected %q after j, k; want back on run-c", selectedRun(m))
	}
	m = clickAt(t, m, "charlie job")
	if !m.picker {
		t.Fatalf("a key between the clicks must break the double click; run %q opened", m.live.RunID)
	}
	if selectedRun(m) != "run-c" {
		t.Fatalf("selected %q, want run-c still selected", selectedRun(m))
	}
}

func TestMouseHomeSecondClickOnAnotherRunOnlySelects(t *testing.T) {
	m := homeMouseModel(t)
	m = clickAt(t, m, "charlie job")
	m = clickAt(t, m, "delta job")
	if !m.picker || selectedRun(m) != "run-d" {
		t.Fatalf("two clicks on different runs: picker=%v selected=%q", m.picker, selectedRun(m))
	}
}

func TestMouseHomeClickOnTheSecondLineOfARowSelectsIt(t *testing.T) {
	m := homeMouseModel(t)
	x, y := cellOf(t, m.View(), "charlie job")
	m = send(m, mouseMsg(x, y+1, tea.MouseButtonLeft, tea.MouseActionPress)) // the path line
	if selectedRun(m) != "run-c" {
		t.Fatalf("the second line of a row selected %q", selectedRun(m))
	}
}

func TestMouseHomeWheelMovesThree(t *testing.T) {
	m := homeMouseModel(t)
	m.runCur = 1
	m = wheelAt(t, m, "alpha job", tea.MouseButtonWheelDown)
	if m.runCur != 4 {
		t.Fatalf("wheel down moved to %d, want 4", m.runCur)
	}
	m = wheelAt(t, m, "alpha job", tea.MouseButtonWheelUp)
	if m.runCur != 1 {
		t.Fatalf("wheel up moved to %d, want 1", m.runCur)
	}
}

func TestMouseHomeHeadingAndEmptyAreaDoNothing(t *testing.T) {
	m := homeMouseModel(t)
	before := m.runCur
	m = clickAt(t, m, "In progress")
	m = send(m, mouseMsg(5, m.height-4, tea.MouseButtonLeft, tea.MouseActionPress)) // below the list
	m = send(m, mouseMsg(5, 0, tea.MouseButtonLeft, tea.MouseActionPress))          // the header
	if m.runCur != before || !m.picker {
		t.Fatalf("an empty click changed state: runCur=%d picker=%v", m.runCur, m.picker)
	}
}

func TestMouseHomeIgnoresReleaseMotionAndOtherButtons(t *testing.T) {
	m := homeMouseModel(t)
	x, y := cellOf(t, m.View(), "charlie job")
	for _, msg := range []tea.MouseMsg{
		mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionRelease),
		mouseMsg(x, y, tea.MouseButtonNone, tea.MouseActionMotion),
		mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionMotion),
		mouseMsg(x, y, tea.MouseButtonRight, tea.MouseActionPress),
		mouseMsg(x, y, tea.MouseButtonMiddle, tea.MouseActionPress),
	} {
		if got := send(m, msg); got.runCur != m.runCur || !got.picker {
			t.Fatalf("%+v changed the home screen", msg)
		}
	}
}

func TestMouseHomeRequestLineKeepsTheMouse(t *testing.T) {
	m := homeMouseModel(t)
	m = pressRun(m, "n") // home.new opens the request line
	if !m.composing {
		t.Fatal("setup: the request line is not open")
	}
	before := m.runCur
	m = clickAt(t, m, "charlie job")
	m = wheelAt(t, m, "charlie job", tea.MouseButtonWheelDown)
	if m.runCur != before || !m.composing || len(m.composeBuf) != 0 {
		t.Fatalf("the mouse reached the home list or typed: runCur=%d composing=%v buf=%q", m.runCur, m.composing, string(m.composeBuf))
	}
}

func runMouseModel(t *testing.T, box bool) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(6, 12)
	m.width, m.height = 100, 28
	m.boxMode = box
	m.focus = focusRadio
	return steady(m)
}

// taskCell is where task id is drawn in the graph pane, left of the divider:
// its label in a lane row, or the middle of its box. The icon before a lane
// label is a glyph from the icon set, so the search skips it.
func taskCell(t *testing.T, m tuiModel, id string) (int, int) {
	t.Helper()
	x, y, ok := findTask(m, id)
	if !ok {
		t.Fatalf("task %q is not on screen:\n%s", id, ansi.Strip(m.View()))
	}
	return x, y
}

func findTask(m tuiModel, id string) (x, y int, ok bool) {
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if y < 2 {
			continue // the header and the frame's top edge
		}
		left := ansi.Truncate(line, 34, "")
		for needle, off := range map[string]int{" " + id + " │": 1, " " + id + "  ": 1} {
			if i := strings.Index(left, needle); i >= 0 {
				return ansi.StringWidth(left[:i]) + off, y, true
			}
		}
	}
	return 0, 0, false
}

func wheelOnTask(t *testing.T, m tuiModel, id string, b tea.MouseButton) tuiModel {
	t.Helper()
	x, y := taskCell(t, m, id)
	return send(m, mouseMsg(x, y, b, tea.MouseActionPress))
}

func clickTask(t *testing.T, m tuiModel, id string) tuiModel {
	t.Helper()
	x, y := taskCell(t, m, id)
	return send(m, mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
}

func selectedID(m tuiModel) string {
	t, _ := m.selectedTask()
	return t.ID
}

func TestMouseGraphClickFocusesAndSelects(t *testing.T) {
	for _, box := range []bool{false, true} {
		m := runMouseModel(t, box)
		m = clickTask(t, m, "e")
		if m.focus != focusTasks || selectedID(m) != "e" {
			t.Fatalf("box=%v: click left focus=%v selected=%q", box, m.focus, selectedID(m))
		}
		if m.detail != detailClosed {
			t.Fatalf("box=%v: a single click opened the task", box)
		}
		m = clickTask(t, m, "b")
		if selectedID(m) != "b" {
			t.Fatalf("box=%v: second click on another task selected %q", box, selectedID(m))
		}
		if m.detail != detailClosed {
			t.Fatalf("box=%v: clicks on two tasks opened one", box)
		}
	}
}

func TestMouseGraphSecondClickOnSelectedTaskOpensIt(t *testing.T) {
	for _, box := range []bool{false, true} {
		m := runMouseModel(t, box)
		m = clickTask(t, m, "d")
		m = clickTask(t, m, "d")
		if m.detail != detailSession || selectedID(m) != "d" {
			t.Fatalf("box=%v: second click: detail=%v selected=%q", box, m.detail, selectedID(m))
		}
	}
}

// The graph shares the detector: a key between two clicks on a task breaks
// the pair, so the second click only selects and the task stays closed.
func TestMouseGraphKeyBetweenClicksBreaksTheDoubleClick(t *testing.T) {
	m := runMouseModel(t, false)
	m = clickTask(t, m, "d")
	for _, k := range []string{"j", "k"} {
		next, _ := m.Update(keyMsg(k))
		m = next.(tuiModel)
	}
	if selectedID(m) != "d" {
		t.Fatalf("selected %q after j, k; want back on d", selectedID(m))
	}
	m = clickTask(t, m, "d")
	if m.detail != detailClosed {
		t.Fatalf("a key between the clicks must break the double click; detail=%v", m.detail)
	}
	if selectedID(m) != "d" {
		t.Fatalf("selected %q, want d still selected", selectedID(m))
	}
}

func TestMouseGraphDoubleClickNeedsTheWindow(t *testing.T) {
	m := runMouseModel(t, false)
	m = clickTask(t, m, "d")
	m.mouseNow = func() time.Time { return t0.Add(time.Second) }
	m = clickTask(t, m, "d")
	if m.detail != detailClosed {
		t.Fatal("two clicks a second apart opened the task")
	}
}

func TestMouseWheelOverTheGraphFocusesItAndMovesThree(t *testing.T) {
	m := runMouseModel(t, false)
	m = wheelOnTask(t, m, "a", tea.MouseButtonWheelDown)
	if m.focus != focusTasks || m.taskCur != 3 {
		t.Fatalf("wheel down: focus=%v taskCur=%d, want graph and 3", m.focus, m.taskCur)
	}
	m = wheelOnTask(t, m, "a", tea.MouseButtonWheelUp)
	if m.taskCur != 0 {
		t.Fatalf("wheel up: taskCur=%d, want 0", m.taskCur)
	}
}

func TestMouseWheelOverTheReaderMovesItsOwnPane(t *testing.T) {
	m := runMouseModel(t, false)
	m.focus, m.followTail, m.radioCur = focusRadio, false, 0
	m = wheelAt(t, m, "message body", tea.MouseButtonWheelDown)
	if m.focus != focusRadio || m.radioCur != 3 || m.taskCur != 0 {
		t.Fatalf("wheel over the radio: focus=%v radioCur=%d taskCur=%d", m.focus, m.radioCur, m.taskCur)
	}
}

func TestMouseTabClickFocusesThatPane(t *testing.T) {
	m := runMouseModel(t, false)
	m = clickAt(t, m, "3 Timeline")
	if m.focus != focusTimeline {
		t.Fatalf("tab 3 focused %v", m.focus)
	}
	m = clickAt(t, m, "2 Task")
	if m.focus != focusNode {
		t.Fatalf("tab 2 focused %v", m.focus)
	}
	m = clickAt(t, m, "1 Graph")
	if m.focus != focusTasks {
		t.Fatalf("title 1 focused %v", m.focus)
	}
}

func TestMouseClickOnTheFocusedRadioTabDoesNotTurnIt(t *testing.T) {
	m := runMouseModel(t, false)
	m = clickAt(t, m, "4 Radio")
	if m.focus != focusRadio || m.briefTab {
		t.Fatalf("a click turned the tab: focus=%v brief=%v", m.focus, m.briefTab)
	}
}

func TestMouseClickInTheReaderFocusesItWithoutMovingTheGraph(t *testing.T) {
	m := runMouseModel(t, false)
	m.focus = focusTasks
	m.reader = focusTimeline
	x, y := cellOf(t, m.View(), "3 Timeline")
	m = send(m, mouseMsg(x, y+3, tea.MouseButtonLeft, tea.MouseActionPress)) // an empty cell below the tabs
	if m.focus != focusTimeline || m.taskCur != 0 {
		t.Fatalf("focus=%v taskCur=%d, want the timeline the right column showed", m.focus, m.taskCur)
	}
}

func TestMouseNarrowLayoutHasOnePane(t *testing.T) {
	m := runMouseModel(t, false)
	m.width = 60
	m = pressRun(m, "1") // the graph, across the body
	taskCell(t, m, "c")  // the narrow graph is drawn
	m = clickTask(t, m, "c")
	if selectedID(m) != "c" || m.focus != focusTasks {
		t.Fatalf("narrow click selected %q (focus %v)", selectedID(m), m.focus)
	}
	m = clickTask(t, m, "c")
	if m.detail != detailSession {
		t.Fatal("narrow second click did not open the task")
	}
}

func TestMouseNarrowWheelMovesTheShownPane(t *testing.T) {
	m := runMouseModel(t, false)
	m.width = 60
	m = pressRun(m, "1")
	m = wheelOnTask(t, m, "a", tea.MouseButtonWheelDown)
	if m.taskCur != 3 {
		t.Fatalf("narrow wheel moved to %d", m.taskCur)
	}
}

func TestMouseRunViewIgnoresReleaseMotionAndEmptyAreas(t *testing.T) {
	m := runMouseModel(t, false)
	x, y := taskCell(t, m, "e")
	for _, msg := range []tea.MouseMsg{
		mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionRelease),
		mouseMsg(x, y, tea.MouseButtonNone, tea.MouseActionMotion),
		mouseMsg(x, y, tea.MouseButtonRight, tea.MouseActionPress),
		mouseMsg(x, y, tea.MouseButtonMiddle, tea.MouseActionPress),
		mouseMsg(0, 0, tea.MouseButtonLeft, tea.MouseActionPress),          // the header
		mouseMsg(5, m.height-1, tea.MouseButtonLeft, tea.MouseActionPress), // the footer
		mouseMsg(0, y, tea.MouseButtonLeft, tea.MouseActionPress),          // the frame
		mouseMsg(m.width+10, y, tea.MouseButtonLeft, tea.MouseActionPress), // off screen
	} {
		if got := send(m, msg); got.focus != m.focus || got.taskCur != m.taskCur || got.detail != detailClosed {
			t.Fatalf("%+v changed the run view: focus=%v taskCur=%d", msg, got.focus, got.taskCur)
		}
	}
	// An empty row of the graph pane, below the last task, selects nothing.
	_, ly := taskCell(t, m, "f")
	if got := send(m, mouseMsg(x, ly+1, tea.MouseButtonLeft, tea.MouseActionPress)); got.taskCur != 0 {
		t.Fatalf("a click below the last task selected %d", got.taskCur)
	}
}

func TestMouseSteeringLineKeepsTheMouse(t *testing.T) {
	m := runMouseModel(t, false)
	m = press(m, "i")
	if !m.steering {
		t.Fatal("setup: the steering line is not open")
	}
	got := clickTask(t, m, "e")
	got = wheelOnTask(t, got, "a", tea.MouseButtonWheelDown)
	if got.taskCur != 0 || got.focus != m.focus || !got.steering {
		t.Fatalf("the mouse reached the graph while a line was open: taskCur=%d focus=%v", got.taskCur, got.focus)
	}
}

func TestMouseOnOtherScreensIsIgnored(t *testing.T) {
	m := runMouseModel(t, false)
	m = clickTask(t, m, "d")
	m = clickTask(t, m, "d") // opens the task view
	if m.detail != detailSession {
		t.Fatal("setup: the task view is not open")
	}
	cur := m.taskCur
	got := send(m, mouseMsg(5, 5, tea.MouseButtonLeft, tea.MouseActionPress))
	got = send(got, mouseMsg(5, 5, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if got.detail != detailSession || got.taskCur != cur {
		t.Fatal("the mouse acted on the task view")
	}
}

func TestMouseHelpOwnsTheMouse(t *testing.T) {
	m := runMouseModel(t, false)
	m = press(m, "?")
	if !m.help {
		t.Fatal("setup: help is not open")
	}
	view := ansi.Strip(m.View())
	if strings.Contains(view, "●  e") && !strings.Contains(view, "Narwhal monitor keys") {
		t.Fatal("setup: help is not drawn")
	}

	// A click inside the box, and one on a task drawn beside it, leave the
	// graph alone; the first does not close help, the second does.
	inX, inY := cellOf(t, m.View(), "Narwhal monitor keys")
	got := send(m, mouseMsg(inX, inY, tea.MouseButtonLeft, tea.MouseActionPress))
	if !got.help || got.taskCur != 0 || got.focus != m.focus {
		t.Fatalf("a click inside help: help=%v taskCur=%d", got.help, got.taskCur)
	}
	got = send(m, mouseMsg(0, m.height-1, tea.MouseButtonLeft, tea.MouseActionPress))
	if got.help || got.taskCur != 0 || got.focus != m.focus || got.detail != detailClosed {
		t.Fatalf("a click outside help: help=%v taskCur=%d focus=%v", got.help, got.taskCur, got.focus)
	}

	// The wheel scrolls it and does not reach the graph or radio.
	got = send(m, mouseMsg(inX, inY, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if !got.help || got.helpOff != 3 || got.taskCur != 0 || got.radioCur != m.radioCur {
		t.Fatalf("wheel over help: help=%v helpOff=%d taskCur=%d", got.help, got.helpOff, got.taskCur)
	}
	got = send(got, mouseMsg(inX, inY, tea.MouseButtonWheelUp, tea.MouseActionPress))
	if got.helpOff != 0 {
		t.Fatalf("wheel up: helpOff=%d", got.helpOff)
	}
}

func TestMousePaletteOwnsTheMouse(t *testing.T) {
	m := runMouseModel(t, false)
	m = press(m, ":")
	if !m.palette {
		t.Fatal("setup: the palette is not open")
	}
	got := send(m, mouseMsg(0, m.height-1, tea.MouseButtonLeft, tea.MouseActionPress))
	if got.palette || got.taskCur != 0 || got.focus != m.focus {
		t.Fatalf("a click outside the palette: palette=%v taskCur=%d", got.palette, got.taskCur)
	}
	x, y := cellOf(t, m.View(), "run · down/up choose")
	got = send(m, mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
	if !got.palette {
		t.Fatal("a click on the palette's footer closed it")
	}
	got = send(m, mouseMsg(x, y, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if got.paletteSel != 3 || !got.palette {
		t.Fatalf("wheel over the palette: sel=%d", got.paletteSel)
	}
}

// A click on a palette match does what down-arrows to it and enter do.
func TestMousePaletteClickRunsThatMatch(t *testing.T) {
	m := runMouseModel(t, false)
	m = press(m, ":")
	es := m.paletteEntries()
	if len(es) < 4 {
		t.Fatalf("setup: only %d palette entries", len(es))
	}
	// Choose a match whose help text is unique on screen.
	var want tuiModel
	var target string
	for i, e := range es {
		if strings.Count(ansi.Strip(m.View()), e.Help) != 1 {
			continue
		}
		kb := m
		for range i {
			kb = send2(kb, tea.KeyMsg{Type: tea.KeyDown})
		}
		kb = send2(kb, tea.KeyMsg{Type: tea.KeyEnter})
		if kb.focus != m.focus || kb.zoom != m.zoom || kb.boxMode != m.boxMode || kb.widthDelta != m.widthDelta {
			want, target = kb, e.Help
			break
		}
	}
	if target == "" {
		t.Skip("no palette entry with a visible effect and a unique label")
	}
	got := clickAt(t, m, target)
	if got.palette {
		t.Fatal("a click on a match left the palette open")
	}
	if got.focus != want.focus || got.zoom != want.zoom || got.boxMode != want.boxMode || got.widthDelta != want.widthDelta {
		t.Fatalf("click on %q: focus=%v zoom=%v box=%v width=%d; the keys give focus=%v zoom=%v box=%v width=%d",
			target, got.focus, got.zoom, got.boxMode, got.widthDelta, want.focus, want.zoom, want.boxMode, want.widthDelta)
	}
}

func send2(m tuiModel, msg tea.Msg) tuiModel {
	next, _ := m.Update(msg)
	return next.(tuiModel)
}

func TestMouseOverlayOnATinyTerminalStillOwnsIt(t *testing.T) {
	m := runMouseModel(t, false)
	m.width, m.height = 20, 7
	m = press(m, "?")
	got := send(m, mouseMsg(3, 3, tea.MouseButtonLeft, tea.MouseActionPress))
	if !got.help || got.taskCur != 0 {
		t.Fatalf("on a terminal too small for a frame the help is the screen: help=%v", got.help)
	}
}

// A list that has scrolled draws row n of its window, not row n of the list.
func TestMouseHomeClickOnAScrolledListHitsTheRowDrawn(t *testing.T) {
	m := homeMouseModel(t)
	m.height = 14
	m.runCur = len(m.runs) - 1
	view := ansi.Strip(m.View())
	hidden := 0
	for _, r := range m.runs {
		if !strings.Contains(view, r.Prompt) {
			hidden++
		}
	}
	if hidden == 0 || !strings.Contains(view, m.runs[m.runCur].Prompt) {
		t.Fatalf("setup: the list has not scrolled:\n%s", view)
	}
	var drawn string
	for _, r := range m.runs {
		if strings.Contains(view, r.Prompt) && r.RunID != selectedRun(m) {
			drawn = r.Prompt
			break
		}
	}
	got := clickAt(t, m, drawn)
	if selectedRun(got) != "run-"+drawn[:1] {
		t.Fatalf("clicked %q on a scrolled list and selected %q", drawn, selectedRun(got))
	}
}

func TestMouseGraphClickOnAScrolledGraphHitsTheRowDrawn(t *testing.T) {
	for _, box := range []bool{false, true} {
		t.Setenv("HOME", t.TempDir())
		m := testModel(12, 3)
		m.width, m.height, m.boxMode, m.focus = 100, 16, box, focusTasks
		m = steady(m)
		m.taskCur = 11 // the window follows the cursor to the end of the graph
		if _, _, ok := findTask(m, "a"); ok {
			t.Fatalf("box=%v: setup: the graph has not scrolled:\n%s", box, ansi.Strip(m.View()))
		}
		var drawn string
		for _, id := range []string{"e", "f", "g", "h", "i", "j", "k"} {
			if _, _, ok := findTask(m, id); ok {
				drawn = id
				break
			}
		}
		if drawn == "" {
			t.Fatalf("box=%v: setup: no earlier task is drawn:\n%s", box, ansi.Strip(m.View()))
		}
		got := clickTask(t, m, drawn)
		if selectedID(got) != drawn {
			t.Fatalf("box=%v: clicked task %q on a scrolled graph and selected %q", box, drawn, selectedID(got))
		}
	}
}

// The home list maps one line to one run row. A line wider than the screen
// wraps in the terminal (View never shows it) and every click below lands one
// row low. A flow header's title is floored at 10 cells, which overflowed a
// narrow pane; every line must fit at every width.
func TestMouseHomeRowsFitTheScreen(t *testing.T) {
	now := time.Now().Unix()
	long := strings.Repeat("아주 긴 요청 very long prompt text ", 10)
	var runs []store.LiveRun
	for i, st := range []string{"active", "done", "failed"} {
		runs = append(runs, store.LiveRun{RunID: "run-" + st, BrokerURL: "http://x",
			CWD: "/src/" + strings.Repeat("deep/", 20) + "repo", Prompt: long, Flow: "flow-" + strings.Repeat("longflowname", 8),
			StartedAt: now - int64(600*(i+1)), State: st, Tasks: 5, Done: 2, Running: 1, Messages: 3})
	}
	runs = append(runs, store.LiveRun{RunID: "run-solo", CWD: "/src/" + strings.Repeat("deep/", 20) + "repo",
		Prompt: long, StartedAt: now - 60, State: "done", Tasks: 5, Done: 5})
	for _, w := range []int{30, 40, 50, 60, 80, 120} {
		m := homeModel(t, runs...)
		m.width, m.height = w, 30
		lines, _, _ := m.homeLines(w)
		for i, l := range lines {
			if n := ansi.StringWidth(l); n > w {
				t.Errorf("width %d: home line %d is %d cells: %q", w, i, n, ansi.Strip(l))
			}
		}
		for i, l := range strings.Split(m.View(), "\n") {
			if n := ansi.StringWidth(l); n > w {
				t.Errorf("width %d: view line %d is %d cells: %q", w, i, n, ansi.Strip(l))
			}
		}
	}
}
