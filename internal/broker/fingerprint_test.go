package broker

import "testing"

// A run id is a timestamp, so nothing could notice the system was being
// asked to do what it was already doing. A daemon restart orphaned a run
// and the same request arrived four minutes later; the two rows in the
// picker were indistinguishable because they were the same request.

func TestTheSameRequestHasTheSameFingerprint(t *testing.T) {
	a := Fingerprint("/src/repo", "audit the auth module")
	b := Fingerprint("/src/repo", "audit the auth module")
	if a != b {
		t.Fatalf("the same request hashed differently: %s vs %s", a, b)
	}
}

func TestDifferentRequestsDiffer(t *testing.T) {
	same := Fingerprint("/src/repo", "audit the auth module")
	for _, tc := range []struct{ cwd, prompt string }{
		{"/src/other", "audit the auth module"},
		{"/src/repo", "audit the billing module"},
	} {
		if got := Fingerprint(tc.cwd, tc.prompt); got == same {
			t.Errorf("%s + %q collided with a different request", tc.cwd, tc.prompt)
		}
	}
}

func TestWhitespaceAndCaseDoNotMakeANewRequest(t *testing.T) {
	// Prompts are pasted and re-typed. A newline or a capital is not a
	// different question, and treating it as one is how this feature
	// fails silently — it stops matching and nobody notices.
	base := Fingerprint("/src/repo", "audit the auth module")
	for _, p := range []string{
		"audit the  auth module",
		"audit the\nauth module",
		"  audit the auth module  ",
		"Audit The Auth Module",
	} {
		if got := Fingerprint("/src/repo", p); got != base {
			t.Errorf("%q read as a different request", p)
		}
	}
}

func TestATrailingSlashIsTheSameDirectory(t *testing.T) {
	if Fingerprint("/src/repo/", "x") != Fingerprint("/src/repo", "x") {
		t.Error("a trailing slash made a new request")
	}
}

func TestPastedBoilerplateDoesNotDefeatTheHash(t *testing.T) {
	// The scratch path in an uploaded-files preamble is different on
	// every submission, so hashing the raw prompt would give the same
	// request a fresh fingerprint every time.
	first := "<uploaded_files> /private/tmp/claude-502/-Users-x/scratchpad/a/kitty_1.0 " +
		"</uploaded_files> I've uploaded a code repository in the directory " +
		"/private/tmp/claude-502/-Users-x/scratchpad/a/kitty_1.0. " +
		"Consider the following question: how does the parser work?"
	second := "<uploaded_files> /private/tmp/claude-502/-Users-x/scratchpad/b/kitty_1.0 " +
		"</uploaded_files> I've uploaded a code repository in the directory " +
		"/private/tmp/claude-502/-Users-x/scratchpad/b/kitty_1.0. " +
		"Consider the following question: how does the parser work?"

	if Fingerprint("/src/repo", first) != Fingerprint("/src/repo", second) {
		t.Error("the same question under two scratch paths hashed differently")
	}
}

func TestADuplicateInFlightIsReported(t *testing.T) {
	b := New()
	b.CreateRun("r1", "audit the auth module", "/src/repo", "main")

	if got := b.DuplicateOf("/src/repo", "audit the auth module"); got != "r1" {
		t.Fatalf("DuplicateOf = %q, want r1", got)
	}
}

func TestAnOrphanedRunIsStillADuplicate(t *testing.T) {
	// This is the case that motivated it. The run's daemon died, its
	// state on disk says active, and asking again should say so — its
	// results may already be recoverable.
	b := New()
	r := b.CreateRun("r1", "audit the auth module", "/src/repo", "main")
	r.SetState(RunActive)

	if got := b.DuplicateOf("/src/repo", "audit the auth module"); got != "r1" {
		t.Fatalf("an orphaned run was not reported as a duplicate: %q", got)
	}
}

func TestAFinishedRunIsNotADuplicate(t *testing.T) {
	// Re-running something that finished is the normal way to work — the
	// code changed, or you want to see it again. Warning about it would
	// train the reader to ignore the warning.
	b := New()
	for _, st := range []RunState{RunDone, RunFailed, RunCanceled} {
		r := b.CreateRun("r-"+string(st), "audit the auth module", "/src/repo", "main")
		r.SetState(st)
	}

	if got := b.DuplicateOf("/src/repo", "audit the auth module"); got != "" {
		t.Errorf("a finished run was reported as a duplicate: %q", got)
	}
}

func TestADifferentRequestIsNotADuplicate(t *testing.T) {
	b := New()
	b.CreateRun("r1", "audit the auth module", "/src/repo", "main")

	if got := b.DuplicateOf("/src/repo", "rewrite the scheduler"); got != "" {
		t.Errorf("an unrelated request matched r1: %q", got)
	}
	if got := b.DuplicateOf("/src/elsewhere", "audit the auth module"); got != "" {
		t.Errorf("the same prompt in another directory matched: %q", got)
	}
}

func TestAHumanRetypingIsNotCaught(t *testing.T) {
	// Documenting the limit, because a reader who assumes this catches
	// every duplicate will trust it in the case where it does not.
	//
	// These two are the real pair: the same EKS request resubmitted four
	// minutes after a restart orphaned the first. One comma moved.
	first := "EKS 네이티브 kubeSchedulerConfig(2026-08-12 GA) 위에서, Sendbird mesg 클러스터의 비용 효율화"
	second := "EKS 네이티브 kubeSchedulerConfig(2026-08-12 GA) 위에서 Sendbird mesg 클러스터의 비용 효율화"

	if Fingerprint("/src/x", first) == Fingerprint("/src/x", second) {
		t.Skip("normalization got stronger — reconsider whether it now over-matches")
	}
	// Not a failure. Normalising hard enough to match these would make
	// unrelated requests collide, and a confident warning pointing at the
	// wrong run is worse than no warning.
}
