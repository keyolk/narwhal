package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// attachModel is a model whose run has one dispatched task, with HOME
// pointed at a temp dir so the session lookup does not read the developer's
// real ~/.narwhal.
func attachModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.snap.RunID = "run-attach"
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskDispatched, Dispatches: 1},
	}
	m.focus = focusTasks
	return m
}

// writeSessionID puts a recorded session id where the launcher would.
func writeSessionID(t *testing.T, m tuiModel, taskID, sid string) {
	t.Helper()
	path := m.sessionIDPath(taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAttachNeedsARecordedSession(t *testing.T) {
	// A run from before session pinning has no id on disk. Attaching must
	// report that rather than launching claude with an empty --resume,
	// which would silently open the wrong conversation.
	m := attachModel(t)
	if cmd := m.attachToSession("alpha"); cmd != nil {
		t.Fatal("attach produced a command with no recorded session id")
	}
}

func TestAttachUsesTheRecordedSession(t *testing.T) {
	m := attachModel(t)
	writeSessionID(t, m, "alpha", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")

	if got := m.workerSessionID("alpha"); got != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("workerSessionID = %q", got)
	}
	if cmd := m.attachToSession("alpha"); cmd == nil {
		t.Fatal("attach produced no command despite a recorded session")
	}
}

func TestAttachKeyReportsAMissingSession(t *testing.T) {
	// Pressing a with nothing to attach to must say so. Silence reads as
	// "attached and nothing happened", which is the worse failure.
	m := attachModel(t)
	m = press(m, "a")
	if m.err == nil {
		t.Fatal("a with no recorded session left err nil")
	}
	if !strings.Contains(m.err.Error(), "alpha") {
		t.Errorf("error does not name the task: %v", m.err)
	}
}

func TestRunningWorkerWithNoEventsSaysSo(t *testing.T) {
	// A worker that has started but not yet written its first transcript
	// entry must not read as "nothing here" — the pane is empty for a
	// reason and the reason is temporary.
	m := attachModel(t)
	m.width, m.height = 100, 30
	m.detail = detailSession

	out := m.viewSessionDetail()
	if strings.Contains(out, "no transcript found") {
		t.Errorf("a running worker was reported as having no transcript:\n%s", out)
	}
	if !strings.Contains(out, "has not written its first event") {
		t.Errorf("the empty pane does not explain itself:\n%s", out)
	}
}

func TestUndispatchedTaskStillSaysSo(t *testing.T) {
	m := attachModel(t)
	m.width, m.height = 100, 30
	m.snap.Tasks[0] = broker.TaskSnapshot{ID: "alpha", State: broker.TaskPending}
	m.detail = detailSession

	out := m.viewSessionDetail()
	if !strings.Contains(out, "not dispatched yet") {
		t.Errorf("a pending task should say it has not started:\n%s", out)
	}
}
