package mcp

import (
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
