package launcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// sendCapture stands in for the broker's /send endpoint and records what the
// wrapper script actually posted.
type sendCapture struct {
	mu   sync.Mutex
	body map[string]any
}

func (c *sendCapture) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

// runSendScript prepares an agent, then invokes its send wrapper with the
// given arguments against a stub broker, returning the decoded payload.
func runSendScript(t *testing.T, args ...string) map[string]any {
	t.Helper()

	cap := &sendCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode send body: %v", err)
		}
		cap.mu.Lock()
		cap.body = body
		cap.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)

	l := New(srv.URL, "run-test", t.TempDir())
	// New() resolved sessionDir from the ambient HOME; point it at the
	// per-test one so the scripts land somewhere the test owns.
	l.sessionDir = filepath.Join(home, ".narwhal", "sessions", "run-test")

	reg := broker.NewAgentRegistry()
	agent := reg.Register("worker-1", "run-test", false)
	agentDir, err := l.SetupAgent(agent, WorkerConfig{
		AgentID:    agent.ID,
		TaskID:     "task-1",
		Assignment: "investigate",
	})
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	script := filepath.Join(agentDir, "scripts", "send")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("send script missing: %v", err)
	}
	out, err := exec.Command("bash", append([]string{script}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("send script failed: %v\n%s", err, out)
	}
	body := cap.last()
	if body == nil {
		t.Fatalf("broker never received a send; script output: %s", out)
	}
	return body
}

func mentionsOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["mentions"].([]any)
	if !ok {
		if body["mentions"] == nil {
			return nil
		}
		t.Fatalf("mentions is %T, want a list: %v", body["mentions"], body["mentions"])
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		out = append(out, m.(string))
	}
	return out
}

func TestSendTreatsBarePriorityAsPriority(t *testing.T) {
	// `send worklog "..." urgent` reads as a priority to anyone writing it.
	// Taken literally it @-mentions an agent named "urgent", which narrows the
	// message to a recipient that cannot exist — peers never see it, and the
	// sender gets no error.
	for _, prio := range []string{"fyi", "normal", "urgent"} {
		body := runSendScript(t, "worklog", "a finding", prio)
		if got := mentionsOf(t, body); len(got) != 0 {
			t.Fatalf("%q landed in mentions: %v", prio, got)
		}
		if got := body["priority"]; got != prio {
			t.Fatalf("priority = %v, want %q", got, prio)
		}
	}
}

func TestSendKeepsExplicitMentions(t *testing.T) {
	body := runSendScript(t, "worklog", "a finding", "task-2,task-3", "urgent")
	got := mentionsOf(t, body)
	if len(got) != 2 || got[0] != "task-2" || got[1] != "task-3" {
		t.Fatalf("mentions = %v, want [task-2 task-3]", got)
	}
	if body["priority"] != "urgent" {
		t.Fatalf("priority = %v, want urgent", body["priority"])
	}
}

func TestSendKeepsPriorityNamedAgentWhenFourArgsGiven(t *testing.T) {
	// With all four arguments present the caller has been explicit about both
	// slots, so an agent that happens to be named like a priority is still a
	// mention.
	body := runSendScript(t, "worklog", "a finding", "urgent", "fyi")
	got := mentionsOf(t, body)
	if len(got) != 1 || got[0] != "urgent" {
		t.Fatalf("mentions = %v, want [urgent]", got)
	}
	if body["priority"] != "fyi" {
		t.Fatalf("priority = %v, want fyi", body["priority"])
	}
}

func TestSendDefaultsToNormalPriority(t *testing.T) {
	body := runSendScript(t, "worklog", "a finding")
	if len(mentionsOf(t, body)) != 0 {
		t.Fatalf("bare send should not mention anyone: %v", body["mentions"])
	}
	if body["priority"] != "normal" {
		t.Fatalf("priority = %v, want normal", body["priority"])
	}
}
