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

func TestSplitRequestParseAndFormat(t *testing.T) {
	// Round-trip: format then parse should preserve all fields.
	deps := []string{"task-1", "task-2"}
	body := FormatSplitRequest("task-3", "lifecycle", "investigate lifecycle", deps)
	id, name, assignment, parsedDeps, ok := ParseSplitRequest(body)
	if !ok {
		t.Fatalf("parse failed on %q", body)
	}
	if id != "task-3" || name != "lifecycle" || assignment != "investigate lifecycle" {
		t.Fatalf("fields = %q/%q/%q, want task-3/lifecycle/investigate lifecycle", id, name, assignment)
	}
	if len(parsedDeps) != 2 || parsedDeps[0] != "task-1" || parsedDeps[1] != "task-2" {
		t.Fatalf("deps = %v, want [task-1 task-2]", parsedDeps)
	}

	// No deps case.
	body2 := FormatSplitRequest("task-4", "solo", "standalone", nil)
	_, _, _, parsedDeps2, ok2 := ParseSplitRequest(body2)
	if !ok2 {
		t.Fatalf("parse failed on %q", body2)
	}
	if len(parsedDeps2) != 0 {
		t.Fatalf("expected 0 deps, got %v", parsedDeps2)
	}

	// Non-split-request message should not parse.
	_, _, _, _, ok3 := ParseSplitRequest("just a regular FYI message")
	if ok3 {
		t.Fatal("non-split-request content should not parse")
	}

	// Malformed (missing fields).
	_, _, _, _, ok4 := ParseSplitRequest("SPLIT_REQUEST|only-one-field")
	if ok4 {
		t.Fatal("malformed split request should not parse")
	}
}

func TestMessagesSinceRespectsCursor(t *testing.T) {
	b := New()
	r := b.CreateRun("run-cursor", "test", "/tmp", "main")

	r.PostMessage("th", "a", nil, PriorityNormal, "m1")
	r.PostMessage("th", "b", nil, PriorityNormal, "m2")
	r.PostMessage("th", "c", nil, PriorityNormal, "m3")

	all := r.MessagesSince(0)
	if len(all) != 3 {
		t.Fatalf("MessagesSince(0) = %d, want 3", len(all))
	}
	since1 := r.MessagesSince(1)
	if len(since1) != 2 {
		t.Fatalf("MessagesSince(1) = %d, want 2", len(since1))
	}
	if since1[0].Seq != 2 || since1[1].Seq != 3 {
		t.Fatalf("seqs = %d, %d; want 2, 3", since1[0].Seq, since1[1].Seq)
	}
}

func TestMessagesMentioningFiltersByMention(t *testing.T) {
	b := New()
	r := b.CreateRun("run-mention", "test", "/tmp", "main")

	// Message mentioning worker-1 only.
	r.PostMessage("th", "sender", []string{"worker-1"}, PriorityUrgent, "for you")
	// Broadcast (no mentions) — visible to everyone.
	r.PostMessage("th", "sender", nil, PriorityFYI, "broadcast")
	// Message mentioning worker-2 only.
	r.PostMessage("th", "sender", []string{"worker-2"}, PriorityNormal, "not for you")

	worker1Msgs := r.MessagesMentioning(0, "worker-1")
	if len(worker1Msgs) != 2 {
		t.Fatalf("worker-1 should see 2 messages (mention + broadcast), got %d", len(worker1Msgs))
	}
	worker2Msgs := r.MessagesMentioning(0, "worker-2")
	if len(worker2Msgs) != 2 {
		t.Fatalf("worker-2 should see 2 messages (mention + broadcast), got %d", len(worker2Msgs))
	}
	// Cursor filter: only messages after seq 1.
	worker1After1 := r.MessagesMentioning(1, "worker-1")
	if len(worker1After1) != 1 {
		t.Fatalf("worker-1 after seq 1 should see 1 (broadcast), got %d", len(worker1After1))
	}
}
