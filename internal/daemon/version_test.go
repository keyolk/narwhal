package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// A daemon outlives installs by design, so the two versions drift and
// nothing said so. Run s1787888345056-2 was served by a daemon four days
// older than the installed binary and recorded no token accounting,
// because the code that measures it had been installed but was not what
// was running.
func TestAnOlderDaemonIsStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteVersion("0c38ff2"); err != nil {
		t.Fatalf("WriteVersion: %v", err)
	}
	stale, running := Stale("d398125")
	if !stale {
		t.Error("a daemon on an older build is not reported as stale")
	}
	if running != "0c38ff2" {
		t.Errorf("running = %q, want the build the daemon recorded", running)
	}
}

func TestAMatchingDaemonIsNotStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = WriteVersion("d398125")
	if stale, _ := Stale("d398125"); stale {
		t.Error("a daemon on the same build is reported as stale")
	}
}

// A daemon that recorded no version predates the field, so it is older
// than anything asking.
func TestAnUnstampedDaemonIsStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stale, running := Stale("d398125")
	if !stale {
		t.Error("a daemon that recorded no version is not reported as stale")
	}
	if running != "" {
		t.Errorf("running = %q, want empty", running)
	}
}

// An unstamped local build cannot be compared against anything. Calling
// those stale would train the reader to ignore the warning.
func TestALocalBuildDoesNotReportStaleness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = WriteVersion("d398125")
	for _, current := range []string{"", "dev"} {
		if stale, _ := Stale(current); stale {
			t.Errorf("current=%q reported the daemon as stale", current)
		}
	}
}

// A version file left by a dead daemon would answer for the next one.
func TestClearStateRemovesTheVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = WriteVersion("0c38ff2")
	ClearState()
	if got := RunningVersion(); got != "" {
		t.Errorf("RunningVersion = %q after ClearState, want empty", got)
	}
}

// The status output has to name the mismatch and what to do about it —
// two version strings and no verdict is what the reader has to decode.
func TestStatusNamesAStaleDaemonAndWhatToDo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = WriteVersion("0c38ff2")
	// No live daemon in a temp HOME, so Status fails and the payload is
	// the not-running one. That path must not claim staleness.
	out, _ := StatusJSONFor("d398125")
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if got["running"] != false {
		t.Fatalf("running = %v, want false with no daemon up", got["running"])
	}
	if _, ok := got["stale"]; ok {
		t.Error("a daemon that is not running was reported as stale")
	}
}

// The staleness payload itself, with a daemon that looks live. Status
// needs the lock held and a url file, which is what a real daemon
// publishes at startup.
func TestTheStalePayloadCarriesTheVerdictAndTheFix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock, err := AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lock.Close()
	if err := WriteState(lock, 4242, "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	_ = WriteVersion("0c38ff2")

	out, _ := StatusJSONFor("d398125")
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if got["running"] != true {
		t.Fatalf("running = %v, want true", got["running"])
	}
	if got["stale"] != true {
		t.Errorf("stale = %v, want true — the daemon is on an older build", got["stale"])
	}
	if got["version"] != "0c38ff2" {
		t.Errorf("version = %v, want the build the daemon is on", got["version"])
	}
	hint, _ := got["hint"].(string)
	if hint == "" {
		t.Fatal("no hint; two version strings and no verdict is what the reader has to decode")
	}
	for _, want := range []string{"0c38ff2", "d398125", "daemon-restart"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint does not mention %q: %s", want, hint)
		}
	}
}

// A daemon on the same build must not carry the fields at all — a status
// that always mentions staleness is one nobody reads.
func TestAMatchingDaemonCarriesNoStaleFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock, err := AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer lock.Close()
	_ = WriteState(lock, 4242, "http://127.0.0.1:9999")
	_ = WriteVersion("d398125")

	out, _ := StatusJSONFor("d398125")
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["stale"]; ok {
		t.Error("a current daemon carries a stale flag")
	}
	if _, ok := got["hint"]; ok {
		t.Error("a current daemon carries a restart hint")
	}
	if got["version"] != "d398125" {
		t.Errorf("version = %v, want it reported even when current", got["version"])
	}
}
