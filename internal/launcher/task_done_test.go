package launcher

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A worker that finishes into a closed port has done the work and written
// the files; losing the result to a connection error is the worst possible
// outcome. The broker is a separate long-lived process, and restarting it
// under a running worker is an ordinary thing to do by accident — it
// happened to a four-worker run.

// agentScripts sets up an agent against the given broker URL and returns
// its scripts directory.
func agentScripts(t *testing.T, brokerURL string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	l := New(brokerURL, "run-td", t.TempDir())
	l.sessionDir = filepath.Join(home, ".narwhal", "sessions", "run-td")

	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-1", "run-td", false)
	dir, err := l.SetupAgent(a, WorkerConfig{
		AgentID: a.ID, TaskID: "task-1", Assignment: "investigate",
	})
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}
	return dir
}

func TestTaskDoneScriptIsValidBash(t *testing.T) {
	// The script is built by string formatting with %% escapes and nested
	// quoting; a syntax error would only show up as a worker failing at
	// the very end of its run.
	dir := agentScripts(t, "http://127.0.0.1:1")
	script := filepath.Join(dir, "scripts", "task-done")

	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected the script: %v\n%s", err, out)
	}
}

func TestTaskDoneRecordsTheOutcomeWhenTheBrokerIsGone(t *testing.T) {
	dir := agentScripts(t, "http://127.0.0.1:1") // nothing listens there
	script := filepath.Join(dir, "scripts", "task-done")

	out, err := exec.Command("bash", script, "task-1", "here is the answer").CombinedOutput()
	if err == nil {
		t.Fatal("task-done reported success against a dead broker")
	}
	if !strings.Contains(string(out), "not lost") {
		t.Errorf("the worker was not told its outcome was kept:\n%s", out)
	}

	recorded, rerr := os.ReadFile(filepath.Join(dir, "outcome-task-1.json"))
	if rerr != nil {
		t.Fatalf("the outcome was not recorded: %v", rerr)
	}
	if !strings.Contains(string(recorded), "here is the answer") {
		t.Errorf("the recorded outcome is not the worker's: %s", recorded)
	}
}

func TestTaskDoneSucceedsAgainstALiveBroker(t *testing.T) {
	// The retry loop must not change the ordinary path: a 200 completes on
	// the first attempt and writes no local record.
	srv := newStubBroker(t, 200, `{"task_id":"task-1","state":"completed"}`)
	defer srv.Close()

	dir := agentScripts(t, srv.URL+"/api/v1/agents/tok")
	script := filepath.Join(dir, "scripts", "task-done")

	out, err := exec.Command("bash", script, "task-1", "the answer").CombinedOutput()
	if err != nil {
		t.Fatalf("task-done failed against a live broker: %v\n%s", err, out)
	}
	if _, serr := os.Stat(filepath.Join(dir, "outcome-task-1.json")); serr == nil {
		t.Error("a successful task-done still wrote a local record")
	}
}

func TestTaskDoneSurfacesTheFoldInRound(t *testing.T) {
	// 202 means peers posted during the wait and the task is not complete.
	// Exiting 0 there would let the worker stop on a stale answer.
	srv := newStubBroker(t, 202, `{"task_id":"task-1","new_messages":[]}`)
	defer srv.Close()

	dir := agentScripts(t, srv.URL+"/api/v1/agents/tok")
	script := filepath.Join(dir, "scripts", "task-done")

	out, err := exec.Command("bash", script, "task-1", "partial").CombinedOutput()
	if err == nil {
		t.Fatal("a 202 was reported as success")
	}
	if !strings.Contains(string(out), "NOT COMPLETE YET") {
		t.Errorf("the worker was not told to fold findings in:\n%s", out)
	}
}

// newStubBroker answers every request with the given status and body.
func newStubBroker(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}
