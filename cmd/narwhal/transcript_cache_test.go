package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The node pane scrolls through the whole transcript, and every scroll step
// and every frame used to re-read and re-parse the file from scratch. On
// the largest transcript on disk here — 1.5MB, 929 rendered lines — that
// was 15ms to move the cursor one line, repeated at the poll interval
// whether or not anything had changed.
//
// A transcript is append-only, which is what makes the fix cheap. These
// tests pin the parts where "cheap" could quietly become "wrong": a worker
// appends while we read, and the tail of the file is routinely a partial
// write.

// transcriptFile writes n entries and returns the path.
func transcriptFile(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(transcriptBody(0, n)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { globalTranscripts.forget(path) })
	return path
}

func transcriptBody(from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-08-18T10:%02d:00Z",`+
			`"message":{"content":[{"type":"text","text":"entry %d"}]}}`+"\n", i%60, i)
	}
	return b.String()
}

func appendTo(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

func TestAnAppendIsPickedUp(t *testing.T) {
	// The whole risk of caching a growing file: stopping at what you have.
	path := transcriptFile(t, 5)
	if got := len(globalTranscripts.read(path)); got != 5 {
		t.Fatalf("first read got %d entries, want 5", got)
	}

	appendTo(t, path, transcriptBody(5, 8))

	if got := len(globalTranscripts.read(path)); got != 8 {
		t.Errorf("after an append the cache reports %d entries, want 8", got)
	}
}

func TestAPartialLineIsNotConsumed(t *testing.T) {
	// The file is being appended to while we read it, so the tail is
	// routinely half a line. Counting it as read would drop that entry
	// forever once the rest arrived — a silent hole in the history, which
	// is worse than the full re-read this replaces.
	path := transcriptFile(t, 3)
	globalTranscripts.read(path)

	// A line that stops mid-JSON, the way a concurrent write looks.
	appendTo(t, path, `{"type":"assistant","timestamp":"2026-08-18T10:04:00Z",`)
	if got := len(globalTranscripts.read(path)); got != 3 {
		t.Fatalf("a half-written line was parsed: %d entries", got)
	}

	// The rest arrives.
	appendTo(t, path, `"message":{"content":[{"type":"text","text":"entry 3"}]}}`+"\n")
	entries := globalTranscripts.read(path)
	if len(entries) != 4 {
		t.Fatalf("the completed line was not picked up: %d entries", len(entries))
	}
	if !strings.Contains(entries[3].text, "entry 3") {
		t.Errorf("the recovered entry is %q", entries[3].text)
	}
}

func TestATruncatedFileIsReread(t *testing.T) {
	// A file that shrank is not a file the cached prefix is a prefix of.
	path := transcriptFile(t, 10)
	if got := len(globalTranscripts.read(path)); got != 10 {
		t.Fatalf("setup read %d entries", got)
	}

	if err := os.WriteFile(path, []byte(transcriptBody(0, 2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := len(globalTranscripts.read(path)); got != 2 {
		t.Errorf("after truncation the cache reports %d entries, want 2", got)
	}
}

func TestARewriteOfTheSameLengthIsNoticed(t *testing.T) {
	// Size alone would miss this, which is why mtime is part of the key.
	path := transcriptFile(t, 4)
	first := globalTranscripts.read(path)
	if len(first) != 4 {
		t.Fatalf("setup read %d entries", len(first))
	}

	body := strings.ReplaceAll(transcriptBody(0, 4), "entry", "ENTRY")
	if len(body) != len(transcriptBody(0, 4)) {
		t.Fatal("setup: the rewrite changed the length")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	entries := globalTranscripts.read(path)
	if len(entries) == 0 || !strings.Contains(entries[0].text, "ENTRY") {
		t.Error("a same-length rewrite was served from the stale cache")
	}
}

func TestTheRenderIsReusedUntilSomethingChanges(t *testing.T) {
	path := transcriptFile(t, 20)

	first := globalTranscripts.render(path, 80)
	second := globalTranscripts.render(path, 80)
	if len(first) == 0 {
		t.Fatal("nothing rendered")
	}
	// Same backing array means the render was not redone.
	if &first[0] != &second[0] {
		t.Error("the render was recomputed for an unchanged file")
	}

	// A new width has to re-render: the lines wrap differently.
	narrow := globalTranscripts.render(path, 40)
	if len(narrow) > 0 && len(first) > 0 && &narrow[0] == &first[0] {
		t.Error("a resized pane reused a render made at another width")
	}

	// And an append has to re-render, or the new lines never appear.
	appendTo(t, path, transcriptBody(20, 22))
	grown := globalTranscripts.render(path, 40)
	if len(grown) <= len(narrow) {
		t.Errorf("after an append the render is %d lines, was %d", len(grown), len(narrow))
	}
}

func TestAMissingTranscriptIsNotAnError(t *testing.T) {
	if got := globalTranscripts.read(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Errorf("a missing file returned %d entries", len(got))
	}
	if got := globalTranscripts.read(""); got != nil {
		t.Errorf("an empty path returned %d entries", len(got))
	}
}

func TestScrollingBackSurvivesAPoll(t *testing.T) {
	// Reading history in a live run means the file grows under you. If a
	// poll rewound the pane, the history would be unreadable in exactly
	// the case it matters — while the worker is still writing.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 60)

	m = press(m, "k", "k", "k")
	parked := m.nodeScroll
	if parked == nodeScrollTail {
		t.Fatal("setup: still at the tail")
	}

	// A poll delivering the same snapshot must not move the reader.
	updated, _ := m.Update(snapshotMsg{snap: m.snap})
	m = updated.(tuiModel)

	if m.nodeScroll != parked {
		t.Errorf("a poll moved the node pane from %d to %d", parked, m.nodeScroll)
	}
}

func TestTheTailKeepsUpWithNewOutput(t *testing.T) {
	// The other half: a pane left at the tail must follow the worker.
	m := flexModel(t)
	m.focus = focusNode
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 10)

	before := m.nodeLineCount()
	sid := m.workerSessionID("task-1")
	appendTo(t, transcriptPath(m.live.CWD, sid), transcriptBody(10, 20))

	if after := m.nodeLineCount(); after <= before {
		t.Errorf("new output did not reach the pane: %d then %d", before, after)
	}
	if m.nodeScroll != nodeScrollTail {
		t.Errorf("the pane left the tail on its own: %d", m.nodeScroll)
	}
}

func TestTheCacheIsSafeUnderConcurrentReaders(t *testing.T) {
	// The poll command runs on its own goroutine while View renders on the
	// main one, and render() hands back the cached slice by reference.
	// What makes that safe is that renderTranscript allocates a fresh
	// slice — an append swaps the field, it does not write through the
	// one a reader is holding. Run under -race, which make check does.
	path := transcriptFile(t, 50)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				for _, l := range globalTranscripts.render(path, 60+n) {
					_ = len(l)
				}
				for _, e := range globalTranscripts.read(path) {
					_ = e.text
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			appendTo(t, path, transcriptBody(50+j, 51+j))
		}
	}()
	wg.Wait()
}
