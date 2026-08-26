package broker

import "testing"

// fakeProbe records what it was asked to measure and answers from a table.
type fakeProbe struct {
	calls []string
	byID  map[string]*Usage
}

func (f *fakeProbe) TaskUsage(runID, taskID string) *Usage {
	f.calls = append(f.calls, taskID)
	return f.byID[taskID]
}

func probedRun(t *testing.T, p UsageProbe) (*Broker, *Run) {
	t.Helper()
	b := New()
	b.SetUsageProbe(p)
	return b, b.CreateRun("r1", "test", "/tmp", "main")
}

// The three ways a task stops running must all account for it. Accounting
// that covered only success would report the expensive half of a run —
// retries that each ran and lost, and workers killed mid-flight — as free.
func TestEveryTerminalPathMeasuresTheTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(run *Run, task *Task)
	}{
		{"completed", func(run *Run, task *Task) {
			task.StartDispatch("d1", "w")
			task.CompleteDispatch("done", run)
		}},
		{"failed by the breaker", func(run *Run, task *Task) {
			for i := 0; i < MaxDispatchFailures; i++ {
				task.StartDispatch("d", "w")
				task.FailDispatch("nope", run)
			}
		}},
		{"cancelled with the run", func(run *Run, task *Task) {
			task.StartDispatch("d1", "w")
			task.CancelDispatch("run canceled", run)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeProbe{byID: map[string]*Usage{
				"a": {OutputTokens: 42, Turns: 3, ServedModel: "claude-opus-5"},
			}}
			_, run := probedRun(t, probe)
			task := run.AddTask("a", "a", "x", nil)

			tc.stop(run, task)

			var got *Usage
			for _, ts := range run.SnapshotTasks() {
				if ts.ID == "a" {
					got = ts.Usage
				}
			}
			if got == nil {
				t.Fatalf("task carries no usage after %s; probe calls = %v", tc.name, probe.calls)
			}
			if got.OutputTokens != 42 || got.Turns != 3 {
				t.Errorf("usage = %+v, want the probe's answer", got)
			}
		})
	}
}

// A retry appends to the same transcript, so the probe's answer already
// covers every attempt. The guard matters for the reverse case: a cheap
// final attempt must not erase an expensive earlier one.
func TestARetryDoesNotOverwriteEarlierAccounting(t *testing.T) {
	probe := &fakeProbe{byID: map[string]*Usage{"a": {OutputTokens: 1000, Turns: 9}}}
	_, run := probedRun(t, probe)
	task := run.AddTask("a", "a", "x", nil)

	task.StartDispatch("d1", "w")
	task.FailDispatch("nope", run) // not terminal — back to ready
	task.StartDispatch("d2", "w")
	task.CompleteDispatch("done", run)

	// Now a second measurement reporting far less must not win.
	probe.byID["a"] = &Usage{OutputTokens: 1, Turns: 1}
	run.measureTask("a")

	for _, ts := range run.SnapshotTasks() {
		if ts.ID == "a" && ts.Usage.OutputTokens != 1000 {
			t.Errorf("OutputTokens = %d, want the first measurement kept", ts.Usage.OutputTokens)
		}
	}
}

// A task that never ran costs nothing and has no transcript. Reporting a
// zero tally would make it indistinguishable from a measured-free task.
func TestAnUnmeasurableTaskCarriesNoUsage(t *testing.T) {
	probe := &fakeProbe{byID: map[string]*Usage{}} // answers nil for everything
	_, run := probedRun(t, probe)
	task := run.AddTask("a", "a", "x", nil)
	task.StartDispatch("d1", "w")
	task.CompleteDispatch("done", run)

	for _, ts := range run.SnapshotTasks() {
		if ts.ID == "a" && ts.Usage != nil {
			t.Errorf("Usage = %+v, want nil when the probe cannot measure", ts.Usage)
		}
	}
}

// Without a probe nothing is measured and nothing panics — the default
// for every test that does not care about cost.
func TestNoProbeMeansNoAccounting(t *testing.T) {
	b := New()
	run := b.CreateRun("r1", "test", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	task.StartDispatch("d1", "w")
	task.CompleteDispatch("done", run)

	for _, ts := range run.SnapshotTasks() {
		if ts.ID == "a" && ts.Usage != nil {
			t.Errorf("Usage = %+v, want nil with no probe installed", ts.Usage)
		}
	}
}

// The snapshot must not alias the live task's usage: it is handed to
// callers that persist it, and a later measurement rewriting an
// already-written file is the bug that copy avoids.
func TestSnapshotUsageIsACopy(t *testing.T) {
	probe := &fakeProbe{byID: map[string]*Usage{
		"a": {OutputTokens: 5, ServedModels: []string{"claude-opus-5", "claude-haiku-4-5-20251001"}},
	}}
	_, run := probedRun(t, probe)
	task := run.AddTask("a", "a", "x", nil)
	task.StartDispatch("d1", "w")
	task.CompleteDispatch("done", run)

	snap := run.SnapshotTasks()
	for i := range snap {
		if snap[i].ID == "a" {
			snap[i].Usage.OutputTokens = 999
			snap[i].Usage.ServedModels[0] = "mutated"
		}
	}
	again := run.SnapshotTasks()
	for _, ts := range again {
		if ts.ID != "a" {
			continue
		}
		if ts.Usage.OutputTokens != 5 {
			t.Errorf("OutputTokens = %d, want 5 — the snapshot aliased the task", ts.Usage.OutputTokens)
		}
		if ts.Usage.ServedModels[0] != "claude-opus-5" {
			t.Errorf("ServedModels[0] = %q, want the slice copied too", ts.Usage.ServedModels[0])
		}
	}
}

// A run's total is only meaningful alongside how much of it was measured:
// a run whose transcripts were removed reports a small number for the
// same reason a cheap run does.
func TestRunUsageSeparatesMeasuredFromUnmeasured(t *testing.T) {
	probe := &fakeProbe{byID: map[string]*Usage{
		"a": {OutputTokens: 10, Turns: 1},
		"b": {OutputTokens: 20, Turns: 2},
	}}
	_, run := probedRun(t, probe)
	for _, id := range []string{"a", "b", "c"} {
		task := run.AddTask(id, id, "x", nil)
		task.StartDispatch("d-"+id, "w")
		task.CompleteDispatch("done", run)
	}
	// d never ran at all.
	run.AddTask("d", "d", "x", nil)

	total, measured, unmeasured := run.RunUsage()
	if total.OutputTokens != 30 || total.Turns != 3 {
		t.Errorf("total = %+v, want output 30 turns 3", total)
	}
	if measured != 2 {
		t.Errorf("measured = %d, want 2", measured)
	}
	// c was dispatched and could not be measured; d never ran, so it is
	// not a gap in the accounting.
	if unmeasured != 1 {
		t.Errorf("unmeasured = %d, want 1 (c dispatched but unmeasured; d never ran)", unmeasured)
	}
}

// A restored run keeps what its tasks already cost, and goes on measuring
// the ones still running. Surviving a daemon restart is the normal life
// of an interactive run.
func TestRestoreKeepsAccountingAndKeepsMeasuring(t *testing.T) {
	probe := &fakeProbe{byID: map[string]*Usage{"b": {OutputTokens: 7, Turns: 1}}}
	b := New()
	b.SetUsageProbe(probe)

	restored := RestoreRun(Snapshot{
		RunID: "r1", Prompt: "p", CWD: "/tmp", State: RunActive,
		Tasks: []TaskSnapshot{
			{ID: "a", Name: "a", State: TaskCompleted, Dispatches: 1,
				Usage: &Usage{OutputTokens: 100, Turns: 4}},
			{ID: "b", Name: "b", State: TaskDispatched, Dispatches: 1},
		},
	})
	b.AdoptRun(restored)

	var a *Usage
	for _, ts := range restored.SnapshotTasks() {
		if ts.ID == "a" {
			a = ts.Usage
		}
	}
	if a == nil || a.OutputTokens != 100 {
		t.Fatalf("restored task a usage = %+v, want the persisted 100 output tokens", a)
	}

	// The still-running task finishes under the restored run and is measured.
	restored.GetTask("b").CompleteDispatch("done", restored)
	for _, ts := range restored.SnapshotTasks() {
		if ts.ID == "b" {
			if ts.Usage == nil || ts.Usage.OutputTokens != 7 {
				t.Errorf("task b usage = %+v, want the probe to have run after restore", ts.Usage)
			}
		}
	}
}
