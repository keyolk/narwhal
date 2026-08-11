// Package broker tests cover the core graph and radio state transitions.
package broker

import (
	"testing"
)

func TestCreateRunAndAddTask(t *testing.T) {
	b := New()
	r := b.CreateRun("run-1", "analyze auth", "/tmp/repo", "main")

	if r.ID != "run-1" {
		t.Fatalf("run id = %q, want run-1", r.ID)
	}
	if r.State != RunActive {
		t.Fatalf("run state = %q, want active", r.State)
	}

	t1 := r.AddTask("task-1", "investigate launcher", "desc", nil)
	if t1.State != TaskReady {
		t.Fatalf("task with no deps should be ready, got %q", t1.State)
	}

	t2 := r.AddTask("task-2", "synthesize", "desc", []string{"task-1"})
	if t2.State != TaskPending {
		t.Fatalf("task with unmet deps should be pending, got %q", t2.State)
	}
}

func TestDependencyActivation(t *testing.T) {
	b := New()
	r := b.CreateRun("run-2", "test deps", "/tmp", "main")

	t1 := r.AddTask("t1", "first", "", nil)
	t2 := r.AddTask("t2", "second", "", []string{"t1"})

	if t2.State != TaskPending {
		t.Fatalf("t2 should be pending before t1 completes, got %q", t2.State)
	}

	t1.CompleteDispatch("done", r)
	if t1.State != TaskCompleted {
		t.Fatalf("t1 should be completed, got %q", t1.State)
	}
	if t2.State != TaskReady {
		t.Fatalf("t2 should be ready after t1 completes, got %q", t2.State)
	}
}

func TestDispatchCircuitBreaker(t *testing.T) {
	b := New()
	r := b.CreateRun("run-3", "circuit breaker", "/tmp", "main")
	t1 := r.AddTask("t1", "flaky", "", nil)

	for i := 0; i < MaxDispatchFailures; i++ {
		t1.StartDispatch("d", "worker-1")
		t1.FailDispatch("boom", r)
	}
	if t1.State != TaskFailed {
		t.Fatalf("task should be failed after %d dispatch failures, got %q", MaxDispatchFailures, t1.State)
	}
}

func TestPostMessageAssignsMonotonicSeq(t *testing.T) {
	b := New()
	r := b.CreateRun("run-4", "radio", "/tmp", "main")

	m1 := r.PostMessage("th-1", "worker-1", nil, PriorityNormal, "hello")
	m2 := r.PostMessage("th-1", "worker-2", []string{"worker-1"}, PriorityUrgent, "hi back")

	if m1.Seq != 1 || m2.Seq != 2 {
		t.Fatalf("seq = %d, %d; want 1, 2", m1.Seq, m2.Seq)
	}
	if m2.Priority != PriorityUrgent {
		t.Fatalf("priority = %q, want urgent", m2.Priority)
	}
}

func TestSnapshotIsConsistent(t *testing.T) {
	b := New()
	r := b.CreateRun("run-5", "snapshot", "/tmp", "main")
	r.AddTask("t1", "first", "do thing", nil)
	r.CreateThread("th-1", "worklog", []string{"a", "b"})

	s := r.Snapshot()
	if len(s.Tasks) != 1 {
		t.Fatalf("snapshot tasks = %d, want 1", len(s.Tasks))
	}
	if len(s.Threads) != 1 {
		t.Fatalf("snapshot threads = %d, want 1", len(s.Threads))
	}
	if s.Tasks[0].Name != "first" {
		t.Fatalf("task name = %q, want first", s.Tasks[0].Name)
	}
}
