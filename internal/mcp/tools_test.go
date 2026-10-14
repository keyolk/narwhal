package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestADuplicateWarningIsHardToMiss(t *testing.T) {
	// The field alone is one key in a JSON blob among five others, and
	// the whole point is to interrupt: an unfinished run doing this exact
	// work already exists, and redoing it costs real money.
	out := annotateDuplicate(`{"run_id":"s2","duplicate_of":"s1"}`)
	if !strings.Contains(out, "s1") {
		t.Errorf("the duplicate run is not named: %q", out)
	}
	if !strings.Contains(out, "narwhal_status") {
		t.Errorf("the note does not say how to find the earlier results: %q", out)
	}
	// The run was started, not refused — saying otherwise would be a lie
	// the caller acts on.
	if !strings.Contains(out, "started anyway") {
		t.Errorf("the note does not say the new run is running: %q", out)
	}
}

func TestNoNoteWithoutADuplicate(t *testing.T) {
	// A warning on every spawn is a warning nobody reads.
	out := annotateDuplicate(`{"run_id":"s2"}`)
	if strings.Contains(out, "NOTE") {
		t.Errorf("an ordinary spawn was annotated: %q", out)
	}
}

// A run started from a Claude Code session carries that session, so its
// cockpit can say when the run needs the person.
func TestStartingARunSendsTheSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-1")
	t.Setenv("TMUX_PANE", "%9")
	var got map[string]any
	if err := json.Unmarshal(withOrigin([]byte(`{"prompt":"x"}`)), &got); err != nil {
		t.Fatal(err)
	}
	o, _ := got["origin"].(map[string]any)
	if o["session_id"] != "s-1" || o["tmux_pane"] != "%9" || got["prompt"] != "x" {
		t.Errorf("body = %v", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if b := string(withOrigin([]byte(`{"prompt":"x"}`))); b != `{"prompt":"x"}` {
		t.Errorf("outside Claude Code the body changed: %s", b)
	}
}
