package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Stopping the broker does not stop the workers: they are detached
// processes that keep going and then report to a closed port. A routine
// `make daemon-restart` in another terminal killed a four-worker run this
// way, and the workers' results were invisible even though their files were
// written.

func TestStopRefusesWhileWorkersRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[{"run_id":"r1","active_workers":3}]}`))
	}))
	defer srv.Close()

	n, err := activeWorkers(srv.URL)
	if err != nil {
		t.Fatalf("activeWorkers: %v", err)
	}
	if n != 3 {
		t.Fatalf("activeWorkers = %d, want 3", n)
	}
}

func TestActiveWorkersSumsAcrossRuns(t *testing.T) {
	// One interactive session routinely has several runs open, and any of
	// them having a worker is reason enough to refuse.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"runs":[{"active_workers":2},{"active_workers":0},{"active_workers":1}]}`))
	}))
	defer srv.Close()

	if n, err := activeWorkers(srv.URL); err != nil || n != 3 {
		t.Fatalf("activeWorkers = %d, %v; want 3, nil", n, err)
	}
}

func TestActiveWorkersIsZeroWhenIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	defer srv.Close()

	if n, err := activeWorkers(srv.URL); err != nil || n != 0 {
		t.Fatalf("activeWorkers = %d, %v; want 0, nil", n, err)
	}
}

func TestActiveWorkersReportsAnUnreachableBroker(t *testing.T) {
	// A daemon that cannot be asked must not be assumed idle: silently
	// stopping is the failure this check exists to prevent. The caller
	// turns the error into "use --force", which is a decision the user
	// makes rather than one the tool makes for them.
	if _, err := activeWorkers("http://127.0.0.1:1"); err == nil {
		t.Error("an unreachable broker reported no error")
	}
}

func TestStopWithNoDaemonSaysSo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := Stop(false)
	if err == nil {
		t.Fatal("stopping a daemon that is not running succeeded")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error does not say the daemon is not running: %v", err)
	}
}
