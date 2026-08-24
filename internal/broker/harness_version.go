// harness_version.go records which build of narwhal produced a run.
//
// A snapshot could say what the graph was and what each task concluded,
// but not what was running it. That gap makes the corpus's failure signal
// unreadable. Of the ten runs on disk carrying a retry, a failed task, or
// a stuck frontier, nine started before the harness fixes in #24 and
// #32–#36, and the tenth started 41 minutes after #36 merged with no way
// to tell whether the daemon serving it had been restarted onto that
// binary. s1787127043469-1 is the clearest case: its outcome blames the
// worker for exiting without calling task-done, and the transcript shows
// the worker emitted exactly what it was asked for.
//
// So "this decomposition failed" and "the harness of that week failed" are
// the same bytes on disk. Anything that scores past graphs to improve
// planning has to be able to draw that line, and the only durable way is
// for the run to record the build that ran it.
package broker

import "sync/atomic"

// harnessVersion is process-global because it is a property of the binary,
// not of any one run. Set once at startup from main's linker-injected
// version; every snapshot taken afterwards carries it.
//
// One setter on purpose. Threading a version through each call site is how
// the planner instructions ended up with two copies that drifted (#39).
var harnessVersion atomic.Pointer[string]

// SetHarnessVersion records the build identifier for every subsequent
// snapshot. Called once from main; safe to call from tests.
func SetHarnessVersion(v string) {
	harnessVersion.Store(&v)
}

// HarnessVersion returns the recorded build identifier, or "" if unset.
// Unset stays unset rather than falling back to a guess: "unknown build"
// and "this build" are exactly the distinction the field exists to make,
// and every run already on disk is the former.
func HarnessVersion() string {
	if p := harnessVersion.Load(); p != nil {
		return *p
	}
	return ""
}
