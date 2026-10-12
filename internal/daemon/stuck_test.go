package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

func transcriptLine(t *testing.T, content any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"message": map[string]any{"content": content}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeLoop writes a transcript of the same failing command, n times.
func writeLoop(t *testing.T, path string, n int) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	var lines []string
	lines = append(lines, transcriptLine(t, "the assignment"))
	for i := 0; i < n; i++ {
		id := "t" + string(rune('a'+i%26))
		lines = append(lines,
			transcriptLine(t, []map[string]any{{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "go test ./internal/api"}}}),
			transcriptLine(t, []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": []map[string]any{{"type": "text", "text": "FAIL: TestLock\nwant 200 got 500"}}}}))
	}
	must(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func TestRecentActionsReadsToolCallsAndTheirResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLoop(t, path, 30)
	got, err := recentActions(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 || got[19] != "Bash: go test ./internal/api  →  FAIL: TestLock want 200 got 500" {
		t.Fatalf("got %d actions, last %q", len(got), got[len(got)-1])
	}
}

// A worker that has run long is judged once per window, in the background;
// a stuck one is reported to the operator.
func TestALongRunningStuckWorkerIsReported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	saved := StuckAfter
	StuckAfter = time.Millisecond
	t.Cleanup(func() { StuckAfter = saved })

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	cwd := t.TempDir()
	var asked atomic.Int32
	sess.Broker.SetJudge(broker.JudgeFunc(func(_ context.Context, state, name, _ string) (float64, bool) {
		if name != broker.QuestionStuck {
			return 0, false
		}
		asked.Add(1)
		return 0.97, strings.Contains(state, "FAIL: TestLock")
	}))
	run := sess.Broker.CreateRun("r-stuck", "p", cwd, "main")
	run.CreateStandardThreads()
	task := run.AddTask("api", "api", "make go test ./internal/api pass", nil)
	task.StartDispatch("d1", "worker-api")
	sess.LauncherFor("r-stuck", cwd)

	const sid = "11111111-2222-4333-8444-555555555555"
	agentDir := filepath.Join(home, ".narwhal", "sessions", "r-stuck", "agents", "worker-api")
	must(t, os.MkdirAll(agentDir, 0o700))
	must(t, os.WriteFile(filepath.Join(agentDir, "claude-session-id"), []byte(sid+"\n"), 0o600))
	writeLoop(t, launcher.TranscriptPath(cwd, sid), 25)

	d := NewDispatcher(sess)
	time.Sleep(5 * time.Millisecond)
	d.checkStuck("r-stuck", run)
	waitFor(t, "the stuck worker to be reported", func() bool {
		for _, m := range run.MessagesSince(0) {
			if m.Sender == "coordinator" && strings.Contains(m.Content, "worker-api looks stuck") {
				return true
			}
		}
		return false
	})
	// Not again until the window has passed.
	StuckAfter = time.Hour
	d.checkStuck("r-stuck", run)
	time.Sleep(50 * time.Millisecond)
	if n := asked.Load(); n != 1 {
		t.Errorf("judged %d times, want once", n)
	}
	if task.CurrentState() != broker.TaskDispatched {
		t.Errorf("reporting changed the task: %s", task.CurrentState())
	}
}
