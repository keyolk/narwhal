// export.go renders a persisted run as markdown, so past runs can be
// found by the search index over this machine's notes.
//
// A run's record is JSON under ~/.narwhal/runs and nothing but narwhal
// reads it. The index takes directories of markdown files — that is the
// interface, and it is a file format rather than a protocol, which is why
// this is an exporter and not an integration. Point a collection at the
// output directory and past multi-agent runs join the same corpus as the
// wiki and the operational notes.
//
// There was nothing worth indexing until the outcome reached the snapshot:
// before that every run on disk recorded what each task was asked and
// nothing about what it answered. The outcomes are what someone searches
// for months later — "did we ever measure the SAN coverage" is answered by
// a task's conclusion, not by its assignment.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// ExportMarkdown renders one run.
//
// The first line is an `# H1`, because that is where the indexer takes a
// document's title from and the title is what a search result shows. The
// run id alone would be a timestamp, so the prompt goes in the heading.
func ExportMarkdown(s broker.Snapshot) string {
	var b strings.Builder

	title := headline(s.Prompt)
	if title == "" {
		title = "run " + s.RunID
	}
	fmt.Fprintf(&b, "# %s\n\n", title)

	fmt.Fprintf(&b, "Run: `%s`  ", s.RunID)
	fmt.Fprintf(&b, "State: %s  ", s.State)
	if s.CWD != "" {
		fmt.Fprintf(&b, "CWD: `%s`  ", s.CWD)
	}
	fmt.Fprintf(&b, "Tasks: %d\n\n", len(s.Tasks))

	if strings.TrimSpace(s.Prompt) != "" && headline(s.Prompt) != s.Prompt {
		// A prompt that runs to several lines was truncated in the
		// heading; the whole thing is what someone would search.
		fmt.Fprintf(&b, "## Prompt\n\n%s\n\n", strings.TrimSpace(s.Prompt))
	}

	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	// The run's own answer, first. A synthesis task read every peer's
	// findings before writing its outcome — 26 of the 40 runs on disk have
	// one — so the distilled version of the run already exists and only
	// needs to be put where a reader and a search snippet will meet it.
	if f := synthesisOutcome(tasks); f != "" {
		fmt.Fprintf(&b, "## Findings\n\n%s\n\n", f)
	}

	if len(tasks) > 0 {
		b.WriteString("## Tasks\n\n")
		for _, t := range tasks {
			writeTask(&b, t)
		}
	}

	// The radio is where workers told each other what they found, and it
	// holds observations that never made it into any one task's outcome.
	if len(s.Messages) > 0 {
		b.WriteString("## Radio\n\n")
		for _, m := range s.Messages {
			if m == nil || strings.TrimSpace(m.Content) == "" {
				continue
			}
			if isProtocol(m.Content) {
				continue
			}
			fmt.Fprintf(&b, "**%s** (%s): %s\n\n",
				m.Sender, m.ThreadID, strings.TrimSpace(m.Content))
		}
	}
	return b.String()
}

func writeTask(b *strings.Builder, t broker.TaskSnapshot) {
	name := t.ID
	if t.Name != "" && t.Name != t.ID {
		name = fmt.Sprintf("%s — %s", t.ID, t.Name)
	}
	fmt.Fprintf(b, "### %s (%s)\n\n", name, t.State)

	if t.Model != "" {
		fmt.Fprintf(b, "Model: %s  ", t.Model)
	}
	if len(t.Deps) > 0 {
		fmt.Fprintf(b, "Depends on: %s  ", strings.Join(t.Deps, ", "))
	}
	if t.Model != "" || len(t.Deps) > 0 {
		b.WriteString("\n\n")
	}

	if a := strings.TrimSpace(t.Assignment); a != "" {
		fmt.Fprintf(b, "%s\n\n", a)
	}
	// The end condition and what it showed, before the outcome — reading
	// the answer is different once you know it was tested, and reading it
	// is different again when the check was asked and never answered.
	if c := strings.TrimSpace(t.Check); c != "" {
		fmt.Fprintf(b, "**Check:** %s\n\n", c)
		if r := strings.TrimSpace(t.CheckResult); r != "" {
			fmt.Fprintf(b, "**Check result:** %s\n\n", r)
		} else {
			// Named rather than omitted. A check with no result is a task
			// that completed off the harvest path, where the broker was
			// gone and nobody could ask — worth knowing when the answer
			// turns out to be wrong.
			b.WriteString("**Check result:** not answered\n\n")
		}
	}
	// The outcome last and labelled: it is the answer, and a failed task's
	// reason is the thing most worth finding later.
	if o := strings.TrimSpace(t.Outcome); o != "" {
		label := "Outcome"
		if t.State == broker.TaskFailed {
			label = "Failed"
		}
		fmt.Fprintf(b, "**%s:** %s\n\n", label, o)
	}
}

// synthesisOutcome is the answer from the task whose job was to write
// one, if the run had such a task and it finished.
func synthesisOutcome(tasks []broker.TaskSnapshot) string {
	for _, t := range tasks {
		if t.State != broker.TaskCompleted {
			continue
		}
		if !strings.Contains(strings.ToLower(t.Name), "synth") {
			continue
		}
		if o := strings.TrimSpace(t.Outcome); o != "" {
			return o
		}
	}
	return ""
}

// isProtocol reports whether a radio message is the workers coordinating
// rather than something one of them said.
//
// The radio carries both. A split request's body is a whole assignment —
// one on disk is 5,304 bytes — and across the corpus 60 such lines put 9 KB
// of pipe-delimited protocol into the index as if a person had written it.
//
// Matched on the message's shape, not its thread: the most valuable thing
// found in this history was posted to worklog, which also carries every
// file claim.
func isProtocol(content string) bool {
	if _, _, _, _, ok := broker.ParseSplitRequest(content); ok {
		return true
	}
	if _, _, _, ok := broker.ParseFileClaimRequest(content); ok {
		return true
	}
	if _, _, _, ok := broker.ParseDepEdgeRequest(content); ok {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(content), broker.ModelEscalatePrefix)
}

// substantial reports whether a rendered run is worth putting in a corpus
// shared with hand-written notes.
//
// Measured rather than guessed: rendered sizes on disk step from 719 bytes
// to 1,198 with nothing between, and everything below that line is a probe
// or a run that died before doing anything. BM25 rewards a small document
// holding the query terms, so those crowd out real work — two 250-byte
// probes came back first and second for "narwhal 워커 outcome", above every
// run that had actually investigated something.
func substantial(md string) bool {
	const floor = 1000
	return len(md) >= floor
}

// ExportRuns writes every persisted run into dir as <run-id>.md and
// returns how many were written.
//
// Rewrites in place rather than appending: a run that is still going gains
// tasks and radio traffic, so the exporter is meant to be re-run and must
// converge on one file per run rather than accumulating copies.
func ExportRuns(dir string, all bool) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create export dir: %w", err)
	}
	ids, err := ListRuns()
	if err != nil {
		return 0, fmt.Errorf("list runs: %w", err)
	}
	written, skipped := 0, 0
	for _, id := range ids {
		snap, err := LoadRun(id)
		if err != nil {
			// One unreadable run should not stop the rest: the point is a
			// searchable corpus, and a partial one beats none.
			fmt.Fprintf(os.Stderr, "export: skip %s: %v\n", id, err)
			continue
		}
		md := ExportMarkdown(snap)
		if !all && !substantial(md) {
			skipped++
			continue
		}
		path := filepath.Join(dir, id+".md")
		if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written++
	}
	// Say what was left out. A corpus that silently drops records reads as
	// complete when it is not.
	if skipped > 0 {
		fmt.Fprintf(os.Stderr,
			"export: skipped %d runs with nothing substantial to index "+
				"(use --all to include them)\n", skipped)
	}
	return written, nil
}

// headline is a title-length opening for the document.
//
// The heading is what a search result shows as the document's name, and a
// run's prompt is not written to be one — the first line of a real prompt
// on disk runs to 300 characters before its newline, and a result list of
// those is unreadable. Cut at a sentence end when there is one nearby,
// otherwise at a word boundary. The whole prompt is still in the body, so
// nothing becomes unsearchable by being left out of the title.
func headline(prompt string) string {
	const max = 90
	s := firstProse(prompt)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	// Everything here is in runes. Mixing a byte offset from strings into
	// a rune slice is the same number only for ASCII, and the prompts on
	// this machine are routinely Korean — the first version panicked on
	// the first one it met.
	head := runes[:max]
	if i := lastIndexAnyRune(head, ".?!"); i > max/3 {
		if cut := strings.TrimSpace(string(head[:i])); cut != "" {
			return cut
		}
	}
	if i := lastIndexAnyRune(head, " "); i > max/3 {
		head = head[:i]
	}
	return strings.TrimRight(strings.TrimSpace(string(head)), ",;:-") + "…"
}

// firstProse is the first line that reads as a sentence rather than as
// part of a wrapper block.
//
// Prompts arrive with markup on the front — an uploaded-files manifest, a
// question tag, a bare path. Five of the forty runs on disk were titled
// "<uploaded_files>", which names nothing and makes five documents share
// one title. Skipping tag lines and lone paths finds the line someone
// would recognise the run by.
func firstProse(prompt string) string {
	// A <question> block is what the run is actually about. The prose
	// around it is boilerplate that repeats verbatim across runs — five
	// on disk open with the same "I've uploaded a code repository in the
	// directory ..." line, which titles them all identically.
	if q := taggedBlock(prompt, "question"); q != "" {
		return q
	}
	var fallback string
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if fallback == "" {
			fallback = line
		}
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue // a wrapper tag on its own line
		}
		if strings.HasPrefix(line, "/") && !strings.Contains(line, " ") {
			continue // a bare path
		}
		return line
	}
	return fallback
}

// taggedBlock returns the first non-empty line inside <tag>...</tag>.
func taggedBlock(text, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(text, open)
	if i < 0 {
		return ""
	}
	rest := text[i+len(open):]
	if j := strings.Index(rest, close); j >= 0 {
		rest = rest[:j]
	}
	for _, line := range strings.Split(rest, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// lastIndexAnyRune is strings.LastIndexAny over a rune slice, returning a
// rune index rather than a byte offset.
func lastIndexAnyRune(rs []rune, chars string) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if strings.ContainsRune(chars, rs[i]) {
			return i
		}
	}
	return -1
}

// firstLine is the opening line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
