// usage_report.go renders what a run cost.
//
// The numbers exist as soon as a task finishes, and a number nothing
// prints is the same as no number — that was the state of the persisted
// run graph before #39 read it back at plan time. This is the read side.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/usage"
)

// usageLine is the one-line summary shown per run in a list.
//
// Output tokens lead because they are what the run produced and what
// scales with the model tier; cache reads dominate every total by an
// order of magnitude (1.14B against 110M input across the corpus on this
// machine) and would drown the number that varies if summed in.
func usageLine(s broker.Snapshot) string {
	total, measured, unmeasured := runUsage(s)
	if measured == 0 {
		return ""
	}
	line := fmt.Sprintf("out=%s turns=%d", humanTokens(total.OutputTokens), total.Turns)
	if unmeasured > 0 {
		// Named rather than folded in: a total that silently covers half
		// the graph reads as the whole cost of the run.
		line += fmt.Sprintf(" (%d task(s) unmeasured)", unmeasured)
	}
	return line
}

// runUsage totals a snapshot the way broker.Run.RunUsage totals a live
// run. Duplicated deliberately: this side reads a file that may have been
// written by any past build, so it must not require a live Run.
func runUsage(s broker.Snapshot) (total broker.Usage, measured, unmeasured int) {
	for _, t := range s.Tasks {
		if t.Usage == nil {
			if t.Dispatches > 0 {
				unmeasured++
			}
			continue
		}
		measured++
		total.InputTokens += t.Usage.InputTokens
		total.OutputTokens += t.Usage.OutputTokens
		total.CacheCreationTokens += t.Usage.CacheCreationTokens
		total.CacheReadTokens += t.Usage.CacheReadTokens
		total.Turns += t.Usage.Turns
	}
	return total, measured, unmeasured
}

// UsageReport renders the full per-task accounting for one run.
func UsageReport(s broker.Snapshot) string {
	total, measured, unmeasured := runUsage(s)
	if measured == 0 && unmeasured == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Usage\n")

	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	for _, t := range tasks {
		if t.Usage == nil {
			if t.Dispatches > 0 {
				// A dispatched task with no tally is a hole in the
				// accounting, not a free task. Saying so is the whole
				// reason absent and zero are different values here.
				fmt.Fprintf(&b, "  %-20s unmeasured (no transcript)\n", t.ID)
			}
			continue
		}
		fmt.Fprintf(&b, "  %-20s out=%-8s in=%-8s cache=%-8s turns=%-4d %s\n",
			t.ID,
			humanTokens(t.Usage.OutputTokens),
			humanTokens(t.Usage.InputTokens),
			humanTokens(t.Usage.CacheReadTokens+t.Usage.CacheCreationTokens),
			t.Usage.Turns,
			servedNote(t))
	}

	fmt.Fprintf(&b, "  %-20s out=%-8s in=%-8s cache=%-8s turns=%-4d\n",
		"TOTAL",
		humanTokens(total.OutputTokens),
		humanTokens(total.InputTokens),
		humanTokens(total.CacheReadTokens+total.CacheCreationTokens),
		total.Turns)
	if unmeasured > 0 {
		fmt.Fprintf(&b, "  %d dispatched task(s) could not be measured; the total is a floor.\n",
			unmeasured)
	}
	return b.String()
}

// servedNote says which model ran, and calls out the case where that is
// not the tier the task asked for.
//
// This is the finding the accounting exists to surface. Across the 93
// tasks on disk with both a requested tier and a transcript, 13 named a
// tier explicitly and 5 of those were served by the other model — ccproxy
// routes on account and quota, not on what narwhal asked for. A run whose
// workers were meant to be cheap and were served by a frontier model
// looks identical to one that worked, unless something says so.
func servedNote(t broker.TaskSnapshot) string {
	served := t.Usage.ServedModel
	if served == "" {
		return ""
	}
	note := served
	if len(t.Usage.ServedModels) > 1 {
		note = strings.Join(t.Usage.ServedModels, "+")
	}
	if want := t.Model; want != "" {
		if got := usage.Tier(served); got != "" && got != want {
			return note + fmt.Sprintf("  ← asked for %s", want)
		}
	}
	return note
}

// humanTokens renders a token count compactly. Counts here span six
// orders of magnitude — a task can be 131 output tokens or 6M cache reads
// — and a column of raw integers is unreadable at that spread.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
