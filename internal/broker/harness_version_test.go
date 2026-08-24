package broker

import "testing"

// A snapshot cannot say which narwhal produced it. That matters because
// the failure signal in the corpus is not what it looks like: of the ten
// runs on disk carrying a retry, a failed task, or a stuck frontier, nine
// started before the harness fixes in #24 and #32–#36 and the tenth 41
// minutes after #36 merged — with no way to tell whether the daemon
// serving it had been restarted onto that binary.
//
// s1787127043469-1 is the exhibit: its outcome reads "worker exited
// without calling task-done" and the transcript shows the worker emitted
// exactly what it was asked for. That run's failure is a fact about the
// harness of 2026-08-19, not about how the request was decomposed.
//
// Anything that later scores past graphs to improve planning has to be
// able to draw that line, and the only durable way is for each run to
// record the build that ran it.
func TestASnapshotNamesTheHarnessThatProducedIt(t *testing.T) {
	b := New()
	SetHarnessVersion("abc1234")
	t.Cleanup(func() { SetHarnessVersion("") })

	r := b.CreateRun("r1", "a prompt", "/repo", "main")
	if got := r.Snapshot().HarnessVersion; got != "abc1234" {
		t.Errorf("snapshot does not name its harness: %q", got)
	}
}

// Every run already on disk predates the field. Absent must stay absent
// rather than become a version that is wrong, because "unknown build" and
// "this build" are the distinction the field exists to make.
func TestAnUnstampedRunStaysUnstamped(t *testing.T) {
	b := New()
	SetHarnessVersion("")
	r := b.CreateRun("r2", "a prompt", "/repo", "main")
	if got := r.Snapshot().HarnessVersion; got != "" {
		t.Errorf("an unset harness version invented one: %q", got)
	}
}
