package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

func viewText(m tuiModel) string { return ansi.Strip(m.View()) }

// A task's view is its whole story: the assignment, the instruction the
// operator sent it, the judge's call, and the worker's work between them,
// in the order it happened.
func TestTheTaskViewTellsTheTaskInOrder(t *testing.T) {
	m := press(fixtureRun(t), "1")
	m.taskCur = indexOf(t, m, "api")
	m = press(m, "enter")
	got := viewText(m)
	order := []string{
		"▶ task: Validate LockKey",
		"Reading the router first",
		"→ Read  api/router.go",
		"» you: make sure nothing logs the lock token",
		"The request logger prints headers verbatim",
		"asked the judge: does middleware validation cover the replay path too? → 81%",
	}
	at := 0
	for _, w := range order {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("%q missing, or out of order, after offset %d:\n%s", w, at, got)
		}
		at += i + len(w)
	}
}

// An instruction appears only in the feeds of the tasks it went to.
func TestAnInstructionShowsOnlyWhereItWent(t *testing.T) {
	m := press(fixtureRun(t), "1")
	m.taskCur = indexOf(t, m, "schema")
	if got := viewText(press(m, "enter")); strings.Contains(got, "nothing logs the lock token") {
		t.Errorf("schema's view shows an instruction routed to api:\n%s", got)
	}
}

// The judge's call on a retried task heads its second attempt.
func TestARetriedTaskShowsTheJudgesCall(t *testing.T) {
	m := press(fixtureRun(t), "1")
	m.taskCur = indexOf(t, m, "client")
	got := viewText(press(m, "enter"))
	for _, w := range []string{"judge: no-result 0.86 → retry", "try 2 of 3", "── final answer ──"} {
		if !strings.Contains(got, w) {
			t.Errorf("client's view lacks %q:\n%s", w, got)
		}
	}
}

// enter on a timeline event opens its task at that moment, not at the end.
func TestATimelineEventOpensItsTaskThere(t *testing.T) {
	m := press(fixtureRun(t), "3")
	events := m.timelineEvents()
	want := -1
	for i, e := range events {
		if e.Kind == broker.TimelineStarted && e.Who == "api" {
			want = i
		}
	}
	if want < 0 {
		t.Fatal("setup: no start event for api")
	}
	m.timelineSel = want
	m = press(m, "enter")
	if m.detail != detailSession || m.sessionTail {
		t.Fatalf("detail=%v following=%v, want the task view held at a moment", m.detail, m.sessionTail)
	}
	if s, ok := m.selectedTask(); !ok || s.ID != "api" {
		t.Fatalf("opened %q, want api", s.ID)
	}
	if got := viewText(m); !strings.Contains(got, "▶ task: Validate LockKey") {
		t.Errorf("the view did not open at api's start:\n%s", got)
	}
}

// enter on an event lifted from the radio opens the message itself; enter
// there goes on to the task it concerns, at the time it was posted.
func TestARadioEventOpensTheMessageAndThenItsTask(t *testing.T) {
	m := fixtureRun(t)
	m = press(m, "3", "3") // zoomed: the timeline lists the radio's lines too
	m.zoom = focusTimeline
	events := m.timelineEvents()
	for i, e := range events {
		if e.Kind == broker.TimelineUrgent {
			m.timelineSel = i
		}
	}
	m = press(m, "enter")
	if m.detail != detailMessage {
		t.Fatalf("detail=%v, want the message", m.detail)
	}
	if got := viewText(m); !strings.Contains(got, "the same nonce passes twice") ||
		!strings.Contains(got, "enter api at this moment") {
		t.Fatalf("message view:\n%s", got)
	}
	m = press(m, "enter")
	if s, _ := m.selectedTask(); m.detail != detailSession || s.ID != "api" || m.sessionTail {
		t.Fatalf("enter on the message: detail=%v task=%q following=%v", m.detail, s.ID, m.sessionTail)
	}
	// And esc goes back to where you were.
	if m = press(m, "esc"); m.detail != detailClosed {
		t.Errorf("esc left detail=%v", m.detail)
	}
}

// The operator's own message says where it went and who answered.
func TestAnInstructionSaysWhereItWentAndWhoAnswered(t *testing.T) {
	m := fixtureRun(t)
	for i, msg := range m.snap.Messages {
		if msg.Sender == "main" {
			m.radioCur = i
		}
	}
	got := viewText(press(m, "1", "4", "enter"))
	for _, w := range []string{"you →", "went to → api", "✓ answered by api"} {
		if !strings.Contains(got, w) {
			t.Errorf("instruction view lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "thread=") || strings.Contains(got, "priority=") {
		t.Errorf("empty labels shown:\n%s", got)
	}
}

// A watcher's wake-up reads as one quiet line with its summary, not the
// notification's markup.
func TestAWatcherNotificationIsOneQuietLine(t *testing.T) {
	var st feedState
	renderEntry(transcriptEntry{kind: "text", user: true, text: "the assignment"}, 80, &st)
	got := ansi.Strip(strings.Join(renderEntry(transcriptEntry{kind: "text", user: true,
		text: "<task-notification><task-id>x</task-id><summary>Monitor event: message 7</summary></task-notification>"}, 80, &st), "\n"))
	if !strings.Contains(got, "⚑ Monitor event: message 7") || strings.Contains(got, "task-id") {
		t.Errorf("notification rendered as %q", got)
	}
}

// ? shows the reference over everything; any key closes it.
func TestHelpOpensAndCloses(t *testing.T) {
	m := press(fixtureRun(t), "?")
	if got := viewText(m); !strings.Contains(got, "Open what you are looking at") {
		t.Fatalf("help not shown:\n%s", got)
	}
	if m = press(m, "j"); m.help || m.focus != focusRadio {
		t.Errorf("closing help: help=%v focus=%v — the key must only close it", m.help, m.focus)
	}
}

// The footer leads with what enter does in the focused pane.
func TestTheFooterSaysWhatEnterOpens(t *testing.T) {
	m := fixtureRun(t)
	for key, want := range map[string]string{
		"1": "enter open task", "2": "enter open task", "3": "enter open event", "4": "enter open message",
	} {
		m = press(m, "1", key)
		if got := ansi.Strip(m.footerKeys()); !strings.HasPrefix(got, want) || !strings.Contains(got, "? help") {
			t.Errorf("pane %s footer: %q", key, got)
		}
	}
}

func indexOf(t *testing.T, m tuiModel, id string) int {
	t.Helper()
	for i, r := range m.graphRows() {
		if r.id == id {
			return i
		}
	}
	t.Fatalf("no task %q", id)
	return -1
}
