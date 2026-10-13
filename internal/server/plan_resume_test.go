package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitArgs waits for a planner invocation whose arguments contain want.
func waitArgs(t *testing.T, argsFile, want string) string {
	t.Helper()
	for i := 0; i < 150; i++ {
		files, _ := filepath.Glob(argsFile + ".*")
		for _, f := range files {
			if strings.HasSuffix(f, ".tmp") {
				continue
			}
			if b, _ := os.ReadFile(f); strings.Contains(string(b), want) {
				return string(b)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no planner run with %q", want)
	return ""
}

// The planner asks and exits; answering its questions resumes its session
// with the answers — after the last one, not each.
func TestAnsweringThePlannerResumesIt(t *testing.T) {
	addr, argsFile := planFixture(t)
	code, out := postPlan(t, addr, map[string]any{"cwd": t.TempDir(), "prompt": "install it locally"})
	if code != 201 {
		t.Fatalf("plan: %d %v", code, out)
	}
	runID := out["run_id"].(string)
	first := waitArgs(t, argsFile, "--session-id")
	sid := ""
	lines := strings.Split(first, "\n")
	for i, l := range lines {
		if l == "--session-id" && i+1 < len(lines) {
			sid = lines[i+1]
		}
	}
	if sid == "" {
		t.Fatalf("planner started without a session id:\n%s", first)
	}

	// The planner's two blocking questions return at once.
	tok := ""
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "NARWHAL_AGENT_TOKEN="); ok {
			tok, _, _ = strings.Cut(rest, " ")
		}
	}
	if tok == "" {
		t.Fatal("no planner token in its instructions")
	}
	for _, q := range []string{"hooks: SessionStart only?", "which profile?"} {
		start := time.Now()
		r := postJSON(t, addr+"/api/v1/agents/"+tok+"/ask", map[string]any{"question": q, "assumed": "yes", "blocking": true})
		if time.Since(start) > 2*time.Second || r["status"] != "recorded" {
			t.Fatalf("planner ask held or failed: %v", r)
		}
	}
	// The stand-in planner has exited by now; give its Wait a moment.
	time.Sleep(100 * time.Millisecond)

	postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": runID,
		"answers": []map[string]any{{"id": 1, "answer": "both hooks"}}})
	time.Sleep(200 * time.Millisecond)
	if files, _ := filepath.Glob(argsFile + ".*"); countWith(files, "--resume") != 0 {
		t.Fatal("resumed before every question was answered")
	}
	postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": runID,
		"answers": []map[string]any{{"id": 2, "answer": "default"}}})
	resumed := waitArgs(t, argsFile, "--resume")
	for _, w := range []string{sid, "both hooks", "default", "hooks: SessionStart only?"} {
		if !strings.Contains(resumed, w) {
			t.Errorf("resume lacks %q:\n%s", w, resumed)
		}
	}
}

func countWith(files []string, want string) int {
	n := 0
	for _, f := range files {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), want) {
			n++
		}
	}
	return n
}
