package server_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
)

// The whole loop against a real broker: the script a worker is actually
// given, the server that actually gates, and a task that actually
// carries an end condition.
//
// #18 established why this is worth its cost — the wrapper scripts had
// been tested as strings rather than run, and a worker is handed the
// script, not the string. Here the two 202 paths, the exit codes, and the
// snapshot fields all have to agree, and each is written in a different
// place.
func TestAWorkerAnswersItsCheckThroughTheRealScriptAndServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-rt", "count exported functions", "/tmp", "main")
	run.CreateStandardThreads()
	task := run.AddTask("task-1", "count", "count exported functions in note.md", nil)
	task.SetCheck("Confirm the names you report start with a capital letter.")

	srv := server.New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown()

	agent := reg.Register("worker-task-1", "run-rt", false)
	// New reads HOME for its session directory, which t.Setenv has
	// already pointed at a temp dir — the store guard refuses a test
	// write into the developer's own ~/.narwhal (#23).
	l := launcher.New(addr, "run-rt", t.TempDir())
	dir, err := l.SetupAgent(agent, launcher.WorkerConfig{
		AgentID: agent.ID, TaskID: "task-1", Assignment: "count them",
	})
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}
	script := filepath.Join(dir, "scripts", "task-done")

	// First call: the worker submits its answer without having been
	// asked anything.
	out, err := exec.Command("bash", script, "task-1", "3 functions are exported").CombinedOutput()
	if err == nil {
		t.Fatal("the task completed on the first call despite carrying a check")
	}
	if code := err.(*exec.ExitError).ExitCode(); code != 5 {
		t.Fatalf("exit code = %d, want 5 (the check round)\n%s", code, out)
	}
	if !strings.Contains(string(out), "capital letter") {
		t.Errorf("the 202 did not carry the check text back to the worker:\n%s", out)
	}
	if got := task.CurrentState(); got == broker.TaskCompleted {
		t.Fatal("the task completed without answering its check")
	}

	// Second call: the worker reports what the check showed — and it
	// contradicts the answer, which is the case the whole mechanism
	// exists for.
	out, err = exec.Command("bash", script, "task-1",
		"3 functions are exported", "final", "0",
		"only Alpha and Gamma start with a capital; beta does not, so the count is 2",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("the answering call failed: %v\n%s", err, out)
	}
	if got := task.CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("state = %s, want completed after answering", got)
	}

	// And both halves are in the record, which is what makes a
	// wrong-but-completed answer findable afterwards.
	var found bool
	for _, ts := range run.SnapshotTasks() {
		if ts.ID != "task-1" {
			continue
		}
		found = true
		if ts.Check == "" {
			t.Error("the snapshot lost the check")
		}
		if !strings.Contains(ts.CheckResult, "the count is 2") {
			t.Errorf("CheckResult = %q, want what the check actually showed", ts.CheckResult)
		}
	}
	if !found {
		t.Fatal("task-1 missing from the snapshot")
	}
}
