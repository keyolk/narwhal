package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

func testModel(tasks int, msgs int) tuiModel {
	runs := []store.LiveRun{{RunID: "r1", BrokerURL: "http://x"}}
	m := newTUIModel(runs, 0, time.Second, false)
	snap := broker.Snapshot{RunID: "r1", State: broker.RunActive}
	for i := 0; i < tasks; i++ {
		snap.Tasks = append(snap.Tasks, broker.TaskSnapshot{
			ID:    string(rune('a' + i)),
			State: broker.TaskReady,
		})
	}
	for i := 0; i < msgs; i++ {
		snap.Messages = append(snap.Messages, &broker.Message{
			Seq:     int64(i + 1),
			Sender:  "worker-x",
			Content: "message body",
		})
	}
	m.snap = snap
	return m
}

func press(m tuiModel, keys ...string) tuiModel {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.Update(msg)
		m = next.(tuiModel)
	}
	return m
}

func TestTabSwitchesFocus(t *testing.T) {
	m := testModel(3, 3)
	if m.focus != focusRadio {
		t.Fatalf("initial focus = %v, want radio", m.focus)
	}
	m = press(m, "tab")
	if m.focus != focusTasks {
		t.Fatalf("after tab focus = %v, want tasks", m.focus)
	}
	m = press(m, "tab")
	if m.focus != focusRadio {
		t.Fatalf("after second tab focus = %v, want radio", m.focus)
	}
}

func TestCursorMovesWithinFocusedPane(t *testing.T) {
	m := testModel(4, 4)
	m.radioCur = 0
	m.followTail = false

	m = press(m, "j", "j")
	if m.radioCur != 2 {
		t.Fatalf("radioCur = %d, want 2", m.radioCur)
	}
	if m.taskCur != 0 {
		t.Fatalf("task cursor moved while radio was focused: %d", m.taskCur)
	}

	// In the graph the cursor moves by geometry, so which task j lands on
	// depends on the drawn layout — what this test pins is that the focused
	// pane is the one that moves. These four tasks have no deps and share a
	// row, so l is the direction with somewhere to go.
	m = press(m, "tab", "l")
	if m.taskCur == 0 {
		t.Fatalf("task cursor did not move while tasks were focused")
	}
	if m.radioCur != 2 {
		t.Fatalf("radio cursor moved while tasks were focused: %d", m.radioCur)
	}
}

func TestCursorClampsAtBounds(t *testing.T) {
	m := testModel(3, 3)
	m.followTail = false

	m = press(m, "k", "k", "k", "k")
	if m.radioCur != 0 {
		t.Fatalf("radioCur = %d, want clamped to 0", m.radioCur)
	}
	m = press(m, "j", "j", "j", "j", "j")
	if m.radioCur != 2 {
		t.Fatalf("radioCur = %d, want clamped to 2", m.radioCur)
	}
}

func TestManualNavigationReleasesTailFollowing(t *testing.T) {
	// Reading an older message must not be yanked away by the next poll.
	m := testModel(2, 5)
	if !m.followTail {
		t.Fatal("expected followTail on by default")
	}
	m = press(m, "k")
	if m.followTail {
		t.Fatal("manual navigation should release tail following")
	}

	// A new poll must not move the cursor while following is off.
	before := m.radioCur
	snap := m.snap
	snap.Messages = append(snap.Messages, &broker.Message{Seq: 6, Sender: "w", Content: "new"})
	next, _ := m.Update(snapshotMsg{snap: snap})
	m = next.(tuiModel)
	if m.radioCur != before {
		t.Fatalf("cursor moved on poll while not following: %d → %d", before, m.radioCur)
	}

	// f re-arms following and jumps to the newest.
	m = press(m, "f")
	if !m.followTail {
		t.Fatal("f should re-arm tail following")
	}
	if m.radioCur != len(m.snap.Messages)-1 {
		t.Fatalf("f should jump to newest: cur=%d last=%d", m.radioCur, len(m.snap.Messages)-1)
	}
}

func TestTailFollowingTracksNewMessages(t *testing.T) {
	m := testModel(2, 3)
	snap := m.snap
	snap.Messages = append(snap.Messages, &broker.Message{Seq: 4, Sender: "w", Content: "newest"})
	next, _ := m.Update(snapshotMsg{snap: snap})
	m = next.(tuiModel)

	if m.radioCur != 3 {
		t.Fatalf("following cursor = %d, want 3 (newest)", m.radioCur)
	}
}

func TestEnterOpensDetailAndEscCloses(t *testing.T) {
	m := testModel(2, 3)
	m = press(m, "enter")
	if m.detail != detailMessage {
		t.Fatal("enter should open the message detail view")
	}
	m = press(m, "esc")
	if m.detail != detailClosed {
		t.Fatal("esc should close the detail view")
	}
}

func TestEnterOnTasksPaneOpensTaskDetail(t *testing.T) {
	m := testModel(2, 3)
	m = press(m, "tab", "enter")
	if m.detail != detailTask {
		t.Fatalf("enter on the tasks pane should open the task detail, got %v", m.detail)
	}
}

func TestDetailNavigationBetweenMessages(t *testing.T) {
	m := testModel(2, 3)
	m.radioCur = 0
	m.followTail = false
	m = press(m, "enter")

	m = press(m, "n")
	if m.radioCur != 1 {
		t.Fatalf("n should advance to the next message, got %d", m.radioCur)
	}
	if m.detailScroll != 0 {
		t.Fatalf("scroll should reset on message change, got %d", m.detailScroll)
	}
	m = press(m, "p")
	if m.radioCur != 0 {
		t.Fatalf("p should go back, got %d", m.radioCur)
	}
}

func TestDetailNavigationBetweenTasks(t *testing.T) {
	m := testModel(3, 1)
	m = press(m, "tab", "enter")

	m = press(m, "n")
	if m.taskCur != 1 {
		t.Fatalf("n should advance to the next task, got %d", m.taskCur)
	}
	m = press(m, "p")
	if m.taskCur != 0 {
		t.Fatalf("p should go back, got %d", m.taskCur)
	}
	// Walking tasks must not disturb the radio cursor.
	if m.radioCur != 0 {
		t.Fatalf("radio cursor moved during task navigation: %d", m.radioCur)
	}
}

func TestDetailScrollDoesNotGoNegative(t *testing.T) {
	m := testModel(2, 2)
	m = press(m, "enter", "k", "k")
	if m.detailScroll != 0 {
		t.Fatalf("detailScroll = %d, want 0", m.detailScroll)
	}
}

func TestQuitSetsQuitFlag(t *testing.T) {
	m := testModel(1, 1)
	m = press(m, "q")
	if !m.quit {
		t.Fatal("q should set the quit flag")
	}
}

func TestEmptyRunDoesNotPanic(t *testing.T) {
	m := testModel(0, 0)
	m.width, m.height = 80, 24
	m = press(m, "j", "k", "enter", "tab", "g", "G")
	_ = m.View()
}

func TestSortedTasksIsStable(t *testing.T) {
	// The broker stores tasks in a map, so render order must be imposed
	// here or the cursor jumps between polls.
	m := testModel(0, 0)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "c"}, {ID: "a"}, {ID: "b"},
	}
	got := m.sortedTasks()
	if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("sortedTasks = %v, want a,b,c", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestScrollStartKeepsCursorVisible(t *testing.T) {
	cases := []struct {
		cur, visible, total, want int
	}{
		{cur: 0, visible: 5, total: 20, want: 0},
		{cur: 19, visible: 5, total: 20, want: 15},
		{cur: 10, visible: 5, total: 20, want: 8},
		{cur: 3, visible: 10, total: 5, want: 0},
	}
	for _, c := range cases {
		if got := scrollStart(c.cur, c.visible, c.total); got != c.want {
			t.Fatalf("scrollStart(%d,%d,%d) = %d, want %d",
				c.cur, c.visible, c.total, got, c.want)
		}
	}
}

func TestWrapTextPreservesNewlinesAndWraps(t *testing.T) {
	lines := wrapText("short\n\nthis is a much longer line that needs wrapping", 20)
	if lines[0] != "short" {
		t.Fatalf("first line = %q", lines[0])
	}
	if lines[1] != "" {
		t.Fatalf("blank line not preserved: %q", lines[1])
	}
	for _, l := range lines {
		if len(l) > 20 {
			t.Fatalf("line exceeds width: %q", l)
		}
	}
}

func TestViewRendersRunAndMessages(t *testing.T) {
	m := testModel(2, 2)
	m.width, m.height = 100, 24
	out := m.View()
	if !strings.Contains(out, "r1") {
		t.Fatalf("view missing run id:\n%s", out)
	}
	if !strings.Contains(out, "Radio") {
		t.Fatalf("view missing radio panel:\n%s", out)
	}
	if !strings.Contains(out, "Graph") {
		t.Fatalf("view missing graph panel:\n%s", out)
	}
}

func TestDetailViewShowsFullContent(t *testing.T) {
	// The whole point of the detail view: content the list truncates.
	m := testModel(1, 1)
	m.width, m.height = 80, 24
	long := strings.Repeat("detail content that the list would truncate. ", 5)
	m.snap.Messages[0].Content = long
	m.snap.Messages[0].Priority = broker.PriorityUrgent
	m = press(m, "enter")

	out := m.View()
	if !strings.Contains(out, "detail content") {
		t.Fatalf("detail view missing content:\n%s", out)
	}
	if !strings.Contains(out, "urgent") {
		t.Fatalf("detail view missing priority:\n%s", out)
	}
}

func TestSplitRequestIsLegibleInList(t *testing.T) {
	// A split request used to be tagged "[split]" with the wire format
	// after it. It now reads as a sentence, but the requirement is the
	// same: the row must say a task was created, and the id must be in it.
	m := testModel(1, 1)
	m.width, m.height = 100, 24
	m.snap.Messages[0].Content = broker.FormatSplitRequest("t9", "extra", "do extra", nil)
	row := m.radioRow(m.snap.Messages[0], 100, false)
	if !strings.Contains(row, "new task") {
		t.Fatalf("split request is not identifiable: %s", row)
	}
	if !strings.Contains(row, "t9") {
		t.Fatalf("split request does not name the new task: %s", row)
	}
	if strings.Contains(row, "SPLIT_REQUEST") {
		t.Fatalf("the wire format leaked into the row: %s", row)
	}
}

func TestTaskDetailShowsAssignmentAndEdges(t *testing.T) {
	m := testModel(0, 0)
	m.width, m.height = 80, 24
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "root", State: broker.TaskCompleted, Assignment: "investigate the launcher path"},
		{ID: "leaf", State: broker.TaskPending, Deps: []string{"root"}, Assignment: "synthesize"},
	}
	m.focus = focusTasks
	m.taskCur = 0
	m = press(m, "enter")

	out := m.View()
	if !strings.Contains(out, "investigate the launcher path") {
		t.Fatalf("task detail missing assignment:\n%s", out)
	}
	if !strings.Contains(out, "blocks leaf") {
		t.Fatalf("task detail should list downstream tasks:\n%s", out)
	}
}

func TestGraphPaneDrawsEdges(t *testing.T) {
	m := testModel(0, 0)
	m.width, m.height = 90, 20
	m.boxMode = false // lane gutter
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "root", State: broker.TaskCompleted},
		{ID: "child", State: broker.TaskReady, Deps: []string{"root"}},
	}
	out := m.viewTasks(40, 10)
	// child inherits root's lane, so the edge is the column itself.
	if !strings.Contains(out, glyphNodeOnLine) {
		t.Fatalf("lane view should show the dependency edge:\n%s", out)
	}
}

func TestBoxModeDrawsBoxesAndConnectors(t *testing.T) {
	m := testModel(0, 0)
	m.width, m.height = 90, 24
	m.boxMode = true
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "root", State: broker.TaskCompleted},
		{ID: "child", State: broker.TaskReady, Deps: []string{"root"}},
	}
	out := m.viewTasks(44, 14)

	if !strings.Contains(out, boxTopLeft) || !strings.Contains(out, boxBottomRight) {
		t.Fatalf("box mode should draw box borders:\n%s", out)
	}
	// A dependent's top border carries a tee where the edge lands.
	if !strings.Contains(out, jTeeUp) {
		t.Fatalf("dependent box should show an incoming tee:\n%s", out)
	}
	if !strings.Contains(out, boxVert) {
		t.Fatalf("box mode should draw a connector between boxes:\n%s", out)
	}
}

func TestBoxModeToggle(t *testing.T) {
	m := testModel(2, 1)
	if !m.boxMode {
		t.Fatal("box mode should be the default")
	}
	m = press(m, "b")
	if m.boxMode {
		t.Fatal("b should toggle box mode off")
	}
	m = press(m, "b")
	if !m.boxMode {
		t.Fatal("b should toggle box mode back on")
	}
}

// multiRunModel builds a model watching several live runs.
func multiRunModel(n int) tuiModel {
	runs := make([]store.LiveRun, n)
	for i := range runs {
		runs[i] = store.LiveRun{
			RunID:     string(rune('a'+i)) + "-run",
			BrokerURL: "http://x",
			Prompt:    "prompt " + string(rune('a'+i)),
			// Descending, so the newest-first ordering the picker applies
			// leaves them in a-run, b-run, c-run order and the tests can
			// talk about positions.
			StartedAt: int64(1000 - i),
		}
	}
	m := newTUIModel(runs, 0, time.Second, false)
	m.width, m.height = 100, 24
	m.snap = broker.Snapshot{RunID: runs[0].RunID, State: broker.RunActive}
	return m
}

func TestBracketKeysSwitchRuns(t *testing.T) {
	// An interactive session creates a run per request, so moving between
	// them has to be cheap.
	m := multiRunModel(3)
	if m.live.RunID != "a-run" {
		t.Fatalf("initial run = %q, want a-run", m.live.RunID)
	}
	m = press(m, "]")
	if m.live.RunID != "b-run" {
		t.Fatalf("] should advance to b-run, got %q", m.live.RunID)
	}
	m = press(m, "[")
	if m.live.RunID != "a-run" {
		t.Fatalf("[ should go back to a-run, got %q", m.live.RunID)
	}
}

func TestRunSwitchClearsPreviousRunState(t *testing.T) {
	// Cursors and messages belong to a run; carrying them across would
	// point at rows that do not exist in the new one.
	m := multiRunModel(2)
	m.snap.Messages = []*broker.Message{
		{Seq: 1, Sender: "w", Content: "old"},
		{Seq: 2, Sender: "w", Content: "older"},
	}
	m.radioCur = 1
	m.taskCur = 3
	m.followTail = false

	m = press(m, "]")

	if len(m.snap.Messages) != 0 {
		t.Fatalf("messages from the previous run survived: %d", len(m.snap.Messages))
	}
	if m.radioCur != 0 || m.taskCur != 0 {
		t.Fatalf("cursors not reset: radio=%d task=%d", m.radioCur, m.taskCur)
	}
	if m.detail != detailClosed {
		t.Fatal("detail view should be closed on the new run")
	}
	if !m.followTail {
		t.Fatal("tail following should re-arm for the new run")
	}
}

func TestDetailViewCapturesKeysBeforeRunSwitch(t *testing.T) {
	// While a message is open, ] belongs to the detail view's own
	// navigation, not to run switching — otherwise reading a message
	// could yank the whole view to another run.
	m := multiRunModel(2)
	m.snap.Messages = []*broker.Message{{Seq: 1, Sender: "w", Content: "body"}}
	m = press(m, "enter")
	if m.detail != detailMessage {
		t.Fatal("expected the detail view to open")
	}
	m = press(m, "]")
	if m.live.RunID != "a-run" {
		t.Fatalf("] inside the detail view should not switch runs, got %q", m.live.RunID)
	}
}

func TestSwitchStopsAtEnds(t *testing.T) {
	m := multiRunModel(2)
	m = press(m, "[") // already at the first
	if m.live.RunID != "a-run" {
		t.Fatalf("[ at the start should stay put, got %q", m.live.RunID)
	}
	m = press(m, "]", "]") // past the last
	if m.live.RunID != "b-run" {
		t.Fatalf("] at the end should stay put, got %q", m.live.RunID)
	}
}

func TestPickerOpensAndSelects(t *testing.T) {
	m := multiRunModel(3)
	m = press(m, "r")
	if !m.picker {
		t.Fatal("r should open the run picker")
	}
	m = press(m, "j", "j")
	if m.runCur != 2 {
		t.Fatalf("picker cursor = %d, want 2", m.runCur)
	}
	m = press(m, "enter")
	if m.picker {
		t.Fatal("enter should leave the picker")
	}
	if m.live.RunID != "c-run" {
		t.Fatalf("selected run = %q, want c-run", m.live.RunID)
	}
}

func TestEscBacksOutToTheRunList(t *testing.T) {
	// esc pairs with enter: enter digs into a run, esc backs out. It works
	// with a single run too — a key that silently does nothing is worse
	// than backing out to a one-line list.
	m := testModel(1, 1)
	m = press(m, "esc")
	if !m.picker {
		t.Fatal("esc should back out to the run list")
	}
}

func TestEscFromTheRunListQuits(t *testing.T) {
	// From the top level there is nowhere further back to go.
	m := multiRunModel(2)
	m.picker = true
	m = press(m, "esc")
	if !m.quit {
		t.Fatal("esc at the run list should quit")
	}
}

func TestPickerViewIdentifiesRunsByPromptNotID(t *testing.T) {
	// Regression: every row read "(no prompt)" and the run ids are
	// timestamps, so six live runs were indistinguishable. What tells runs
	// apart is when they started, where they run, and what they were asked.
	m := multiRunModel(2)
	m.runs[0].CWD = "/Users/x/src/myrepo"
	m.runs[0].Prompt = "audit the auth module"
	m.picker = true

	out := m.View()
	for _, want := range []string{"audit the auth module", "prompt b", "2 live runs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("picker view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "myrepo") {
		t.Fatalf("picker view does not show the working directory:\n%s", out)
	}
}

func TestHeaderShowsRunPosition(t *testing.T) {
	m := multiRunModel(3)
	if !strings.Contains(m.View(), "[1/3]") {
		t.Fatalf("header should show which run is open:\n%s", m.View())
	}
	// A single run needs no counter.
	single := testModel(1, 1)
	single.width, single.height = 100, 24
	if strings.Contains(single.View(), "[1/1]") {
		t.Fatal("a lone run should not show a position counter")
	}
}

func TestMergeRunsKeepsWatchedRun(t *testing.T) {
	// Runs come and go while the monitor is open; the selection must
	// follow the run being watched, not its index.
	m := multiRunModel(3)
	m.runCur = 2
	m.live = m.runs[2]

	// A newer run appears at the front, shifting every index.
	refreshed := append([]store.LiveRun{
		{RunID: "z-run", BrokerURL: "http://x", StartedAt: 2000},
	}, m.runs...)
	m.mergeRuns(refreshed)

	if m.runs[m.runCur].RunID != "c-run" {
		t.Fatalf("cursor drifted to %q after the list shifted", m.runs[m.runCur].RunID)
	}
}

func TestMergeRunsHandlesWatchedRunEnding(t *testing.T) {
	m := multiRunModel(3)
	m.runCur = 2
	m.live = m.runs[2]

	m.mergeRuns(m.runs[:1]) // the watched run finished and dropped out

	if m.runCur < 0 || m.runCur >= len(m.runs) {
		t.Fatalf("cursor out of range after the watched run ended: %d", m.runCur)
	}
}

func TestGraphPaneDrawsFanIn(t *testing.T) {
	m := testModel(0, 0)
	m.width, m.height = 90, 20
	m.boxMode = false // lane gutter
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted},
		{ID: "b", State: broker.TaskCompleted},
		{ID: "sink", State: broker.TaskReady, Deps: []string{"a", "b"}},
	}
	out := m.viewTasks(40, 10)
	if !strings.Contains(out, glyphElbow) && !strings.Contains(out, glyphTee) {
		t.Fatalf("fan-in should turn the incoming lane toward the node:\n%s", out)
	}
}

func TestTruncateIsANSIFree(t *testing.T) {
	// truncate operates on plain text; feeding it styled input was the
	// original performance bug. Confirm the plain-text contract.
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("truncate ascii = %q, want abc", got)
	}
	if got := truncate("한글테스트", 4); displayWidth(got) > 4 {
		t.Fatalf("truncate wide runes overflowed: %q width=%d", got, displayWidth(got))
	}
	if got := truncate("short", 99); got != "short" {
		t.Fatalf("truncate should pass through short strings, got %q", got)
	}
}
