// guard.go stops a test that forgot to isolate HOME from writing into the
// developer's real store.
//
// Every store test is expected to call t.Setenv("HOME", t.TempDir()) first.
// That is a rule the writer has to remember, and forgetting it fails
// silently: the snapshot lands in ~/.narwhal/runs and shows up in
// `narwhal show` and the monitor's run picker beside real work. One did —
// a run called "r1", prompt "p", cwd /var/folders/.../TestARunWhose... —
// and sat there for days because nothing was wrong from the test's point
// of view.
//
// The store cannot tell a test from a run. It can tell that the process
// writing is a test binary and that HOME was left pointing at a real home,
// and that combination is always a mistake.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// underTest reports whether this process is a `go test` binary.
//
// Detected from the executable path rather than a build tag: the test
// binary is compiled into the build cache or a temp dir and named for its
// package, and no installed narwhal binary looks like that. A build tag
// would need every caller to be built twice.
func underTest() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	base := filepath.Base(exe)
	if strings.HasSuffix(base, ".test") {
		return true
	}
	// `go test` without -c runs from a temp build directory.
	return strings.Contains(exe, "/go-build") && !strings.HasSuffix(base, "narwhal")
}

// checkTestIsolation returns an error when a test binary is about to write
// into a home directory that is not a temporary one.
//
// It refuses rather than warning, and the refusal is survivable: every
// caller of SaveRun already handles a write error by logging it and
// carrying on.
//
// One case is genuinely racy and worth naming. A dispatcher tick runs on
// its own goroutine, and t.Setenv only unwinds when the test's own stack
// returns — so a tick that fires in that window sees the real HOME and is
// refused. That refusal is correct: the write really was about to land in
// the developer's store. It is also why a test that asserts on such a save
// must stop its dispatcher before returning, which is what Stop() is for.
func checkTestIsolation(dir string) error {
	if !underTest() {
		return nil
	}
	tmp := os.TempDir()
	if strings.HasPrefix(dir, tmp) {
		return nil
	}
	// macOS resolves /var to /private/var and TMPDIR to either form, so
	// compare both.
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil &&
		strings.HasPrefix(dir, resolved) {
		return nil
	}
	if strings.HasPrefix(dir, "/tmp") || strings.HasPrefix(dir, "/private/") {
		return nil
	}
	return fmt.Errorf("refusing to write to %s from a test: "+
		"call t.Setenv(\"HOME\", t.TempDir()) first, or this lands in the "+
		"developer's real store and shows up in narwhal show", dir)
}
