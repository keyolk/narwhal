package daemon

import (
	"testing"
)

func TestActiveRunsIsSorted(t *testing.T) {
	// Go randomizes map iteration, so an unsorted ActiveRuns makes the
	// monitor's run picker reshuffle on every poll.
	s := NewSession()
	for _, id := range []string{"s3", "s1", "s2", "s5", "s4"} {
		s.LauncherFor(id, t.TempDir())
	}

	want := []string{"s1", "s2", "s3", "s4", "s5"}
	// Repeat: a single call could match by luck.
	for i := 0; i < 20; i++ {
		got := s.ActiveRuns()
		if len(got) != len(want) {
			t.Fatalf("ActiveRuns = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("ActiveRuns = %v, want %v", got, want)
			}
		}
	}
}
