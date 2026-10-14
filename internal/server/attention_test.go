package server

import (
	"testing"
)

// A plan from a Claude Code session records it; the session's attention
// lists that run's question, and another session's does not.
func TestAttentionIsPerSession(t *testing.T) {
	addr, argsFile := planFixture(t)
	code, out := postPlan(t, addr, map[string]any{"cwd": t.TempDir(), "prompt": "install it",
		"origin": map[string]string{"session_id": "s-1", "tmux_pane": "%9"}})
	if code != 201 {
		t.Fatalf("plan: %d %v", code, out)
	}
	runID := out["run_id"].(string)
	status := getJSON(t, addr+"/api/v1/control/status?run_id="+runID)
	snap := status["snapshot"].(map[string]any)
	if o, _ := snap["origin"].(map[string]any); o["session_id"] != "s-1" {
		t.Fatalf("origin = %v", snap["origin"])
	}

	tok := plannerTokenFor(t, argsFile)
	postJSON(t, addr+"/api/v1/agents/"+tok+"/ask", map[string]any{"question": "SessionStart only?", "assumed": "yes", "blocking": true})

	mine := getJSON(t, addr+"/api/v1/control/attention?session=s-1")["items"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["kind"] != "question" {
		t.Fatalf("s-1 attention = %v", mine)
	}
	if other := getJSON(t, addr+"/api/v1/control/attention?session=s-2")["items"].([]any); len(other) != 0 {
		t.Errorf("another session sees %v", other)
	}
	if all := getJSON(t, addr+"/api/v1/control/attention")["items"].([]any); len(all) != 1 {
		t.Errorf("all = %v", all)
	}
}
