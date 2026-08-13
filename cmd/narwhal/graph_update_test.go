package main

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The coordinator's tests prove split-request and DEP_ADD mutate the run.
// What was never checked is the other half: that the monitor redraws the
// graph from the mutated snapshot. The TUI holds no graph state of its own
// — every frame is laid out from m.snap — but "should be fine by
// construction" is how caching creeps in unnoticed, so pin it down.

func graphSnapshot(m tuiModel) []string {
	rows := m.boxRows(52)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.text
	}
	return out
}

func TestGraphRedrawsWhenATaskIsAddedMidRun(t *testing.T) {
	m := testModel(0, 0)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskDispatched},
	}
	before := graphSnapshot(m)

	// A split-request lands: the coordinator creates the task, the next
	// poll carries it.
	m.snap.Tasks = append(m.snap.Tasks,
		broker.TaskSnapshot{ID: "discovered", State: broker.TaskReady})
	after := graphSnapshot(m)

	// Not "more lines": an independent task joins the same layer and sits
	// beside its sibling, so the graph grows sideways.
	if sameLines(before, after) {
		t.Fatalf("graph did not change for the new task:\n%v", after)
	}
	found := false
	for _, l := range after {
		if contains(l, "discovered") {
			found = true
		}
	}
	if !found {
		t.Fatalf("new task is not drawn:\n%v", after)
	}
}

// sameLines reports whether two rendered graphs are identical.
func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGraphDrawsEdgesAddedMidRun(t *testing.T) {
	// DEP_ADD is the interesting case: the task set does not change, only
	// the edges. A flat layer becomes two layers with a connector between.
	m := testModel(0, 0)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskCompleted},
		{ID: "beta", State: broker.TaskReady},
	}
	if layers := distinctLayers(m); layers != 1 {
		t.Fatalf("independent tasks should share one layer, got %d", layers)
	}

	// beta discovers it needs alpha's result first.
	m.snap.Tasks[1].Deps = []string{"alpha"}
	if layers := distinctLayers(m); layers != 2 {
		t.Fatalf("after DEP_ADD beta should sit below alpha, layers = %d", layers)
	}

	// And the connector is actually drawn, not just implied by position.
	var connectors int
	for _, l := range graphSnapshot(m) {
		for _, r := range l {
			if r == '│' || r == '┬' || r == '┴' {
				connectors++
			}
		}
	}
	if connectors == 0 {
		t.Fatalf("no edge glyphs drawn between alpha and beta:\n%v", graphSnapshot(m))
	}
}

func TestGraphDropsEdgesRemovedMidRun(t *testing.T) {
	m := testModel(0, 0)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskCompleted},
		{ID: "beta", State: broker.TaskReady, Deps: []string{"alpha"}},
	}
	if layers := distinctLayers(m); layers != 2 {
		t.Fatalf("setup: want 2 layers, got %d", layers)
	}
	m.snap.Tasks[1].Deps = nil
	if layers := distinctLayers(m); layers != 1 {
		t.Fatalf("after DEP_REMOVE the tasks should share a layer, got %d", layers)
	}
}

func TestCursorSurvivesATaskAppearing(t *testing.T) {
	// A task added mid-run must not drag the cursor off what the user is
	// looking at. The graph is sorted by id, so a new task can land above
	// the selection and shift its index.
	m := testModel(0, 0)
	m.focus = focusTasks
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "beta", State: broker.TaskDispatched},
		{ID: "gamma", State: broker.TaskDispatched},
	}
	m.taskCur = 1
	selected, _ := m.selectedTask()
	if selected.ID != "gamma" {
		t.Fatalf("setup: cursor on %q, want gamma", selected.ID)
	}

	// "alpha" sorts first, pushing gamma from index 1 to index 2. Go
	// through the poll path, which is where the cursor is restored.
	next := m.snap
	next.Tasks = append(append([]broker.TaskSnapshot(nil), m.snap.Tasks...),
		broker.TaskSnapshot{ID: "alpha", State: broker.TaskReady})
	updated, _ := m.Update(snapshotMsg{snap: next})
	m = updated.(tuiModel)

	after, _ := m.selectedTask()
	if after.ID != "gamma" {
		t.Errorf("a task appearing moved the cursor from gamma to %q", after.ID)
	}
}

func TestCursorSurvivesATaskDisappearing(t *testing.T) {
	// Tasks do not normally vanish, but a run switch or a broker restart
	// can hand the monitor a snapshot without the selected task. The
	// cursor must land somewhere valid rather than off the end.
	m := testModel(0, 0)
	m.focus = focusTasks
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskCompleted},
		{ID: "beta", State: broker.TaskCompleted},
		{ID: "gamma", State: broker.TaskCompleted},
	}
	m.taskCur = 2

	next := m.snap
	next.Tasks = []broker.TaskSnapshot{{ID: "alpha", State: broker.TaskCompleted}}
	updated, _ := m.Update(snapshotMsg{snap: next})
	m = updated.(tuiModel)

	if m.taskCur < 0 || m.taskCur >= len(m.snap.Tasks) {
		t.Fatalf("taskCur = %d is out of range for %d tasks", m.taskCur, len(m.snap.Tasks))
	}
}

// distinctLayers counts how many dependency layers the current snapshot
// lays out into.
func distinctLayers(m tuiModel) int {
	seen := map[int]bool{}
	for _, n := range layoutGraph(m.sortedTasks()).nodes {
		seen[n.layer] = true
	}
	return len(seen)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
