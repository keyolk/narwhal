package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The tool schema and the planner's system prompt describe the same graph to
// two different builders, and only one of them was ever taught. The planner
// was told at length that a run wants a synthesis task, that deps on it gate
// completion rather than dispatch, and what an end condition is worth. The
// schema said "Task ids that must complete first" and offered no check field
// at all — and the schema is the path nearly every run takes. Every run built
// by the planner carries deps and a synthesis task; the six most recent, all
// spawned, carry neither, and not one carries a check.
//
// These tests hold the schema to the shared constants rather than to a copy
// of their wording, because a copy is what drifted.

// spawnItemSchema digs out the per-worker property schema, so a test can ask
// what a caller of narwhal_spawn is actually told.
func spawnItemSchema(t *testing.T) map[string]any {
	t.Helper()
	for _, tool := range toolDefinitions() {
		if tool["name"] != "narwhal_spawn" {
			continue
		}
		in, _ := tool["inputSchema"].(map[string]any)
		props, _ := in["properties"].(map[string]any)
		workers, _ := props["workers"].(map[string]any)
		items, _ := workers["items"].(map[string]any)
		fields, _ := items["properties"].(map[string]any)
		if fields == nil {
			t.Fatal("narwhal_spawn has no per-worker property schema")
		}
		return fields
	}
	t.Fatal("narwhal_spawn is not in the tool definitions")
	return nil
}

func TestSpawnTeachesTheSameGraphRulesAsThePlanner(t *testing.T) {
	fields := spawnItemSchema(t)

	deps, _ := fields["deps"].(map[string]any)
	if got, _ := deps["description"].(string); got != broker.DepsContract {
		t.Errorf("the deps description is not the shared contract:\n got: %q", got)
	}

	check, _ := fields["check"].(map[string]any)
	if check == nil {
		t.Fatal("narwhal_spawn cannot pass an end condition; the gate is unreachable from this path")
	}
	if got, _ := check["description"].(string); got != broker.CheckContract {
		t.Errorf("the check description is not the shared contract:\n got: %q", got)
	}

	// Model steering is the Cursor economics split — frontier synthesis,
	// cheap investigators. narwhal_plan exposes it and the server accepted
	// it per-worker all along; the schema just never offered it, so every
	// spawned task ran on the launcher default.
	if _, ok := fields["model"]; !ok {
		t.Error("narwhal_spawn cannot set a per-worker model tier")
	}
}

func TestSpawnDoesNotDescribeItselfAsForIndependentWorkOnly(t *testing.T) {
	// "Launch workers on independent sub-tasks" reads as an instruction not
	// to build edges. The caller followed it.
	for _, tool := range toolDefinitions() {
		if tool["name"] != "narwhal_spawn" {
			continue
		}
		desc, _ := tool["description"].(string)
		if strings.Contains(desc, "independent sub-tasks") {
			t.Error("narwhal_spawn still advertises itself as being for independent work only")
		}
		if !strings.Contains(desc, "synthesis") {
			t.Error("narwhal_spawn does not mention that a multi-worker run wants a synthesis task")
		}
	}
}

func TestAGraphGapIsHardToMiss(t *testing.T) {
	// A key beside session_dir and a worker array is not read. The whole
	// point is to interrupt: the run just started is the shape that has
	// been returning unreconciled fragments.
	out := annotateGraphGap(`{"run_id":"s2","graph_gap":"no synthesis task: name it \"synthesis\""}`)
	if !strings.Contains(out, "GRAPH GAP") {
		t.Errorf("the gap is not surfaced: %q", out)
	}
	if !strings.Contains(out, "synthesis") {
		t.Errorf("the gap's content was dropped: %q", out)
	}
}

func TestNoGapNoteOnAWellFormedRun(t *testing.T) {
	out := annotateGraphGap(`{"run_id":"s2"}`)
	if strings.Contains(out, "GRAPH GAP") {
		t.Errorf("a well-formed run was annotated: %q", out)
	}
}

// The schema is serialized to the client, so a contract string that cannot
// survive JSON encoding would reach the caller mangled or not at all.
func TestTheSchemaStillSerializes(t *testing.T) {
	if _, err := json.Marshal(toolDefinitions()); err != nil {
		t.Fatalf("the tool definitions no longer encode: %v", err)
	}
}
