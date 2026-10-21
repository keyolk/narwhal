package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/selection"
	"github.com/keyolk/tuikit/tuitest"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// Coordinates come from where View drew text (cellOf), never from the pane
// arithmetic under test. The copy is captured through selection.Clipboard
// and the commands Update returns are run, the path the app takes; the
// real clipboard is never written.

func captureCopies(t *testing.T) *[]string {
	t.Helper()
	var (
		mu  sync.Mutex
		got []string
	)
	old := selection.Clipboard
	selection.Clipboard = func(s string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
		return nil
	}
	t.Cleanup(func() { selection.Clipboard = old })
	return &got
}

func lastCopy(c *[]string) string {
	if len(*c) == 0 {
		return ""
	}
	return (*c)[len(*c)-1]
}

// sendRun is send that also runs the command Update returned and feeds the
// copy's result back, as Bubble Tea would. An auto-scroll tick takes longer
// than the wait and is left out: tests that want it drain it themselves.
func sendRun(m tuiModel, msg tea.Msg) tuiModel {
	next, cmd := m.Update(msg)
	m = next.(tuiModel)
	for _, out := range tuitest.Drain(cmd, 10*time.Millisecond) {
		if c, ok := out.(selection.CopiedMsg); ok {
			next, _ = m.Update(c)
			m = next.(tuiModel)
		}
	}
	return m
}

// highlighted is the text drawn in reverse video, one entry per row.
func highlighted(m tuiModel) []string { return tuitest.Reversed(m.View()) }

func lpress(m tuiModel, x, y int) tuiModel {
	return sendRun(m, mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionPress))
}

func lmotion(m tuiModel, x, y int) tuiModel {
	return sendRun(m, mouseMsg(x, y, tea.MouseButtonLeft, tea.MouseActionMotion))
}

func lrelease(m tuiModel, x, y int) tuiModel {
	return sendRun(m, mouseMsg(x, y, tea.MouseButtonNone, tea.MouseActionRelease))
}

func dragFrom(m tuiModel, x0, y0, x1, y1 int) tuiModel {
	return lrelease(lmotion(lpress(m, x0, y0), x1, y1), x1, y1)
}

func screenRow(m tuiModel, y int) string {
	return ansi.Strip(strings.Split(m.View(), "\n")[y])
}

func TestDragFromTheGraphTowardTheRadioStaysInTheGraph(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	x0, y0 := taskCell(t, m, "b")
	_, y1 := taskCell(t, m, "d")
	bx, _ := cellOf(t, m.View(), "message body")
	m = dragFrom(m, x0, y0, bx+5, y1)
	got := lastCopy(c)
	if got == "" {
		t.Fatal("a drag copies on release")
	}
	if strings.Contains(got, "message") || strings.Contains(got, "│") {
		t.Fatalf("the copy reached past the graph into the radio or the divider:\n%q", got)
	}
	if !strings.Contains(got, "c") {
		t.Fatalf("the copy should hold the graph rows dragged over: %q", got)
	}
	// One highlighted run per row, all of them inside the graph: the last
	// ends on task d's row, and none reaches the divider or the radio.
	hl := highlighted(m)
	if len(hl) != y1-y0+1 || !strings.Contains(hl[len(hl)-1], "d") {
		t.Fatalf("the graph rows b..d should be highlighted, got %q", hl)
	}
	for _, run := range hl {
		if strings.Contains(run, "│") || strings.Contains(run, "message") {
			t.Fatalf("the highlight reached into the radio: %q", hl)
		}
	}
}

// resultModel is the result screen with an answer far longer than it.
func resultModel(t *testing.T) tuiModel {
	t.Helper()
	m := runMouseModel(t, false)
	var b strings.Builder
	for i := range 80 {
		fmt.Fprintf(&b, "answer line %02d\n\n", i)
	}
	m.snap.Tasks[0].State = broker.TaskCompleted
	m.snap.Tasks[0].Outcome = b.String()
	m.detail = detailResult
	return m
}

func TestDragPastTheBottomOfTheResultScrollsAndKeepsTheLinesThatLeft(t *testing.T) {
	c := captureCopies(t)
	m := resultModel(t)
	x0, y0 := cellOf(t, m.View(), "answer line 01")
	m = lpress(m, x0, y0)
	for range 10 {
		m = lmotion(m, m.width-1, m.height+2)
	}
	if strings.Contains(ansi.Strip(m.View()), "answer line 01") {
		t.Fatal("dragging past the bottom of the result should scroll it")
	}
	m = lrelease(m, m.width-1, m.height+2)
	got := lastCopy(c)
	if !strings.HasPrefix(got, "answer line 01") {
		t.Fatalf("the copy lost the lines that scrolled out: %q", got[:min(len(got), 60)])
	}
	last := ""
	for _, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.Contains(l, "answer line") {
			last = strings.TrimSpace(l)
		}
	}
	// The bottom row is the blank line under the last answer line drawn.
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), last) {
		t.Fatalf("the copy should end at the bottom row %q, got ...%q", last, got[max(len(got)-40, 0):])
	}
}

func TestDragInTheTaskViewLeavesTheGutterOut(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	m.snap.Tasks[0].Assignment = "first assignment line\nsecond assignment line"
	m = clickTask(t, m, "a")
	m = clickTask(t, m, "a") // the task view
	if m.detail != detailSession {
		t.Fatal("setup: the task view is not open")
	}
	x0, y0 := cellOf(t, m.View(), "first assignment line")
	x1, y1 := cellOf(t, m.View(), "second assignment line")
	end := x1 + len("second assignment line") - 1
	// From the f of "first": the copy starts at the cell pressed, so a pane
	// that counted the gutter as body would start one cell later.
	m = dragFrom(m, x0, y0, end, y1)
	if got := lastCopy(c); got != "first assignment line\n  second assignment line" {
		t.Fatalf("copy = %q", got)
	}
	m = press(m, "j") // clears the selection, and scrolls: find the lines again
	_, y0 = cellOf(t, m.View(), "first assignment line")
	x1, y1 = cellOf(t, m.View(), "second assignment line")
	end = x1 + len("second assignment line") - 1
	// From the end of the second line up to the screen's left edge, over
	// the gutter: the columns stop at the body. The summary indents its
	// lines by two cells; the gutter is the cell left of that.
	m = dragFrom(m, end, y1, 0, y0)
	if got := lastCopy(c); got != "  first assignment line\n  second assignment line" {
		t.Fatalf("copy = %q", got)
	}
}

func TestPressWithoutMotionIsStillAClickAndTheSecondIsEnter(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	x, y := taskCell(t, m, "e")
	m = lrelease(lpress(m, x, y), x, y)
	if len(*c) != 0 || m.sel.Active() {
		t.Fatal("a click selects and copies nothing")
	}
	if selectedID(m) != "e" {
		t.Fatalf("selected %q, want e: a press that does not move is a click", selectedID(m))
	}
	m = lpress(m, x, y)
	if m.detail != detailSession {
		t.Fatal("the second click on the selected task is still enter (run.open)")
	}
}

func TestAKeyClearsTheSelection(t *testing.T) {
	m := resultModel(t)
	x0, y0 := cellOf(t, m.View(), "answer line 01")
	_, y1 := cellOf(t, m.View(), "answer line 03")
	m = dragFrom(m, x0, y0, x0+5, y1)
	if !m.sel.Active() {
		t.Fatal("setup: the drag should leave a selection")
	}
	next, _ := m.Update(keyMsg("j"))
	if next.(tuiModel).sel.Active() {
		t.Fatal("a key ends the selection")
	}
}

func TestHelpOpenLeavesTheBaseUnselectable(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	// Task a's row in the graph, at the graph's first cell: the help box is
	// centred and leaves the left edge of the graph uncovered.
	_, ya := taskCell(t, m, "a")
	m = press(m, "?")
	if !strings.Contains(screenRow(m, ya), "a") {
		t.Fatalf("setup: the graph should still show beside help: %q", screenRow(m, ya))
	}
	got := dragFrom(m, 2, ya, 2, ya+2)
	if got.sel.Active() || len(*c) != 0 {
		t.Fatalf("a drag on the graph under help selected %q", lastCopy(c))
	}
	x, y := cellOf(t, m.View(), "Narwhal monitor keys")
	got = dragFrom(m, x, y, x+15, y)
	if s := lastCopy(c); !strings.HasPrefix(s, "Narwhal monitor") || strings.Contains(s, "│") {
		t.Fatalf("a drag in help selects its text only, got %q", s)
	}
	if !got.help {
		t.Fatal("a drag inside help must not close it")
	}
	// The cell left of the title is the box's padding, not its text: a
	// drag from there selects nothing that starts with it.
	*c = nil
	dragFrom(m, x-1, y, x+15, y)
	if s := lastCopy(c); strings.HasPrefix(s, " ") {
		t.Fatalf("a drag from the help box's padding copied it: %q", s)
	}
}

func TestDragWhileSteeringNeverTypes(t *testing.T) {
	m := runMouseModel(t, false)
	m = press(m, "i")
	if !m.steering {
		t.Fatal("setup: the steering line is not open")
	}
	for _, r := range "hello" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(tuiModel)
	}
	before := m.steerBuf
	x, y := cellOf(t, m.View(), "message body")
	got := dragFrom(m, x, y, x+7, y)
	if got.steerBuf.Value() != before.Value() || got.steerBuf.Cursor() != before.Cursor() || !got.steering {
		t.Fatalf("a drag edited, moved the cursor of or closed the steering line: %q at %d",
			got.steerBuf.Value(), got.steerBuf.Cursor())
	}
}

func TestCopiedMsgShowsAToast(t *testing.T) {
	m := runMouseModel(t, false)
	next, _ := m.Update(selection.CopiedMsg{Text: "abc"})
	if !strings.Contains(ansi.Strip(next.(tuiModel).View()), "copied 3 chars") {
		t.Fatal("a finished copy reports its length")
	}
	next, _ = m.Update(selection.CopiedMsg{Text: "x", Err: errors.New("no tty")})
	if !strings.Contains(ansi.Strip(next.(tuiModel).View()), "copy failed: no tty") {
		t.Fatal("a failed copy says so")
	}
}

func TestDragPastTheBottomOfAnOutputScrollsAndKeepsTheLinesThatLeft(t *testing.T) {
	c := captureCopies(t)
	m := withOutputs(t)
	var b strings.Builder
	for i := range 80 {
		fmt.Fprintf(&b, "plan line %02d\n\n", i)
	}
	must(t, os.WriteFile(filepath.Join(m.snap.CWD, "PLAN.md"), []byte(b.String()), 0o644))
	m = press(m, "o")
	if !m.outputsView {
		t.Fatal("setup: Outputs is not open")
	}
	x0, y0 := cellOf(t, m.View(), "plan line 01")
	m = lpress(m, x0, y0)
	for range 6 {
		m = lmotion(m, m.width-3, m.height+2)
	}
	if strings.Contains(ansi.Strip(m.View()), "plan line 01") {
		t.Fatal("dragging past the bottom of the output should scroll it")
	}
	// The bottom row as the release finds it: the copied toast that
	// follows takes a row from the pane.
	last := ""
	for _, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(l, "plan line"); i >= 0 {
			last = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(l, " "), "│"), " ")[i:]
		}
	}
	m = lrelease(m, m.width-3, m.height+2)
	got := lastCopy(c)
	if !strings.HasPrefix(got, "plan line 01") {
		t.Fatalf("the copy lost the lines that scrolled out: %q", got[:min(len(got), 60)])
	}
	if strings.Contains(got, "│") || strings.Contains(got, "Client findings") {
		t.Fatalf("the copy reached the frame or the list: %q", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), last) {
		t.Fatalf("the copy should run to the bottom row %q, got ...%q", last, got[max(len(got)-40, 0):])
	}
}

func TestDragPastTheBottomOfAFlowStepScrollsAndKeepsTheLinesThatLeft(t *testing.T) {
	c := captureCopies(t)
	m := flowModel(t)
	var b strings.Builder
	for i := range 60 {
		fmt.Fprintf(&b, "request line %02d\n\n", i)
	}
	if _, err := store.UpdateFlow("p1", false, func(f *store.Flow) error {
		f.Steps[1].Request = b.String()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.applyFlows(store.ListFlows())
	m.width, m.height = 100, 24
	m.openFlow("p1", "b2")
	m = press(m, "l", "g")
	for m.stepOutputs()[m.stepOutputSel(m.stepOutputs())].kind != outRequest {
		m = press(m, "k")
	}
	m = steady(m)
	x0, y0 := cellOf(t, m.View(), "request line 01")
	m = lpress(m, x0, y0)
	for range 6 {
		m = lmotion(m, m.width-3, m.height+2)
	}
	if m.flowScroll == 0 || strings.Contains(ansi.Strip(m.View()), "request line 01") {
		t.Fatalf("dragging past the bottom of the step should scroll it (flowScroll %d)", m.flowScroll)
	}
	m = lrelease(m, m.width-3, m.height+2)
	got := lastCopy(c)
	if !strings.HasPrefix(got, "request line 01") || strings.Contains(got, "│") || strings.Contains(got, "Goal") {
		t.Fatalf("copy = %q", got[:min(len(got), 80)])
	}
	if !strings.Contains(got, "request line 05") {
		t.Fatalf("the copy should run past the lines that scrolled out: %q", got)
	}
}

// G parks detailScroll far past the end (it means "the bottom, and stay"),
// and the result screen then shows its last line at the top. A drag past the
// top must still scroll up from the line drawn there.
func TestDragPastTheTopOfTheResultAfterGScrollsUp(t *testing.T) {
	c := captureCopies(t)
	m := resultModel(t)
	m = press(m, "G")
	// G leaves only the last line of the result drawn, task f's row, at the
	// top of the body; its status icon comes before the name.
	x, y := cellOf(t, m.View(), " f ")
	x++
	for _, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), " e") {
			t.Fatalf("setup: after G only task f's row should be drawn:\n%s", ansi.Strip(m.View()))
		}
	}
	m = lpress(m, x, y)
	for range 3 {
		m = lmotion(m, x, 0)
	}
	if strings.HasSuffix(strings.TrimSpace(screenRow(m, y)), " f") {
		t.Fatalf("a drag past the top after G did not scroll up:\n%s", ansi.Strip(m.View()))
	}
	m = lrelease(m, x, 0)
	lines := strings.Split(lastCopy(c), "\n")
	tail := func(i int) string {
		if i < 0 || i >= len(lines) {
			return ""
		}
		return strings.TrimSpace(lines[i])
	}
	n := len(lines)
	// From the scrolled-in rows down to f: f was the line under the press,
	// so the scroll had to start from the line drawn, not from past the end.
	if !strings.HasSuffix(tail(n-1), " f") || !strings.HasSuffix(tail(n-2), " e") || !strings.HasSuffix(tail(n-3), " d") {
		t.Fatalf("the copy should run from the lines scrolled in down to f: %q", lastCopy(c))
	}
}

func TestDragUpOverTheGraphTitleStopsAtItsFirstRow(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	x, y := taskCell(t, m, "c")
	// Up over the title edge to the screen's top row: the graph has no
	// whole content to scroll, so the rows stop at its first task.
	m = dragFrom(m, x, y, x, 0)
	got := lastCopy(c)
	if strings.Contains(got, "Graph") || strings.ContainsAny(got, "─╭┬") {
		t.Fatalf("the copy took the graph's title edge: %q", got)
	}
	if first := strings.SplitN(got, "\n", 2)[0]; !strings.HasSuffix(strings.TrimSpace(first), "a") {
		t.Fatalf("the copy should start at task a's row: %q", got)
	}
	hl := highlighted(m)
	for _, run := range hl {
		if strings.Contains(run, "Graph") || strings.ContainsAny(run, "─╭┬") {
			t.Fatalf("the highlight reached the title edge: %q", hl)
		}
	}
	if len(hl) == 0 || !strings.HasSuffix(strings.TrimSpace(hl[0]), "a") {
		t.Fatalf("task a's row should be the first highlighted: %q", hl)
	}
}

// planOutputs is Outputs reading a PLAN.md far longer than the pane.
func planOutputs(t *testing.T) tuiModel {
	t.Helper()
	m := withOutputs(t)
	var b strings.Builder
	for i := range 80 {
		fmt.Fprintf(&b, "plan line %02d\n\n", i)
	}
	must(t, os.WriteFile(filepath.Join(m.snap.CWD, "PLAN.md"), []byte(b.String()), 0o644))
	m = press(m, "o")
	if !m.outputsView {
		t.Fatal("setup: Outputs is not open")
	}
	return m
}

// G parks outputScroll far past the end (1<<20); the pane draws its last
// page. A drag past the top must move it from the line drawn, which is
// what applying Result.Offset does: adding Result.Scroll to the parked
// value leaves it past the end, and nothing moves.
func TestDragPastTheTopOfAnOutputAfterGScrollsUp(t *testing.T) {
	captureCopies(t)
	m := press(planOutputs(t), "G")
	x, y := cellOf(t, m.View(), "plan line 79")
	_, top := cellOf(t, m.View(), "plan line")
	before := strings.TrimSpace(screenRow(m, top))
	m = lpress(m, x, y)
	m = lmotion(m, x, 0)
	if after := strings.TrimSpace(screenRow(m, top)); after == before {
		t.Fatalf("a drag past the top after G did not scroll the output up: still %q", after)
	}
}

// A drag resting past the bottom keeps scrolling on AutoScrollMsg ticks:
// the terminal sends no motion for a pointer that does not move.
func TestADragRestingPastTheBottomKeepsScrolling(t *testing.T) {
	captureCopies(t)
	m := resultModel(t)
	x0, y0 := cellOf(t, m.View(), "answer line 01")
	m = lpress(m, x0, y0)
	next, cmd := m.Update(mouseMsg(m.width-1, m.height+2, tea.MouseButtonLeft, tea.MouseActionMotion))
	m = next.(tuiModel)
	// Where the first answer line on screen is drawn: a scroll by one line
	// moves it a row up, or swaps it for the next.
	topLine := func(m tuiModel) string {
		x, y := cellOf(t, m.View(), "answer line")
		return fmt.Sprintf("%q at %d,%d", strings.TrimSpace(screenRow(m, y)), x, y)
	}
	before := topLine(m)
	var tick tea.Msg
	for _, msg := range tuitest.Drain(cmd, 500*time.Millisecond) {
		if _, ok := msg.(selection.AutoScrollMsg); ok {
			tick = msg
		}
	}
	if tick == nil {
		t.Fatal("a drag past the bottom should schedule an auto-scroll tick")
	}
	next, cmd = m.Update(tick)
	m = next.(tuiModel)
	if after := topLine(m); after == before {
		t.Fatalf("the tick did not scroll the result: still %s", after)
	}
	if cmd == nil {
		t.Fatal("while the drag rests past the edge the tick schedules the next")
	}
}

func TestNoColorKeepsTheSelectionHighlight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	captureCopies(t)
	m := resultModel(t)
	x0, y0 := cellOf(t, m.View(), "answer line 01")
	m = dragFrom(m, x0, y0, x0+10, y0)
	if hl := highlighted(m); len(hl) != 1 || !strings.HasPrefix(hl[0], "answer line") {
		t.Fatalf("under NO_COLOR the selection should still be drawn in reverse: %q", hl)
	}
}
