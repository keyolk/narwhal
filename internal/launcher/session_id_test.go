package launcher

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// uuidV4 is what claude --session-id accepts. A plain random hex string is
// rejected, so the version and variant nibbles matter.
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestSessionUUIDIsAValidV4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newSessionUUID()
		if err != nil {
			t.Fatalf("newSessionUUID: %v", err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("claude would reject %q as a session id", id)
		}
		if seen[id] {
			t.Fatalf("duplicate session id %q — two workers would share a transcript", id)
		}
		seen[id] = true
	}
}

func TestSessionIDIsRecordedForTheMonitor(t *testing.T) {
	// The monitor is a separate process, so an id kept only in the
	// launcher's memory cannot be used to attach. It has to land on disk
	// next to the agent's other artifacts.
	home := t.TempDir()
	t.Setenv("HOME", home)

	l := New("http://127.0.0.1:1", "run-sid", t.TempDir())
	l.sessionDir = filepath.Join(home, ".narwhal", "sessions", "run-sid")

	reg := broker.NewAgentRegistry()
	agent := reg.Register("worker-1", "run-sid", false)
	if _, err := l.SetupAgent(agent, WorkerConfig{
		AgentID: agent.ID, TaskID: "task-1", Assignment: "investigate",
	}); err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	l.recordSession(agent.ID, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")

	path := filepath.Join(l.sessionDir, "agents", agent.ID, "claude-session-id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session id not recorded where the monitor looks: %v", err)
	}
	if got := string(data); got != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n" {
		t.Fatalf("recorded %q", got)
	}
}

func TestSessionIDIsReadableFromTheLauncher(t *testing.T) {
	l := New("http://127.0.0.1:1", "run-sid", t.TempDir())
	if got := l.SessionID("worker-1"); got != "" {
		t.Fatalf("unlaunched worker reported session %q", got)
	}
	l.sessions = map[string]string{"worker-1": "some-id"}
	if got := l.SessionID("worker-1"); got != "some-id" {
		t.Fatalf("SessionID = %q", got)
	}
}
