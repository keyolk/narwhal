package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// sessionModel builds a model whose run has one task, with a session log
// written where the launcher would put it.
func sessionModel(t *testing.T, taskID string, logLines []string) tuiModel {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	runs := []store.LiveRun{{RunID: "r1", BrokerURL: "http://x"}}
	m := newTUIModel(runs, 0, time.Second, false)
	m.width, m.height = 100, 24
	m.snap = broker.Snapshot{
		RunID: "r1",
		State: broker.RunActive,
		Tasks: []broker.TaskSnapshot{{
			ID: taskID, State: broker.TaskDispatched, Dispatches: 1, Model: "haiku",
		}},
	}

	if logLines != nil {
		dir := filepath.Join(home, ".narwhal", "sessions", "r1", "agents", "worker-"+taskID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := strings.Join(logLines, "\n")
		if err := os.WriteFile(filepath.Join(dir, "claude-output.txt"), []byte(body), 0o600); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}
	return m
}

func TestSKeyOpensTheSessionView(t *testing.T) {
	m := sessionModel(t, "task-1", []string{"reading screen.c", "found rewrap"})
	m = press(m, "s")
	if m.detail != detailSession {
		t.Fatalf("detail = %v, want detailSession", m.detail)
	}
	out := m.View()
	if !strings.Contains(out, "found rewrap") {
		t.Fatalf("session view missing worker output:\n%s", out)
	}
}

func TestSessionViewShowsAgentAndModel(t *testing.T) {
	// The point of the view is knowing which worker you are watching.
	m := sessionModel(t, "task-1", []string{"line"})
	m = press(m, "s")
	out := m.View()
	for _, want := range []string{"worker-task-1", "haiku"} {
		if !strings.Contains(out, want) {
			t.Errorf("session view missing %q:\n%s", want, out)
		}
	}
}

func TestSessionViewExplainsAnAbsentLog(t *testing.T) {
	// A blank pane cannot be told apart from a broken one.
	m := sessionModel(t, "task-1", nil)
	m.snap.Tasks[0].Dispatches = 0
	m.snap.Tasks[0].State = broker.TaskPending
	m = press(m, "s")
	if out := m.View(); !strings.Contains(out, "not dispatched yet") {
		t.Fatalf("absent log not explained:\n%s", out)
	}
}

func TestSessionViewFollowsNewOutputUntilYouScroll(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i%26))
	}
	m := sessionModel(t, "task-1", lines)

	m = press(m, "s")
	if !m.sessionTail {
		t.Fatal("opening the session view should start following")
	}
	m = press(m, "k")
	if m.sessionTail {
		t.Fatal("scrolling up should release following")
	}
	m = press(m, "f")
	if !m.sessionTail {
		t.Fatal("f should re-arm following")
	}
}

func TestSKeyTogglesBetweenTaskAndSession(t *testing.T) {
	m := sessionModel(t, "task-1", []string{"line"})
	m = press(m, "tab", "enter") // task detail
	if m.detail != detailTask {
		t.Fatalf("expected task detail, got %v", m.detail)
	}
	m = press(m, "s")
	if m.detail != detailSession {
		t.Fatalf("s should switch to the session view, got %v", m.detail)
	}
	m = press(m, "s")
	if m.detail != detailTask {
		t.Fatalf("s should switch back to the task view, got %v", m.detail)
	}
}

func TestSessionViewWalksTasksWithNP(t *testing.T) {
	m := sessionModel(t, "task-1", []string{"line"})
	m.snap.Tasks = append(m.snap.Tasks, broker.TaskSnapshot{
		ID: "task-2", State: broker.TaskReady,
	})
	m = press(m, "s", "n")
	if m.taskCur != 1 {
		t.Fatalf("n in the session view should advance the task cursor, got %d", m.taskCur)
	}
	if m.detail != detailSession {
		t.Fatalf("n should stay in the session view, got %v", m.detail)
	}
}

func TestTaskDetailPointsAtTheSessionView(t *testing.T) {
	m := sessionModel(t, "task-1", []string{"some output"})
	m = press(m, "tab", "enter")
	if out := m.View(); !strings.Contains(out, "press s") {
		t.Fatalf("task detail does not mention the session view:\n%s", out)
	}
}
