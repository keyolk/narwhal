package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// The runs on disk are the reason this file exists. Every run built through
// narwhal_plan carries dependency edges and a synthesis task; the six most
// recent runs, all built through narwhal_spawn, carry neither, and not one of
// them carries an end condition. The planner was taught what a graph should
// look like and the spawn path was not — and the spawn path is the one nearly
// every run actually takes.

func spawnFixture(t *testing.T) string {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()
	srv := New(b, reg)
	srv.SetController(&cancelController{l: launcher.New("http://127.0.0.1:1", "r-x", t.TempDir())})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr
}

func postSpawn(t *testing.T, addr string, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/control/spawn",
		"application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn returned %d: %v", resp.StatusCode, out)
	}
	return out
}

func TestSpawnCarriesTheEndCondition(t *testing.T) {
	// spawnWorkerSpec had no Check field while the task-create API did, so
	// a caller could pass one and it would be dropped without a word. The
	// gate that refuses an unanswered check is #43's whole point, and it
	// was unreachable from the path in use.
	addr := spawnFixture(t)
	out := postSpawn(t, addr, map[string]any{
		"cwd": t.TempDir(),
		"workers": []map[string]any{{
			"name":       "audit",
			"assignment": "look at auth/",
			"check":      "confirm the names you report are actually exported",
		}},
	})

	runID, _ := out["run_id"].(string)
	status := getJSON(t, addr+"/api/v1/control/status?run_id="+runID)
	snapshot, _ := status["snapshot"].(map[string]any)
	tasks, _ := snapshot["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d: %v", len(tasks), status)
	}
	task, _ := tasks[0].(map[string]any)
	if got, _ := task["check"].(string); !strings.Contains(got, "actually exported") {
		t.Errorf("the check did not reach the task: %v", task)
	}
}

func TestSpawnSaysWhatAFlatFanOutIsMissing(t *testing.T) {
	// Four workers, no task depending on them, no end condition anywhere:
	// the exact shape of the last six runs. Nothing in the reply said so,
	// and the caller had no reason to think anything was absent.
	addr := spawnFixture(t)
	out := postSpawn(t, addr, map[string]any{
		"cwd": t.TempDir(),
		"workers": []map[string]any{
			{"name": "a", "assignment": "look at a/"},
			{"name": "b", "assignment": "look at b/"},
			{"name": "c", "assignment": "look at c/"},
			{"name": "d", "assignment": "look at d/"},
		},
	})

	gap, _ := out["graph_gap"].(string)
	if gap == "" {
		t.Fatal("a four-worker run with no synthesis and no checks reported no gap")
	}
	if !strings.Contains(gap, "synthesis") {
		t.Errorf("the gap does not name the missing synthesis task: %q", gap)
	}
	if !strings.Contains(gap, "end condition") {
		t.Errorf("the gap does not mention the missing end conditions: %q", gap)
	}
	// It has to say what to do about it, or it is an observation the caller
	// cannot act on without guessing at the API.
	if !strings.Contains(gap, "narwhal_spawn") {
		t.Errorf("the gap does not say how to fill it: %q", gap)
	}
}

func TestAWellFormedGraphIsNotNagged(t *testing.T) {
	// A warning on every spawn is a warning nobody reads. A run with a
	// synthesis task and a check has the shape being asked for.
	addr := spawnFixture(t)
	out := postSpawn(t, addr, map[string]any{
		"cwd": t.TempDir(),
		"workers": []map[string]any{
			{"name": "a", "assignment": "look at a/", "check": "re-read two reported lines"},
			{"name": "b", "assignment": "look at b/"},
			{"name": "synthesis", "assignment": "integrate peer findings from the radio",
				"deps": []string{"a", "b"}, "model": "opus"},
		},
	})
	if gap, _ := out["graph_gap"].(string); gap != "" {
		t.Errorf("a well-formed graph was flagged: %q", gap)
	}
}

func TestASoloWorkerIsNotAskedForASynthesis(t *testing.T) {
	// One worker has nothing to reconcile with. Demanding a synthesis task
	// here is how a check earns itself a reputation for being noise.
	addr := spawnFixture(t)
	out := postSpawn(t, addr, map[string]any{
		"cwd":     t.TempDir(),
		"workers": []map[string]any{{"name": "solo", "assignment": "look at a/"}},
	})
	if gap, _ := out["graph_gap"].(string); gap != "" {
		t.Errorf("a single-worker run was flagged: %q", gap)
	}
}

func TestSynthesisIsReportedAsLaunchingNotWaiting(t *testing.T) {
	// The note read from the state field, and a synthesis task's state is
	// pending — but DispatchableTasks launches it anyway, ahead of its
	// deps, which is the entire reason it can hear its peers. Telling the
	// caller it is waiting describes the wrong thing.
	addr := spawnFixture(t)
	out := postSpawn(t, addr, map[string]any{
		"cwd": t.TempDir(),
		"workers": []map[string]any{
			{"name": "a", "assignment": "look at a/"},
			{"name": "synthesis", "assignment": "integrate peer findings", "deps": []string{"a"}},
		},
	})

	workers, _ := out["workers"].([]any)
	var note string
	for _, w := range workers {
		m, _ := w.(map[string]any)
		if id, _ := m["task_id"].(string); id == "synthesis" {
			note, _ = m["note"].(string)
		}
	}
	if note == "" {
		t.Fatalf("no note for the synthesis task: %v", workers)
	}
	if strings.Contains(note, "will launch when") {
		t.Errorf("the synthesis task was reported as queued behind its deps: %q", note)
	}
	if !strings.Contains(note, "task-done") {
		t.Errorf("the note does not say what the deps actually block: %q", note)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
