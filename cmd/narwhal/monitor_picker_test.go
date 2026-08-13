package main

import (
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/store"
)

func TestAbbreviatePath(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	cases := []struct{ in, want string }{
		{"", "—"},
		{"/Users/x", "~"},
		{"/Users/x/src", "~/src"},
		{"/Users/x/src/keyolk/narwhal", ".../keyolk/narwhal"},
		{"/tmp", "/tmp"},
	}
	for _, c := range cases {
		if got := abbreviatePath(c.in); got != c.want {
			t.Errorf("abbreviatePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRunStartTimePrefersStartedAt(t *testing.T) {
	want := time.Now().Add(-time.Hour).Truncate(time.Second)
	got := runStartTime(store.LiveRun{RunID: "s1786472797321-1", StartedAt: want.Unix()})
	if !got.Equal(want) {
		t.Fatalf("runStartTime = %v, want %v", got, want)
	}
}

func TestRunStartTimeFallsBackToTheRunID(t *testing.T) {
	// Daemon runs carry no StartedAt, but every id ends in the millisecond
	// timestamp it was minted from. Showing the epoch instead would make
	// every daemon run look like 1970.
	for _, id := range []string{"s1786472797321-1", "plan-1786543427573", "run-1786593102332"} {
		got := runStartTime(store.LiveRun{RunID: id})
		if got.IsZero() {
			t.Errorf("runStartTime(%q) is zero; want the id's timestamp", id)
			continue
		}
		if got.Year() < 2020 || got.Year() > 2100 {
			t.Errorf("runStartTime(%q) = %v, outside a plausible range", id, got)
		}
	}
}

func TestRunStartTimeIsZeroForAnUnparseableID(t *testing.T) {
	if got := runStartTime(store.LiveRun{RunID: "handmade"}); !got.IsZero() {
		t.Fatalf("runStartTime = %v, want zero for an id with no timestamp", got)
	}
}
