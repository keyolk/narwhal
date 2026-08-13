package broker

import (
	"testing"
)

func TestParseDepEdgeRequest(t *testing.T) {
	cases := []struct {
		content string
		action  string
		taskID  string
		deps    []string
		ok      bool
	}{
		{"DEP_ADD|task-3|task-1,task-2", DepAddPrefix, "task-3", []string{"task-1", "task-2"}, true},
		{"DEP_REMOVE|task-5|task-4", DepRemovePrefix, "task-5", []string{"task-4"}, true},
		{"DEP_ADD|solo|", DepAddPrefix, "solo", nil, true},
		{"SPLIT_REQUEST|task-1|name|assignment|", "", "", nil, false}, // wrong prefix
		{"DEP_ADDnopipe", "", "", nil, false},                         // no separator after prefix
	}
	for _, c := range cases {
		action, taskID, deps, ok := ParseDepEdgeRequest(c.content)
		if ok != c.ok {
			t.Fatalf("ok = %v, want %v for %q", ok, c.ok, c.content)
		}
		if !ok {
			continue
		}
		if action != c.action {
			t.Errorf("action = %q, want %q for %q", action, c.action, c.content)
		}
		if taskID != c.taskID {
			t.Errorf("taskID = %q, want %q for %q", taskID, c.taskID, c.content)
		}
		if !sliceEq(deps, c.deps) {
			t.Errorf("deps = %v, want %v for %q", deps, c.deps, c.content)
		}
	}
}

func sliceEq(a, b []string) bool {
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

func TestFormatDepEdgeRequest(t *testing.T) {
	got := FormatDepEdgeRequest(DepAddPrefix, "task-3", []string{"task-1", "task-2"})
	want := "DEP_ADD|task-3|task-1,task-2"
	if got != want {
		t.Fatalf("FormatDepEdgeRequest add = %q, want %q", got, want)
	}
	got = FormatDepEdgeRequest(DepRemovePrefix, "task-5", []string{"task-4"})
	want = "DEP_REMOVE|task-5|task-4"
	if got != want {
		t.Fatalf("FormatDepEdgeRequest remove = %q, want %q", got, want)
	}
}

func TestTaskAddDep(t *testing.T) {
	r := New()
	run := r.CreateRun("test", "prompt", "/tmp", "main")
	task := run.AddTask("task-1", "one", "do thing", nil)
	task.AddDep([]string{"task-2", "task-3"}, run)
	if !contains(task.Deps, "task-2") || !contains(task.Deps, "task-3") {
		t.Fatalf("AddDep did not append: deps=%v", task.Deps)
	}
}

func TestTaskRemoveDep(t *testing.T) {
	r := New()
	run := r.CreateRun("test", "prompt", "/tmp", "main")
	task := run.AddTask("task-1", "one", "do thing", []string{"task-2", "task-3", "task-4"})
	task.RemoveDep([]string{"task-2", "task-4"})
	if contains(task.Deps, "task-2") || contains(task.Deps, "task-4") {
		t.Fatalf("RemoveDep did not remove: deps=%v", task.Deps)
	}
	if !contains(task.Deps, "task-3") {
		t.Fatalf("RemoveDep removed wrong dep: deps=%v", task.Deps)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
