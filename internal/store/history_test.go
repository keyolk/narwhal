package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The monitor only ever listed what was running this second — the batch
// registry plus the daemon's in-memory runs. Twenty-five snapshots sat in
// ~/.narwhal/runs readable by `narwhal show` and nothing else, and the
// daemon's own memory of retired runs died with the process. A monitor
// that forgets everything the moment a run finishes cannot answer "what
// did that run do", which is most of what you want a monitor for.

func saveRunFor(t *testing.T, id, prompt, cwd string) {
	t.Helper()
	if err := SaveRun(broker.Snapshot{
		RunID:     id,
		Prompt:    prompt,
		CWD:       cwd,
		State:     broker.RunDone,
		StartedAt: 1786600000,
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
}

func TestDiscoverIncludesFinishedRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveRunFor(t, "old-1", "audit the auth module", "/src/repo")

	runs := Discover(nil)
	if len(runs) != 1 {
		t.Fatalf("discovered %d runs, want the finished one", len(runs))
	}
	if runs[0].Prompt != "audit the auth module" {
		t.Errorf("prompt = %q", runs[0].Prompt)
	}
	if runs[0].CWD != "/src/repo" {
		t.Errorf("cwd = %q — a run is identified by where it worked", runs[0].CWD)
	}
	if runs[0].BrokerURL != "" {
		t.Error("a finished run must have no broker URL; there is nothing to poll")
	}
}

func TestALiveRunHidesItsFinishedCopy(t *testing.T) {
	// A run is persisted while it is still going, so the same id appears
	// in both places. The live entry wins — it has a broker to poll.
	home := t.TempDir()
	t.Setenv("HOME", home)
	saveRunFor(t, "r1", "still going", "/src/repo")

	if err := RegisterLive(LiveRun{
		RunID: "r1", PID: os.Getpid(), BrokerURL: "http://127.0.0.1:1",
		CWD: "/src/repo", Prompt: "still going",
	}); err != nil {
		t.Fatalf("RegisterLive: %v", err)
	}

	runs := Discover(nil)
	if len(runs) != 1 {
		t.Fatalf("discovered %d runs, want one — the live entry", len(runs))
	}
	if runs[0].BrokerURL == "" {
		t.Error("the finished copy shadowed the live run")
	}
}

func TestDaemonRunHidesItsFinishedCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveRunFor(t, "d1", "running now", "/src/repo")

	lister := func() ([]LiveRun, error) {
		return []LiveRun{{RunID: "d1", BrokerURL: "http://127.0.0.1:2",
			Prompt: "running now"}}, nil
	}

	runs := Discover(lister)
	if len(runs) != 1 {
		t.Fatalf("discovered %d runs, want one", len(runs))
	}
	if runs[0].BrokerURL == "" {
		t.Error("the finished copy shadowed the daemon's live run")
	}
}

func TestFinishedRunsAreCapped(t *testing.T) {
	// The directory grows without bound and the list is read at a glance.
	// A hundred rows of last month is not history, it is noise — the rest
	// stay on disk for `narwhal show`.
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < MaxFinishedRuns+10; i++ {
		saveRunFor(t, "run-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "x", "/src")
	}

	if got := len(Discover(nil)); got > MaxFinishedRuns {
		t.Fatalf("discovered %d runs, want at most %d", got, MaxFinishedRuns)
	}
}

func TestAnUnreadableSnapshotIsSkipped(t *testing.T) {
	// One corrupt file must not blank the whole list.
	home := t.TempDir()
	t.Setenv("HOME", home)
	saveRunFor(t, "good", "fine", "/src")

	bad := filepath.Join(home, ".narwhal", "runs", "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	runs := Discover(nil)
	if len(runs) != 1 || runs[0].RunID != "good" {
		t.Fatalf("discovered %v, want just the readable run", runs)
	}
}

func TestSnapshotCarriesCWDAndStartTime(t *testing.T) {
	// Without these a snapshot read back from disk cannot say where the
	// run happened or when — the two things that tell runs apart, since a
	// run id is only a timestamp.
	b := broker.New()
	run := b.CreateRun("r-fields", "do the thing", "/src/repo", "main")

	snap := run.Snapshot()
	if snap.CWD != "/src/repo" {
		t.Errorf("snapshot cwd = %q", snap.CWD)
	}
	if snap.StartedAt == 0 {
		t.Error("snapshot has no start time")
	}
}
