package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/store"
)

// reopenController records which runs were put back in front of the
// dispatcher.
type reopenController struct {
	cancelController
	revived []string
}

func (c *reopenController) LauncherFor(runID, cwd string) *launcher.Launcher {
	c.revived = append(c.revived, runID)
	return c.l
}

func sendAs(t *testing.T, addr string, req map[string]any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(req)
	resp, err := http.Post(addr+"/api/v1/control/send", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func doneRun(t *testing.T, b *broker.Broker, id string) *broker.Run {
	t.Helper()
	run := b.CreateRun(id, "p", t.TempDir(), "main")
	run.CreateStandardThreads()
	for _, tid := range []string{"schema", "api"} {
		task := run.AddTask(tid, tid, "do "+tid, nil)
		task.StartDispatch("d1", "worker-"+tid)
		task.CompleteDispatch(tid+" done", run)
	}
	run.SetState(broker.RunDone)
	return run
}

func startWith(t *testing.T, b *broker.Broker, c Controller) string {
	t.Helper()
	srv := New(b, broker.NewAgentRegistry())
	srv.SetController(c)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return addr
}

func TestAnInstructionToAFinishedTaskReopensIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := broker.New()
	run := doneRun(t, b, "r-done")
	c := &reopenController{cancelController: cancelController{l: launcher.New("http://x", "r-done", t.TempDir())}}
	addr := startWith(t, b, c)

	code, out := sendAs(t, addr, map[string]any{"run_id": "r-done", "content": "@schema also cover the legacy payload"})
	if code != http.StatusOK {
		t.Fatalf("send returned %d: %v", code, out)
	}
	if got, _ := json.Marshal(out["reopened"]); string(got) != `["schema"]` {
		t.Errorf("reopened = %s, want [schema]", got)
	}
	schema := run.GetTask("schema")
	if schema.CurrentState() != broker.TaskReady || !strings.Contains(schema.CurrentFollowUp(), "legacy payload") {
		t.Errorf("schema is %s with follow-up %q", schema.CurrentState(), schema.CurrentFollowUp())
	}
	if run.GetTask("api").CurrentState() != broker.TaskCompleted {
		t.Error("a task the instruction did not name was reopened")
	}
	if run.CurrentState() != broker.RunActive || len(c.revived) == 0 {
		t.Errorf("run is %s, revived %v: it is not back in front of the dispatcher", run.CurrentState(), c.revived)
	}
	// The worker's watcher wakes on its id, whichever way it was named.
	var mentioned bool
	for _, m := range run.MessagesSince(0) {
		if m.Sender == "main" {
			for _, id := range m.Mentions {
				mentioned = mentioned || id == "worker-schema"
			}
		}
	}
	if !mentioned {
		t.Error("the instruction does not mention worker-schema")
	}
}

func TestAFinishedRunOnDiskIsReopenedByTheDaemonRunningNow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := broker.New()
	run := doneRun(t, old, "r-disk")
	if err := store.SaveRun(run.Snapshot()); err != nil {
		t.Fatal(err)
	}
	b := broker.New() // a later daemon, which never hosted r-disk
	c := &reopenController{cancelController: cancelController{l: launcher.New("http://x", "r-disk", t.TempDir())}}
	addr := startWith(t, b, c)

	if code, out := sendAs(t, addr, map[string]any{"run_id": "r-disk", "content": "tighten the summary"}); code != http.StatusConflict {
		t.Errorf("an unaddressed instruction to a finished run: %d %v, want 409 with the reason", code, out)
	}
	if b.GetRun("r-disk") != nil {
		t.Error("an instruction that reopened nothing still loaded the run")
	}
	code, out := sendAs(t, addr, map[string]any{"run_id": "r-disk", "content": "tighten the summary", "mentions": []string{"worker-api"}})
	if code != http.StatusOK {
		t.Fatalf("send returned %d: %v", code, out)
	}
	got := b.GetRun("r-disk")
	if got == nil || got.GetTask("api").CurrentState() != broker.TaskReady {
		t.Fatal("the run on disk was not restored with api reopened")
	}
}

func TestAnInstructionToARunningTaskOnlyGoesOnTheRadio(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := broker.New()
	run := b.CreateRun("r-live", "p", t.TempDir(), "main")
	run.CreateStandardThreads()
	task := run.AddTask("api", "api", "x", nil)
	task.StartDispatch("d1", "worker-api")
	addr := startWith(t, b, &reopenController{cancelController: cancelController{l: launcher.New("http://x", "r-live", t.TempDir())}})

	_, out := sendAs(t, addr, map[string]any{"run_id": "r-live", "content": "@api log less"})
	if got, _ := json.Marshal(out["reopened"]); string(got) != `[]` {
		t.Errorf("reopened = %s, want none", got)
	}
	if task.CurrentState() != broker.TaskDispatched || task.CurrentFollowUp() != "" {
		t.Error("a running task was reopened")
	}
}
