package broker

import "testing"

func TestParseFileClaimRequest(t *testing.T) {
	cases := []struct {
		content string
		action  string
		taskID  string
		paths   []string
		ok      bool
	}{
		{"FILE_CLAIM|task-1|a.go,b.go", FileClaimPrefix, "task-1", []string{"a.go", "b.go"}, true},
		{"FILE_RELEASE|task-2|a.go", FileReleasePrefix, "task-2", []string{"a.go"}, true},
		{"FILE_CLAIM|task-3|", FileClaimPrefix, "task-3", nil, true},
		{"DEP_ADD|task-1|task-2", "", "", nil, false},
		{"FILE_CLAIMnopipe", "", "", nil, false},
	}
	for _, c := range cases {
		action, taskID, paths, ok := ParseFileClaimRequest(c.content)
		if ok != c.ok {
			t.Fatalf("ok = %v, want %v for %q", ok, c.ok, c.content)
		}
		if !ok {
			continue
		}
		if action != c.action || taskID != c.taskID {
			t.Errorf("got action=%q taskID=%q, want %q/%q for %q",
				action, taskID, c.action, c.taskID, c.content)
		}
		if !sliceEq(paths, c.paths) {
			t.Errorf("paths = %v, want %v for %q", paths, c.paths, c.content)
		}
	}
}

func TestFormatFileClaimRequest(t *testing.T) {
	got := FormatFileClaimRequest(FileClaimPrefix, "task-1", []string{"a.go", "b.go"})
	if want := "FILE_CLAIM|task-1|a.go,b.go"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// claimRun builds a run for claim tests.
func claimRun(t *testing.T) *Run {
	t.Helper()
	return New().CreateRun("r", "prompt", "/tmp", "main")
}

func TestClaimFilesGrantsUnheldPaths(t *testing.T) {
	run := claimRun(t)
	if conflicts := run.ClaimFiles("task-1", []string{"a.go", "b.go"}); conflicts != nil {
		t.Fatalf("unheld paths should not conflict, got %v", conflicts)
	}
	if owner := run.FileOwner("a.go"); owner != "task-1" {
		t.Fatalf("FileOwner = %q, want task-1", owner)
	}
}

func TestClaimFilesReportsConflictWithoutReassigning(t *testing.T) {
	// The second claimant must be told who holds the path — silently
	// dropping the claim would let it overwrite, which is the failure the
	// whole protocol exists to prevent.
	run := claimRun(t)
	run.ClaimFiles("task-1", []string{"shared.go"})

	conflicts := run.ClaimFiles("task-2", []string{"shared.go", "own.go"})
	if conflicts["shared.go"] != "task-1" {
		t.Fatalf("conflict for shared.go = %q, want task-1", conflicts["shared.go"])
	}
	if owner := run.FileOwner("shared.go"); owner != "task-1" {
		t.Fatalf("conflicting claim reassigned the path to %q", owner)
	}
	if owner := run.FileOwner("own.go"); owner != "task-2" {
		t.Fatalf("unconflicted path in the same claim was not granted: %q", owner)
	}
}

func TestReclaimingOwnPathIsNotAConflict(t *testing.T) {
	run := claimRun(t)
	run.ClaimFiles("task-1", []string{"a.go"})
	if conflicts := run.ClaimFiles("task-1", []string{"a.go"}); conflicts != nil {
		t.Fatalf("re-claiming an owned path should be a no-op, got %v", conflicts)
	}
}

func TestReleaseFilesOnlyReleasesOwnClaims(t *testing.T) {
	run := claimRun(t)
	run.ClaimFiles("task-1", []string{"a.go"})

	run.ReleaseFiles("task-2", []string{"a.go"}) // not the owner
	if owner := run.FileOwner("a.go"); owner != "task-1" {
		t.Fatalf("a non-owner released the claim: owner=%q", owner)
	}

	run.ReleaseFiles("task-1", []string{"a.go"})
	if owner := run.FileOwner("a.go"); owner != "" {
		t.Fatalf("owner did not release: %q", owner)
	}
}

func TestFileClaimsSnapshotIsACopy(t *testing.T) {
	run := claimRun(t)
	run.ClaimFiles("task-1", []string{"a.go"})
	snap := run.FileClaims()
	snap["a.go"] = "tampered"
	if owner := run.FileOwner("a.go"); owner != "task-1" {
		t.Fatalf("mutating the snapshot changed run state: %q", owner)
	}
}
