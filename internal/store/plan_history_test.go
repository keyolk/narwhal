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

// #39's digest tells the planner to avoid decompositions that failed or
// blocked. That instruction is wrong for every run already on disk: nine
// of the ten runs carrying a failure signal predate the harness fixes in
// #24 and #32–#36, and s1787127043469-1's outcome blames a worker that
// did exactly what it was asked. Those failures are facts about the
// harness of that week, not about how the request was decomposed.
//
// An unstamped run is exactly that population — the field did not exist
// yet — so its failures must not be offered as something to avoid.
func TestAnUnstampedFailureIsNotOfferedAsSomethingToAvoid(t *testing.T) {
	old := broker.Snapshot{
		RunID: "s900-1", Prompt: "final e2e split claim gate", CWD: "/repo",
		State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{
			{ID: "investigate", State: broker.TaskFailed, Dispatches: 3},
			{ID: "synthesis", Deps: []string{"investigate"},
				State: broker.TaskFailed, Dispatches: 3},
		},
	}
	got := HistoryDigest([]broker.Snapshot{old})
	// With nothing stamped, the digest must not carry an avoid-what-failed
	// instruction at all — there is no run here whose failure is
	// attributable to its decomposition.
	if strings.Contains(got, "avoid") {
		t.Errorf("an unstamped failure is presented as a decomposition to avoid:\n%s", got)
	}
	if !strings.Contains(got, "untagged") {
		t.Errorf("the digest does not mark the run as predating the current harness:\n%s", got)
	}
}

// A failure from a build that recorded itself is attributable, so the
// planner should be told to avoid it without qualification.
func TestAStampedFailureIsAttributed(t *testing.T) {
	recent := broker.Snapshot{
		RunID: "s901-1", Prompt: "audit the broker", CWD: "/repo",
		State: broker.RunFailed, HarnessVersion: "abc1234",
		Tasks: []broker.TaskSnapshot{
			{ID: "task-1", Deps: []string{"task-2"}, State: broker.TaskBlocked},
			{ID: "task-2", Deps: []string{"task-1"}, State: broker.TaskBlocked},
		},
	}
	got := HistoryDigest([]broker.Snapshot{recent})
	if !strings.Contains(got, "abc1234") {
		t.Error("a stamped run does not say which build produced it")
	}
}
