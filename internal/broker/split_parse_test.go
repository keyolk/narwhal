package broker

import (
	"reflect"
	"strings"
	"testing"
)

// SPLIT_REQUEST is a pipe-delimited line and the assignment is free text a
// worker wrote, so it can contain pipes. SplitN(rest, "|", 4) gives the
// assignment field three pipes' worth of room and hands everything past the
// fourth to deps.
//
// It happened on the second real split ever attempted. On run
// s1786800188852-1 the tail of a shell pipeline became task-5's dependency
// list:
//
//	deps=[" tar -x -C /tmp/head", " then symlink node_modules ...", ...]
//
// Nothing by those names exists, and recomputeReady returns early on a dep
// it cannot find, so the task sat pending across four daemon restarts while
// all four of its real deps were completed. 1 of 2 SPLIT_REQUESTs on disk is
// malformed this way.

func TestAPipeInTheAssignmentDoesNotBecomeADependency(t *testing.T) {
	assignment := "run `tar -c . | tar -x -C /tmp/head`, then compare"
	body := FormatSplitRequest("task-5", "fix-regression", assignment,
		[]string{"task-1", "task-2"})

	id, name, gotAssign, deps, ok := ParseSplitRequest(body)
	if !ok {
		t.Fatal("a well-formed request did not parse")
	}
	if id != "task-5" || name != "fix-regression" {
		t.Errorf("id/name came back as %q/%q", id, name)
	}
	if gotAssign != assignment {
		t.Errorf("the assignment was truncated at a pipe:\n  got  %q\n  want %q",
			gotAssign, assignment)
	}
	if want := []string{"task-1", "task-2"}; !reflect.DeepEqual(deps, want) {
		t.Errorf("deps picked up assignment text: %q", deps)
	}
}

func TestTheRealMalformedRequestParses(t *testing.T) {
	// Shortened from the message on disk, keeping the shape: the assignment
	// holds a shell pipeline and prose with commas.
	assignment := "FIX A REGRESSION. Build head: tar -c . | tar -x -C /tmp/head, " +
		"then symlink node_modules -- run it the same way, and compare."
	body := FormatSplitRequest("task-5", "fix-orphan-teardown", assignment, nil)

	_, _, gotAssign, deps, ok := ParseSplitRequest(body)
	if !ok {
		t.Fatal("did not parse")
	}
	if gotAssign != assignment {
		t.Errorf("assignment lost everything after the first pipe:\n  got %q", gotAssign)
	}
	if len(deps) != 0 {
		t.Errorf("a task with no deps was given %d: %q", len(deps), deps)
	}
}

func TestSplitRequestRoundTripsAwkwardText(t *testing.T) {
	for _, assignment := range []string{
		"plain text",
		"a | b",
		"a|b|c|d|e",
		"trailing pipe |",
		"| leading pipe",
		"commas, and | pipes, together",
		"newlines\nand | pipes",
	} {
		body := FormatSplitRequest("task-1", "n", assignment, []string{"task-0"})
		_, _, got, deps, ok := ParseSplitRequest(body)
		if !ok {
			t.Errorf("%q did not parse", assignment)
			continue
		}
		if got != assignment {
			t.Errorf("round trip changed the assignment:\n  got  %q\n  want %q",
				got, assignment)
		}
		if want := []string{"task-0"}; !reflect.DeepEqual(deps, want) {
			t.Errorf("deps for %q came back as %q", assignment, deps)
		}
	}
}

func TestADependencyThatCannotExistIsRejected(t *testing.T) {
	// The second half of the same failure. A dep naming a task that does not
	// exist is not a wait — recomputeReady returns early on it forever, so
	// the task is unreachable rather than pending. Names that could never be
	// a task id are refused at the door.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.AddTask("task-1", "n", "a", nil)

	body := FormatSplitRequest("task-2", "n", "do the thing",
		[]string{"task-1", " tar -x -C /tmp/head"})
	_, _, _, deps, ok := ParseSplitRequest(body)
	if !ok {
		t.Fatal("did not parse")
	}
	for _, d := range deps {
		if strings.ContainsAny(d, " /\n") {
			t.Errorf("a dep that cannot be a task id survived parsing: %q", d)
		}
	}
}
