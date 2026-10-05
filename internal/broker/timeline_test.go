package broker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// When a task ran is part of the record now, and survives a restart.
func TestSnapshotRecordsWhenATaskRan(t *testing.T) {
	ts := completedRun(t).Snapshot().Tasks[0]
	if ts.StartedAt.IsZero() || ts.EndedAt.IsZero() || ts.EndedAt.Before(ts.StartedAt) {
		t.Fatalf("started %v ended %v", ts.StartedAt, ts.EndedAt)
	}
	// Through JSON, the way a run reaches disk and comes back.
	raw, err := json.Marshal(completedRun(t).Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	again := RestoreRun(s).Snapshot().Tasks[0]
	if !again.EndedAt.Equal(s.Tasks[0].EndedAt) || !again.StartedAt.Equal(s.Tasks[0].StartedAt) {
		t.Errorf("times lost across restore: %v/%v, want %v/%v",
			again.StartedAt, again.EndedAt, s.Tasks[0].StartedAt, s.Tasks[0].EndedAt)
	}
}

// A task still pending has no time and must not write one to disk.
func TestAnUndispatchedTaskHasNoTimes(t *testing.T) {
	r := New().CreateRun("r1", "p", "/tmp", "main")
	r.AddTask("t", "n", "a", nil)
	raw, _ := json.Marshal(r.Snapshot().Tasks[0])
	if strings.Contains(string(raw), "started_at") || strings.Contains(string(raw), "ended_at") {
		t.Errorf("zero times serialized: %s", raw)
	}
}

func TestTimelineTellsTheRunInOrder(t *testing.T) {
	at := func(sec int) time.Time { return time.Unix(1_000_000+int64(sec), 0) }
	s := Snapshot{
		StartedAt: at(0).Unix(),
		Prompt:    "find the leak",
		Tasks: []TaskSnapshot{
			{ID: "api", State: TaskCompleted, Dispatches: 2, StartedAt: at(5), EndedAt: at(40),
				Outcome: "nonce reuse fixed\nlong detail"},
			{ID: "ci", State: TaskFailed, Dispatches: 1, StartedAt: at(1), EndedAt: at(30), Outcome: "timeout"},
			{ID: "synthesis", State: TaskPending},
		},
		Messages: []*Message{
			{Sender: "main", Content: "make sure nothing logs the token", CreatedAt: at(10)},
			{Sender: "coordinator", Content: RelayPrefix + " from the operator, for api: make sure…", CreatedAt: at(11)},
			{Sender: "worker-api", Priority: PriorityUrgent, Content: "BLOCKER: schema missing LockKey", CreatedAt: at(12)},
			{Sender: "worker-api", Priority: PriorityNormal, Content: "reading router.go", CreatedAt: at(13)},
			{Sender: "worker-api", Priority: PriorityUrgent, Content: FileClaimPrefix + "|api|x.go", CreatedAt: at(14)},
		},
		Verdicts: []Verdict{
			{At: at(20), TaskID: "ci", Question: QuestionNoResult, Decided: true, P: 0.9, Action: ActionRetry},
			{At: at(21), TaskID: "ci", Question: QuestionNoResult, Decided: false, Action: ActionFallback},
			{At: at(22), TaskID: "api", Question: QuestionRoute, Decided: true, P: 0.8, Action: ActionRouted},
			{At: at(23), TaskID: "api", Question: QuestionWorker, Asker: "worker-api", Ask: "is the fix complete?", Decided: true, P: 0.72},
		},
	}
	var got []string
	for _, e := range Timeline(s) {
		got = append(got, string(e.Kind)+" "+e.Who+" "+e.Text)
	}
	want := []string{
		"run  find the leak",
		"started ci ",
		"started api attempt 2",
		"instruction operator make sure nothing logs the token",
		"relay coordinator from the operator, for api: make sure…",
		"urgent api BLOCKER: schema missing LockKey",
		"verdict ci no-result → retry",
		"asked api is the fix complete? → 72%",
		"failed ci timeout",
		"completed api nonce reuse fixed …",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("timeline:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
