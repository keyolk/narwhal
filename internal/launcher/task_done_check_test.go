package launcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Two different 202s reach the script and they ask for different things.
// One says peers posted while the call waited; the other says the task
// has an end condition that has not been answered. Telling them apart by
// status code alone is impossible, so the script reads the body.
func TestTaskDoneTellsTheCheckRoundFromTheFoldInRound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_id": "task-1",
			"check":   "confirm the names you report are actually exported",
			"hint":    "run the check and call again",
		})
	}))
	defer srv.Close()

	dir := agentScripts(t, srv.URL)
	script := filepath.Join(dir, "scripts", "task-done")

	out, err := exec.Command("bash", script, "task-1", "the answer").CombinedOutput()
	if err == nil {
		t.Fatal("task-done reported success on a 202 that asked for a check")
	}
	if code := err.(*exec.ExitError).ExitCode(); code != 5 {
		t.Errorf("exit code = %d, want 5 (the check round, not 4 the fold-in round)", code)
	}
	body := string(out)
	if !strings.Contains(body, "end condition") {
		t.Errorf("the script does not tell the worker a check is owed:\n%s", body)
	}
	// The worker has to know how to answer, or it will simply call again
	// identically and loop.
	if !strings.Contains(body, "what the check showed") {
		t.Errorf("the script does not show how to pass the result back:\n%s", body)
	}
}

// The fold-in round must keep its own exit code and message: a 202 with
// peer messages and no check is the older path and is unchanged.
func TestTaskDoneStillReportsTheFoldInRoundSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_id":      "task-1",
			"new_messages": []string{"a peer finding"},
		})
	}))
	defer srv.Close()

	dir := agentScripts(t, srv.URL)
	script := filepath.Join(dir, "scripts", "task-done")

	out, err := exec.Command("bash", script, "task-1", "the answer").CombinedOutput()
	if err == nil {
		t.Fatal("task-done reported success on a fold-in 202")
	}
	if code := err.(*exec.ExitError).ExitCode(); code != 4 {
		t.Errorf("exit code = %d, want 4 (the fold-in round)", code)
	}
	if !strings.Contains(string(out), "peers finished") {
		t.Errorf("the fold-in message was replaced:\n%s", out)
	}
}

// The check result has to reach the server, or answering it changes
// nothing and the worker loops forever.
func TestTaskDoneSendsTheCheckResult(t *testing.T) {
	var got struct {
		Outcome     string `json:"outcome"`
		CheckResult string `json:"check_result"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_id":"task-1","state":"completed"}`))
	}))
	defer srv.Close()

	dir := agentScripts(t, srv.URL)
	script := filepath.Join(dir, "scripts", "task-done")

	if out, err := exec.Command("bash", script, "task-1", "the answer", "final", "0",
		"ran it: none of the six names is exported").CombinedOutput(); err != nil {
		t.Fatalf("task-done failed: %v\n%s", err, out)
	}
	if got.CheckResult != "ran it: none of the six names is exported" {
		t.Errorf("check_result reached the server as %q", got.CheckResult)
	}
	if got.Outcome != "the answer" {
		t.Errorf("outcome = %q, want it unchanged by the new argument", got.Outcome)
	}
}

// A worker that could not reach the broker still ran its check. The
// outcome file is what harvest reads, so the answer has to be in it or it
// is lost for good.
func TestTheOutcomeFileCarriesTheCheckResult(t *testing.T) {
	dir := agentScripts(t, "http://127.0.0.1:1") // nothing listens there
	script := filepath.Join(dir, "scripts", "task-done")

	_, _ = exec.Command("bash", script, "task-1", "the answer", "final", "0",
		"checked: 3 of 3 confirmed").CombinedOutput()

	data, err := os.ReadFile(filepath.Join(dir, "outcome-task-1.json"))
	if err != nil {
		t.Fatalf("no outcome file: %v", err)
	}
	var body struct {
		Outcome     string `json:"outcome"`
		CheckResult string `json:"check_result"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("outcome file is not valid JSON: %v\n%s", err, data)
	}
	if body.CheckResult != "checked: 3 of 3 confirmed" {
		t.Errorf("check_result in the outcome file = %q", body.CheckResult)
	}
}

// Omitting the argument must stay valid: most tasks have no check.
func TestTaskDoneWithoutACheckResultStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_id":"task-1","state":"completed"}`))
	}))
	defer srv.Close()

	dir := agentScripts(t, srv.URL)
	script := filepath.Join(dir, "scripts", "task-done")

	if out, err := exec.Command("bash", script, "task-1", "the answer").CombinedOutput(); err != nil {
		t.Fatalf("task-done failed with no check argument: %v\n%s", err, out)
	}
}
