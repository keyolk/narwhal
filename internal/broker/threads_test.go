package broker

import "testing"

func TestCreateStandardThreadsOpensAllFour(t *testing.T) {
	run := New().CreateRun("r", "prompt", "/tmp", "main")
	run.CreateStandardThreads()

	snap := run.Snapshot()
	got := make(map[string]bool, len(snap.Threads))
	for _, th := range snap.Threads {
		got[th.ID] = true
	}
	for _, want := range []string{
		PlanningThread, WorklogThread, ResultsThread, EnvironmentThread,
	} {
		if !got[want] {
			t.Errorf("standard thread %q was not created", want)
		}
	}
}

func TestEnvironmentThreadCarriesMessages(t *testing.T) {
	// The environment thread is where a worker reports that the ground
	// itself is broken, so peers do not each rediscover it.
	run := New().CreateRun("r", "prompt", "/tmp", "main")
	run.CreateStandardThreads()

	run.PostMessage(EnvironmentThread, "worker-task-1", nil, PriorityUrgent,
		"build is broken: make test fails on a missing header")

	var found *Message
	for _, m := range run.MessagesSince(0) {
		if m.ThreadID == EnvironmentThread {
			found = m
		}
	}
	if found == nil {
		t.Fatal("environment message not recorded")
	}
	if found.Priority != PriorityUrgent {
		t.Errorf("priority = %q, want urgent", found.Priority)
	}
}

func TestCreateStandardThreadsAcceptsCustomParticipants(t *testing.T) {
	run := New().CreateRun("r", "prompt", "/tmp", "main")
	run.CreateStandardThreads("alice", "bob")

	for _, th := range run.Snapshot().Threads {
		if len(th.Participants) != 2 {
			t.Fatalf("thread %q participants = %v, want alice+bob",
				th.ID, th.Participants)
		}
	}
}
