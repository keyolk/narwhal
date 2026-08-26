// backfill.go fills in accounting for runs that finished before it existed.
//
// The transcripts are still on disk — 109 of the 111 worker sessions this
// machine has recorded resolve to one — so the cost of every past run is
// recoverable rather than lost. Without this, accounting would only ever
// describe runs from today forward, and the corpus that makes a claim
// checkable at all is the backlog.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
	"github.com/keyolk/narwhal/internal/usage"
)

func backfillCmd(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "report what would change without writing")
	_ = fs.Parse(args)

	ids, err := store.ListRuns()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: list runs: %v\n", err)
		os.Exit(1)
	}

	probe := usage.TranscriptProbe{}
	var changedRuns, measuredTasks, missing int
	var total broker.Usage

	for _, id := range ids {
		s, err := store.LoadRun(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: unreadable: %v\n", id, err)
			continue
		}
		changed := false
		for i := range s.Tasks {
			t := &s.Tasks[i]
			if t.Usage != nil || t.Dispatches == 0 {
				continue
			}
			u := probe.TaskUsage(s.RunID, t.ID)
			if u == nil {
				missing++
				continue
			}
			t.Usage = u
			changed = true
			measuredTasks++
			total.OutputTokens += u.OutputTokens
			total.InputTokens += u.InputTokens
			total.CacheCreationTokens += u.CacheCreationTokens
			total.CacheReadTokens += u.CacheReadTokens
			total.Turns += u.Turns
		}
		if !changed {
			continue
		}
		changedRuns++
		if *dry {
			continue
		}
		if err := store.SaveRun(s); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: save: %v\n", id, err)
		}
	}

	verb := "measured"
	if *dry {
		verb = "would measure"
	}
	fmt.Printf("%s %d task(s) across %d run(s)\n", verb, measuredTasks, changedRuns)
	fmt.Printf("  out=%s in=%s cache=%s turns=%d\n",
		humanTokens(total.OutputTokens), humanTokens(total.InputTokens),
		humanTokens(total.CacheReadTokens+total.CacheCreationTokens), total.Turns)
	if missing > 0 {
		fmt.Printf("  %d dispatched task(s) have no readable transcript and stay unmeasured\n", missing)
	}
}
