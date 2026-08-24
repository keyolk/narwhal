package store

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Selection tests for RecentRunsFor — what a planner is and is not shown as
// precedent. history_test.go covers Discover; this file covers the planner
// lookup that reads the same snapshots.

func writeSnap(t *testing.T, s broker.Snapshot) {
	t.Helper()
	if err := SaveRun(s); err != nil {
		t.Fatal(err)
	}
}

// A snapshot has to clear the export floor to be shown, so give it enough
// body to be a real run rather than a probe.
func bulky(id, prompt, cwd string, state broker.RunState) broker.Snapshot {
	return broker.Snapshot{
		RunID: id, Prompt: prompt, CWD: cwd, State: state,
		Tasks: []broker.TaskSnapshot{{
			ID: "task-1", Name: "work", State: broker.TaskCompleted, Model: "haiku",
			Assignment: strings.Repeat("investigate the subsystem carefully. ", 20),
			Outcome:    strings.Repeat("found something worth reporting. ", 20),
		}},
	}
}

func TestARunInFlightIsNotOfferedAsPrecedent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeSnap(t, bulky("s1", "audit the broker for races", "/repo", broker.RunActive))
	if got := RecentRunsFor("/repo", "audit the broker for races", 2); len(got) != 0 {
		t.Errorf("an active run was offered as precedent: %d", len(got))
	}
}

func TestAProbeIsNotOfferedAsPrecedent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeSnap(t, broker.Snapshot{
		RunID: "s2", Prompt: "outcome probe", CWD: "/repo", State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{{ID: "probe", State: broker.TaskFailed}},
	})
	if got := RecentRunsFor("/repo", "outcome probe", 2); len(got) != 0 {
		t.Errorf("a probe was offered as precedent: %d", len(got))
	}
}

func TestTheSameRepositoryOutranksWording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Elsewhere, but worded almost identically.
	writeSnap(t, bulky("s3", "audit the broker for races", "/other", broker.RunDone))
	// Here, and worded differently.
	writeSnap(t, bulky("s4", "check dispatch reaping", "/repo", broker.RunDone))
	got := RecentRunsFor("/repo", "audit the broker for races", 1)
	if len(got) != 1 || got[0].RunID != "s4" {
		t.Errorf("same-cwd run did not win: %+v", got)
	}
}

func TestTheLimitIsHonored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"s5", "s6", "s7"} {
		writeSnap(t, bulky(id, "audit the broker", "/repo", broker.RunDone))
	}
	if got := RecentRunsFor("/repo", "audit the broker", 2); len(got) != 2 {
		t.Errorf("limit ignored: %d", len(got))
	}
}

// An unrelated run in a different repository is noise, not precedent.
func TestAnUnrelatedRunElsewhereIsNotOffered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeSnap(t, bulky("s8", "rewrite the css grid", "/elsewhere", broker.RunDone))
	if got := RecentRunsFor("/repo", "audit the broker for races", 2); len(got) != 0 {
		t.Errorf("an unrelated run was offered: %+v", got)
	}
}
