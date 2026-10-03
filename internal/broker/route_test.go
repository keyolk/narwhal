package broker

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// assignJudge answers by matching a keyword against the worker's
// assignment, so routing is testable without a model.
type assignJudge struct {
	mu    sync.Mutex
	calls int
	match string
}

func (j *assignJudge) Ask(_ context.Context, state, name, _ string) (float64, bool) {
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	_, assignment, _ := strings.Cut(state, "ASSIGNMENT:\n")
	if strings.Contains(assignment, j.match) {
		return 0.9, true
	}
	return 0.1, true
}

func routeRun(t *testing.T, j Judge) *Run {
	t.Helper()
	b := New()
	b.SetJudge(j)
	r := b.CreateRun("r", "p", "/tmp", "main")
	for id, a := range map[string]string{"api": "write the token endpoints", "client": "build client state", "idle": "not started"} {
		task := r.AddTask(id, id, a, nil)
		if id != "idle" {
			task.StartDispatch("d", "worker-"+id)
		}
	}
	return r
}

func messagesFrom(r *Run, sender string) []*Message {
	var out []*Message
	for _, m := range r.MessagesSince(0) {
		if m.Sender == sender {
			out = append(out, m)
		}
	}
	return out
}

// An unaddressed instruction reaches the running worker it concerns, and
// only that one.
func TestUnaddressedInstructionIsRelayedToItsWorker(t *testing.T) {
	j := &assignJudge{match: "token"}
	r := routeRun(t, j)
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "make sure nothing logs the token")
	r.IntakeGraphRequests(0)

	relays := messagesFrom(r, "coordinator")
	if len(relays) != 1 {
		t.Fatalf("relays = %d, want 1", len(relays))
	}
	if len(relays[0].Mentions) != 1 || relays[0].Mentions[0] != "worker-api" {
		t.Errorf("relayed to %v, want [worker-api]", relays[0].Mentions)
	}
	if !strings.Contains(relays[0].Content, "nothing logs the token") {
		t.Errorf("relay lost the instruction: %q", relays[0].Content)
	}
	// Two running workers asked; the idle one was not.
	if j.calls != 2 {
		t.Errorf("judge asked %d times, want once per running worker", j.calls)
	}
	routed := 0
	for _, v := range r.Verdicts() {
		if v.Question == QuestionRoute && v.Action == ActionRouted {
			routed++
		}
	}
	if routed != 1 {
		t.Errorf("routed verdicts = %d", routed)
	}
}

// An instruction no running task covers is new work. Saying so is the
// whole response: minting a task from a misread instruction spends a run.
func TestInstructionNobodyCoversIsFlaggedNotTasked(t *testing.T) {
	r := routeRun(t, &assignJudge{match: "nothing matches this"})
	before := len(r.SnapshotTasks())
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "also add a metrics endpoint")
	r.IntakeGraphRequests(0)

	if len(r.SnapshotTasks()) != before {
		t.Error("routing created a task")
	}
	relays := messagesFrom(r, "coordinator")
	if len(relays) != 1 || !strings.Contains(relays[0].Content, "may need a new task") ||
		relays[0].Priority != PriorityUrgent {
		t.Errorf("relays = %+v", relays)
	}
}

// An addressed instruction already reaches its worker; routing it again
// would wake others for nothing.
func TestAddressedInstructionIsNotRouted(t *testing.T) {
	j := &assignJudge{match: "token"}
	r := routeRun(t, j)
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "@client use camelCase")
	r.PostMessage(WorklogThread, "main", []string{"worker-api"}, PriorityNormal, "rename the field")
	r.IntakeGraphRequests(0)
	if j.calls != 0 || len(messagesFrom(r, "coordinator")) != 0 {
		t.Errorf("addressed instructions were routed: calls=%d", j.calls)
	}
}

// Only the operator's messages are routed, and a relay is never routed
// again — that would loop.
func TestOnlyOperatorMessagesAreRoutedOnce(t *testing.T) {
	j := &assignJudge{match: "token"}
	r := routeRun(t, j)
	r.PostMessage(WorklogThread, "worker-api", nil, PriorityNormal, "found the token handler")
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "rotate the token")
	cur := r.IntakeGraphRequests(0)
	r.IntakeGraphRequests(cur)
	r.IntakeGraphRequests(0) // a restarted dispatcher passing 0

	if n := len(messagesFrom(r, "coordinator")); n != 1 {
		t.Errorf("relays = %d, want exactly 1", n)
	}
}

func TestNoJudgeNoRouting(t *testing.T) {
	r := routeRun(t, nil)
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "careful with the token")
	r.IntakeGraphRequests(0)
	if len(messagesFrom(r, "coordinator")) != 0 || len(r.Verdicts()) != 0 {
		t.Error("routed without a judge")
	}
}

// An undecided judge says nothing: "no worker covers this" would be a
// claim it did not make.
func TestUndecidedRoutingStaysQuiet(t *testing.T) {
	r := routeRun(t, JudgeFunc(func(context.Context, string, string, string) (float64, bool) { return 0, false }))
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "careful with the token")
	r.IntakeGraphRequests(0)
	if n := len(messagesFrom(r, "coordinator")); n != 0 {
		t.Errorf("undecided routing posted %d messages", n)
	}
}

// Scores bunched by a shared preamble: only the workers near the best one
// get the instruction, not everything over the threshold.
func TestRoutingKeepsOnlyWorkersNearTheBest(t *testing.T) {
	scores := map[string]float64{"write the token endpoints": 0.96, "build client state": 0.75}
	j := JudgeFunc(func(_ context.Context, state, _, _ string) (float64, bool) {
		_, a, _ := strings.Cut(state, "ASSIGNMENT:\n")
		return scores[a], true
	})
	r := routeRun(t, j)
	r.PostMessage(WorklogThread, "main", nil, PriorityNormal, "group the key handlers")
	r.IntakeGraphRequests(0)
	relays := messagesFrom(r, "coordinator")
	if len(relays) != 1 || strings.Join(relays[0].Mentions, ",") != "worker-api" {
		t.Errorf("relays = %+v, want only worker-api (0.75 is over the threshold but far from 0.96)", relays)
	}
}
