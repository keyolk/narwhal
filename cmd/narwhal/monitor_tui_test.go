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
	m := newTUIModel(store.LiveRun{RunID: "r1", BrokerURL: "http://x"}, time.Second)
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

	m = press(m, "tab", "j")
	if m.taskCur != 1 {
		t.Fatalf("taskCur = %d, want 1", m.taskCur)
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
	if !m.detail {
		t.Fatal("enter should open the detail view")
	}
	m = press(m, "esc")
	if m.detail {
		t.Fatal("esc should close the detail view")
	}
}

func TestEnterOnTasksPaneDoesNotOpenDetail(t *testing.T) {
	// The detail view renders a message; opening it from the task pane
	// would show something unrelated to the selection.
	m := testModel(2, 3)
	m = press(m, "tab", "enter")
	if m.detail {
		t.Fatal("enter on the tasks pane should not open the message detail")
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
	if !strings.Contains(out, "Tasks") {
		t.Fatalf("view missing tasks panel:\n%s", out)
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

func TestSplitRequestIsTaggedInList(t *testing.T) {
	m := testModel(1, 1)
	m.width, m.height = 100, 24
	m.snap.Messages[0].Content = broker.FormatSplitRequest("t9", "extra", "do extra", nil)
	if !strings.Contains(m.radioLine(m.snap.Messages[0]), "split") {
		t.Fatalf("split request not tagged: %s", m.radioLine(m.snap.Messages[0]))
	}
}
