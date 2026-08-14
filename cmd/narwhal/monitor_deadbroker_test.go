package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// A run outlives the process serving it. The daemon exits, is restarted, or
// retires a finished run out of its memory, and from that moment the
// monitor is pointed at a port nobody is listening on.
//
// It handled that by showing an error and nothing else: the snapshot stayed
// empty, so there were no tasks on screen, so there was nothing to select,
// so pressing a did nothing whatsoever — which is what "attach is broken"
// looked like. The record was on disk the whole time.

// deadBrokerModel watches a run whose broker has gone away, with the run's
// snapshot persisted under a temporary HOME.
func deadBrokerModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	snap := broker.Snapshot{
		RunID:  "r-dead",
		Prompt: "audit the auth module",
		State:  broker.RunDone,
		Tasks: []broker.TaskSnapshot{
			{ID: "task-1", State: broker.TaskCompleted, Dispatches: 1},
		},
	}
	if err := store.SaveRun(snap); err != nil {
		t.Fatal(err)
	}

	// Port 1 has nothing on it, and a short timeout keeps the test from
	// waiting out a connect.
	runs := []store.LiveRun{{RunID: "r-dead", BrokerURL: "http://127.0.0.1:1"}}
	m := newTUIModel(runs, 0, time.Second, false)
	m.client = &http.Client{Timeout: 200 * time.Millisecond}
	m.width, m.height = 120, 30
	m.focus = focusTasks
	return m
}

func TestAnUnreachableBrokerFallsBackToDisk(t *testing.T) {
	m := deadBrokerModel(t)

	msg, ok := m.poll()().(snapshotMsg)
	if !ok {
		t.Fatalf("poll returned %T", msg)
	}
	if msg.snap.RunID != "r-dead" {
		t.Fatalf("the saved snapshot was not read back: %+v", msg.snap)
	}
	// The error rides along: the tasks are real but no longer live, and
	// the header is the only thing that says so.
	if msg.err == nil {
		t.Error("a fallback read reported the broker as healthy")
	}
}

func TestAFallbackSnapshotIsApplied(t *testing.T) {
	// Carrying the snapshot back is useless if Update drops it on the
	// error, which is what it did.
	m := deadBrokerModel(t)

	updated, _ := m.Update(m.poll()())
	m = updated.(tuiModel)

	if len(m.snap.Tasks) != 1 {
		t.Fatalf("the fallback snapshot was discarded: %d tasks", len(m.snap.Tasks))
	}
	if m.err == nil {
		t.Error("the unreachable broker is no longer reported")
	}
}

func TestAttachWorksAfterTheBrokerDies(t *testing.T) {
	// The whole point: the session ids are on disk, the transcript is on
	// disk, and the broker being gone must not stand between them.
	m := deadBrokerModel(t)
	updated, _ := m.Update(m.poll()())
	m = updated.(tuiModel)

	writeSessionID(t, m, "task-1", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	m.live.CWD = t.TempDir()

	task, ok := m.selectedTask()
	if !ok {
		t.Fatal("no task to attach to after falling back to disk")
	}
	cmd, err := m.attachToSession(task.ID)
	if err != nil {
		t.Fatalf("attach refused: %v", err)
	}
	if cmd == nil {
		t.Fatal("attach produced no command")
	}
}

func TestSessionPathsSurviveAnEmptySnapshot(t *testing.T) {
	// Before the disk fallback the snapshot could stay zeroed, and the
	// session paths were keyed off it — so they pointed at
	// ~/.narwhal/sessions//agents/..., a directory that cannot exist.
	m := deadBrokerModel(t)
	m.snap = broker.Snapshot{}

	path := m.sessionIDPath("task-1")
	if path == "" {
		t.Fatal("no session path at all")
	}
	if !slices.Contains(strings.Split(path, string(filepath.Separator)), "r-dead") {
		t.Errorf("the session path does not name the run being watched: %s", path)
	}
}

func TestTheWatchedRunTakesItsRefreshedEntry(t *testing.T) {
	// m.live was set once, when the run was opened, and never again. A run
	// whose broker went away kept its dead URL for the life of the monitor:
	// rediscovery knew the run was now served from disk and the model never
	// heard about it.
	m := deadBrokerModel(t)

	m.mergeRuns([]store.LiveRun{
		{RunID: "r-dead", BrokerURL: "", State: string(broker.RunDone), Tasks: 1, Done: 1},
	})

	if m.live.BrokerURL != "" {
		t.Fatalf("the watched run kept its dead broker URL: %q", m.live.BrokerURL)
	}
	if m.live.State != string(broker.RunDone) {
		t.Errorf("the refreshed outcome was not taken: %+v", m.live)
	}
}

func TestSwitchingRunsIsNotUndoneByARefresh(t *testing.T) {
	// Refreshing m.live must match on run id, not position — otherwise a
	// list that reorders would swap the run out from under the viewer.
	m := deadBrokerModel(t)
	m.live = store.LiveRun{RunID: "r-other", BrokerURL: "http://127.0.0.1:2"}

	m.mergeRuns([]store.LiveRun{
		{RunID: "r-dead", BrokerURL: ""},
		{RunID: "r-other", BrokerURL: "http://127.0.0.1:2"},
	})

	if m.live.RunID != "r-other" {
		t.Fatalf("the watched run changed to %s", m.live.RunID)
	}
}

func TestATruncatedResponseFallsBackToo(t *testing.T) {
	// A daemon killed mid-response answers 200 and then stops writing. That
	// is the same event as the connection failing, one tick earlier, and it
	// was the one error path that did not fall back to disk.
	m := deadBrokerModel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshot": {"run_id": "r-de`))
	}))
	defer srv.Close()
	m.live.BrokerURL = srv.URL

	msg, ok := m.poll()().(snapshotMsg)
	if !ok {
		t.Fatalf("poll returned %T", msg)
	}
	if msg.snap.RunID != "r-dead" {
		t.Fatalf("a truncated response did not fall back to disk: %+v", msg.snap)
	}
	if msg.err == nil {
		t.Error("the truncated read was reported as healthy")
	}
}
