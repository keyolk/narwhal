// history.go lifts finished runs back into the form a planner can read.
//
// Narwhal already records every run's graph — tasks, deps, per-task model,
// and what each task concluded — and until now nothing read it back at plan
// time. A planner decomposing a request saw the prompt and nothing else, so
// a decomposition that deadlocked was as likely to be produced again as one
// that worked. The 44 snapshots on disk were write-only.
//
// The digest is deliberately structural rather than prose: what the planner
// needs from a past run is the shape (which tasks, which deps, which model)
// and the verdict (what each task concluded, or that the run failed), not
// the transcript. Prose belongs in the export corpus, which kmd indexes.
package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// HistoryDigest renders past runs as a system-prompt section, or "" when
// there is nothing worth showing. The caller appends it; it is never fed
// through a format string, because a past prompt can contain percent verbs.
func HistoryDigest(runs []broker.Snapshot) string {
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Past runs on this repository\n\n")
	// Framing matters more here than anywhere else in the fragment. The
	// corpus contains the same prompt run five times, so a planner shown
	// these without a reason to judge them will copy the most recent one
	// verbatim — including the ones that deadlocked. The states and
	// outcomes below are what let it tell those apart.
	b.WriteString("How similar requests were decomposed before, and how each task ended.\n" +
		"Reuse structures that finished. This is evidence, not a template —\n" +
		"decompose the request you were actually given.\n\n")
	// A failure is only evidence about a decomposition if the harness that
	// ran it is the one running now. Nine of the ten runs on disk carrying
	// a failure signal predate the fixes in #24 and #32-#36, and one of
	// them blames a worker that did exactly what it was asked. Telling a
	// planner to avoid those shapes would teach it around bugs that are
	// already fixed. Only stamped runs get that instruction.
	if anyStamped(runs) {
		b.WriteString("Runs tagged with a build id ran on a comparable harness: a task that\n" +
			"failed or blocked there is a fact about the decomposition, so avoid\n" +
			"that shape. Untagged runs predate the current harness — read their\n" +
			"structure, but not their failures.\n\n")
	}
	for _, s := range runs {
		writeRunDigest(&b, s)
	}
	return b.String()
}

// anyStamped reports whether any run shown was produced by a build that
// recorded itself, which is what makes its failures attributable.
func anyStamped(runs []broker.Snapshot) bool {
	for _, s := range runs {
		if s.HarnessVersion != "" {
			return true
		}
	}
	return false
}

func writeRunDigest(b *strings.Builder, s broker.Snapshot) {
	fmt.Fprintf(b, "### %s — %s\n\n", s.RunID, headline(s.Prompt))
	if s.HarnessVersion != "" {
		fmt.Fprintf(b, "Run state: %s (build %s)\n\n", s.State, s.HarnessVersion)
	} else {
		fmt.Fprintf(b, "Run state: %s (untagged — older harness)\n\n", s.State)
	}

	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	for _, t := range tasks {
		line := "- " + t.ID
		if t.Name != "" && t.Name != t.ID {
			line += " (" + t.Name + ")"
		}
		if len(t.Deps) > 0 {
			line += " deps=[" + strings.Join(t.Deps, ",") + "]"
		}
		if t.Model != "" {
			line += " model=" + t.Model
		}
		line += " → " + string(t.State)
		b.WriteString(line + "\n")
		// One line of the outcome. The full text is in the export corpus;
		// what the planner needs is enough to tell a task that answered
		// something from one that ran and produced nothing.
		if o := firstLine(t.Outcome); o != "" {
			fmt.Fprintf(b, "  %s\n", clip(o, 160))
		}
	}
	b.WriteString("\n")
}

// RecentRunsFor returns up to limit finished runs to show a planner
// decomposing prompt in cwd, most relevant first.
//
// Relevance is same-cwd first, then overlap on prompt terms that are not
// common across the corpus. Plain overlap does not work here: measured on
// the 44 snapshots on disk, "the" appears in 27 prompts and "code" in 24,
// so any two prompts share a handful of words and everything looks related
// to everything. Dropping terms that occur in more than half the corpus
// leaves the words that actually name the work — the same query then puts
// the two prior audits of this repo at the top and scores 19 runs at zero.
//
// Runs still in flight are excluded — a graph whose tasks are all pending
// says nothing about what works — as are runs with nothing substantial in
// them, reusing the exporter's floor so the two views of the corpus agree.
func RecentRunsFor(cwd, prompt string, limit int) []broker.Snapshot {
	ids, err := ListRuns()
	if err != nil {
		return nil
	}
	type cand struct {
		snap broker.Snapshot
		toks map[string]bool
	}
	var cands []cand
	df := map[string]int{}
	for _, id := range ids {
		snap, err := LoadRun(id)
		if err != nil || snap.State == broker.RunActive || len(snap.Tasks) == 0 {
			continue
		}
		if !substantial(ExportMarkdown(snap)) {
			continue
		}
		t := tokens(snap.Prompt)
		for k := range t {
			df[k]++
		}
		cands = append(cands, cand{snap, t})
	}
	if len(cands) == 0 {
		return nil
	}
	common := len(cands) / 2
	distinctive := func(t map[string]bool) map[string]bool {
		out := map[string]bool{}
		for k := range t {
			if df[k] <= common {
				out[k] = true
			}
		}
		return out
	}
	want := distinctive(tokens(prompt))

	type scored struct {
		snap  broker.Snapshot
		score int
	}
	var ranked []scored
	for _, c := range cands {
		score := overlap(want, distinctive(c.toks))
		if cwd != "" && c.snap.CWD == cwd {
			// Same repository outranks any amount of wording similarity:
			// the graph shape a planner is copying is a fact about this
			// codebase, not about the phrasing of the request.
			score += 100
		}
		if score == 0 {
			continue
		}
		ranked = append(ranked, scored{c.snap, score})
	}
	// ListRuns is newest-first; a stable sort keeps that as the tiebreak.
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	var out []broker.Snapshot
	for _, r := range ranked {
		if len(out) >= limit {
			break
		}
		out = append(out, r.snap)
	}
	return out
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') && r < 0x80
	}) {
		if len(f) >= 3 {
			out[f] = true
		}
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

func clip(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
