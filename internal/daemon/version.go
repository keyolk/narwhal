// version.go records which build of narwhal the running daemon is, so a
// stale one is visible before it produces a run rather than afterwards.
//
// The daemon outlives installs by design — that is the point of it — and
// nothing connected the two. A daemon started on Aug 24 served every run
// through Aug 28 while `make install` replaced the binary underneath it,
// and `narwhal daemon status` reported pid and url and said nothing about
// the mismatch. The first evidence was a finished run stamped with the
// older build: run s1787888345056-2 carried harness_version 0c38ff2 and
// recorded no token accounting at all, because the code that measures it
// had been installed but was not what was running.
//
// #40 gave runs a build stamp for exactly this reason, and reading it off
// a snapshot is too late — the run is over. This puts the same fact where
// it can be acted on.
package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

func versionFilePath() string { return filepath.Join(StateDir(), "daemon.version") }

// WriteVersion records the build the daemon is running, alongside its pid
// and url. Called once at startup.
//
// A write failure is returned rather than ignored: a missing version file
// is indistinguishable from a daemon too old to write one, and silently
// producing that state is how the gap this closes stayed invisible.
func WriteVersion(version string) error {
	return writeFileAtomic(versionFilePath(), []byte(version), 0o600)
}

// RunningVersion returns the build the daemon reported at startup, or ""
// when it wrote none — which means a daemon predating this file, and is
// itself a stale daemon.
func RunningVersion() string {
	data, err := os.ReadFile(versionFilePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Stale reports whether the running daemon is a different build from the
// binary asking, and returns both for the caller to name.
//
// An empty `current` means an unstamped local build (`go run`, `go
// build` with no ldflags), which cannot be compared against anything —
// those are not stale, they are unknown, and reporting them as stale
// would train the reader to ignore the warning.
func Stale(current string) (stale bool, running string) {
	running = RunningVersion()
	if current == "" || current == "dev" {
		return false, running
	}
	if running == "" {
		// A daemon that records no version predates the field, so it is
		// by definition older than the binary asking.
		return true, ""
	}
	return running != current, running
}
