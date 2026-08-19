package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A run's record lives in ~/.narwhal/runs as JSON, which nothing but
// narwhal reads. The search index over this machine's notes takes
// directories of markdown — so the way a past run becomes findable is a
// file in that shape, not a process talking to another process.
//
// There was nothing worth indexing until #24: every run on disk recorded
// what each task was asked and nothing about what it answered. The
// outcomes are the reason this is worth doing now.

func exportFixture(t *testing.T) broker.Snapshot {
	t.Helper()
	b := broker.New()
	r := b.CreateRun("s123-1", "audit the gateway TLS coverage", "/tmp/repo", "main")
	r.CreateStandardThreads()
	t1 := r.AddTask("task-1", "investigate", "check every gateway host", nil)
	t1.SetModel("opus")
	t1.StartDispatch("d1", "worker-task-1")
	t1.CompleteDispatch("4 of 7 SANs are covered", r)

	t2 := r.AddTask("task-2", "synthesis", "write it up", []string{"task-1"})
	t2.StartDispatch("d2", "worker-task-2")
	t2.FailDispatch("worker exited without calling task-done", r)

	r.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		"the apne2 gateway advertises a host it has no cert for")
	return r.Snapshot()
}

func TestTheExportCarriesWhatMakesARunFindable(t *testing.T) {
	snap := exportFixture(t)
	md := ExportMarkdown(snap)

	// The things someone would search for.
	for _, want := range []string{
		"audit the gateway TLS coverage",      // the prompt
		"4 of 7 SANs are covered",             // what a task concluded
		"worker exited without calling",       // and why one failed
		"the apne2 gateway advertises a host", // what was said on the radio
		"check every gateway host",            // the assignment
		"s123-1",                              // the run id
		"/tmp/repo",                           // where it happened
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the export does not mention %q:\n%s", want, md)
		}
	}
}

func TestTheExportOpensWithATitleTheIndexerCanRead(t *testing.T) {
	// kmd takes a document's title from its first `# heading`, so the
	// first line has to be one — and it has to say something, since the
	// title is what a search result shows.
	md := ExportMarkdown(exportFixture(t))
	first := strings.SplitN(md, "\n", 2)[0]
	if !strings.HasPrefix(first, "# ") {
		t.Fatalf("the export opens with %q, not a heading", first)
	}
	if !strings.Contains(first, "audit the gateway TLS coverage") {
		t.Errorf("the heading does not name the run: %q", first)
	}
}

func TestExportRunWritesOneFilePerRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	snap := exportFixture(t)
	if err := SaveRun(snap); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	dir := filepath.Join(os.Getenv("HOME"), ".narwhal", "exports")
	n, err := ExportRuns(dir)
	if err != nil {
		t.Fatalf("ExportRuns: %v", err)
	}
	if n != 1 {
		t.Errorf("exported %d runs, want 1", n)
	}
	body, err := os.ReadFile(filepath.Join(dir, "s123-1.md"))
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.Contains(string(body), "4 of 7 SANs are covered") {
		t.Error("the written file does not carry the outcome")
	}
}

func TestExportingTwiceDoesNotDuplicate(t *testing.T) {
	// The exporter is meant to be re-run — a run that is still going gets
	// more tasks and more radio traffic. Re-exporting has to update the
	// file rather than accumulate copies of it.
	t.Setenv("HOME", t.TempDir())
	if err := SaveRun(exportFixture(t)); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	dir := filepath.Join(os.Getenv("HOME"), ".narwhal", "exports")
	if _, err := ExportRuns(dir); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if _, err := ExportRuns(dir); err != nil {
		t.Fatalf("second export: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("two exports left %d files: %v", len(entries), names)
	}
}

func TestARunWithNothingToSayIsStillExported(t *testing.T) {
	// A run that failed before any task finished is exactly the kind of
	// thing worth finding later. It must not panic on the empty fields.
	b := broker.New()
	r := b.CreateRun("s999-1", "", "", "main")
	r.AddTask("task-1", "n", "", nil)
	md := ExportMarkdown(r.Snapshot())
	if !strings.Contains(md, "s999-1") {
		t.Errorf("a bare run exported nothing identifying:\n%s", md)
	}
}

func TestTheHeadingIsShortEnoughToBeATitle(t *testing.T) {
	// The heading is what a search result shows as the document's name. A
	// prompt's first line runs to a paragraph on real runs — one on disk
	// opens with 300 characters before its first newline — and a result
	// list of those is unreadable.
	long := "TWeb (terminal-native browser: Rust frontend and Electron engine, " +
		"rendering web pages into tmux panes via Kitty graphics). Fourteen PRs " +
		"landed today, #14 through #27. This run finishes the one piece those " +
		"left scoped: making one Electron process actually serve many panes."
	b := broker.New()
	r := b.CreateRun("s1-1", long, "/tmp", "main")
	md := ExportMarkdown(r.Snapshot())

	heading := strings.SplitN(md, "\n", 2)[0]
	if len([]rune(heading)) > 100 {
		t.Errorf("the heading is %d characters:\n%s", len([]rune(heading)), heading)
	}
	// Truncated in the heading, but still searchable in full.
	if !strings.Contains(md, "one Electron process actually serve many panes") {
		t.Error("the full prompt did not survive anywhere in the document")
	}
}

func TestAKoreanPromptDoesNotBreakTheHeading(t *testing.T) {
	// The first version indexed a rune slice with a byte offset from
	// strings.LastIndexAny, which is the same number only for ASCII. Real
	// prompts here are routinely Korean, and the exporter panicked on the
	// first one it met.
	long := strings.Repeat("게이트웨이 인증서 SAN 커버리지를 감사하고 결과를 보고한다. ", 5)
	b := broker.New()
	r := b.CreateRun("s2-1", long, "/tmp", "main")

	md := ExportMarkdown(r.Snapshot()) // must not panic
	heading := strings.SplitN(md, "\n", 2)[0]
	if len([]rune(heading)) > 100 {
		t.Errorf("the heading is %d runes: %s", len([]rune(heading)), heading)
	}
	if !strings.Contains(heading, "게이트웨이") {
		t.Errorf("the heading lost the prompt: %q", heading)
	}
}

func TestAPromptThatOpensWithMarkupGetsARealTitle(t *testing.T) {
	// Prompts arrive with a wrapper block on the front — an uploaded-files
	// manifest, a question tag. Five of the forty runs on disk are titled
	// "<uploaded_files>", which names nothing and collides with the other
	// four. The first line that is prose is the title.
	prompt := "<uploaded_files>\n/tmp/repo\n</uploaded_files>\n" +
		"Trace the rewrap implementation and explain how resize handles " +
		"line continuation between the screen buffer and the scrollback."
	b := broker.New()
	r := b.CreateRun("s3-1", prompt, "/tmp", "main")

	heading := strings.SplitN(ExportMarkdown(r.Snapshot()), "\n", 2)[0]
	if strings.Contains(heading, "<") {
		t.Errorf("the heading is markup, not a title: %q", heading)
	}
	if !strings.Contains(heading, "rewrap") {
		t.Errorf("the heading does not name what the run was about: %q", heading)
	}
}

func TestRunsAskingAboutDifferentThingsGetDifferentTitles(t *testing.T) {
	// The boilerplate around a question is identical across runs — five
	// of the forty on disk open with the same "I've uploaded a code
	// repository in the directory ..." line. Titled by that, they are
	// indistinguishable in a result list. The question tag carries what
	// actually differs.
	mk := func(id, question string) string {
		prompt := "<uploaded_files>\n/tmp/repo\n</uploaded_files>\n" +
			"I've uploaded a code repository in the directory /tmp/repo. " +
			"Consider the following question:\n\n<question>\n" + question +
			"\n</question>"
		b := broker.New()
		r := b.CreateRun(id, prompt, "/tmp", "main")
		return strings.SplitN(ExportMarkdown(r.Snapshot()), "\n", 2)[0]
	}
	a := mk("s4-1", "How does kitty's terminal reflow system work internally?")
	c := mk("s4-2", "Why does the scrollback buffer lose its cursor position?")
	if a == c {
		t.Errorf("two runs asking different things share a title: %q", a)
	}
	if !strings.Contains(a, "reflow") {
		t.Errorf("the title does not carry the question: %q", a)
	}
}
