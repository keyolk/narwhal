// transcript_cache.go keeps a worker's parsed activity between frames and
// reads only what has been appended since.
//
// The node pane scrolls through the whole transcript, and every scroll step
// and every frame asked for it again from scratch. A transcript is a
// JSONL file that grows to megabytes over a long task — the largest on
// disk here is 1.5MB — so the monitor was re-reading and re-parsing all of
// it on each keystroke:
//
//	10 scroll steps  153ms
//	10 renders       136ms
//
// That is 15ms of work to move a cursor one line, repeated at the poll
// interval whether or not anything changed. A transcript is append-only,
// which is the property that makes the fix cheap: remember where the last
// read stopped and parse forward from there.
package main

import (
	"os"
	"sync"
)

// transcriptCache holds parsed entries per file, with enough of the file's
// identity to know when the tail is still valid.
type transcriptCache struct {
	mu      sync.Mutex
	entries map[string]*cachedTranscript
}

type cachedTranscript struct {
	// size and modTime identify the file state the entries were parsed
	// from. Size alone would miss a rewrite that happens to land on the
	// same length; mtime alone has filesystem granularity coarser than the
	// interval between two appends.
	size    int64
	modTime int64
	// offset is where parsing stopped — the start of the last incomplete
	// line, since a transcript being appended to routinely ends mid-write.
	offset  int64
	entries []transcriptEntry
	// rendered is the last render of these entries, at renderWidth. The
	// pane shows a handful of lines out of hundreds, and styling all of
	// them to display six is work thrown away on every frame — 2.4ms
	// against a feed of 929 lines. Dropped whenever new entries arrive or
	// the pane is resized, which are the only things that change it.
	renderWidth int
	rendered    []string
	// tailProbe is the last few hundred bytes before offset, so the next
	// read can tell "the same file, longer" from "a different file that
	// happens to be at least this long".
	tailProbe string
}

// readProbe returns the bytes just before offset, for the prefix check.
func readProbe(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const probe = 512
	at := offset - probe
	if at < 0 {
		at = 0
	}
	buf := make([]byte, offset-at)
	if _, err := f.ReadAt(buf, at); err != nil {
		return ""
	}
	return string(buf)
}

// globalTranscripts is shared because the model is copied by value on every
// Bubble Tea update; a cache on the struct would be discarded each frame.
// Access is mutex-guarded: the poll command runs on its own goroutine.
var globalTranscripts = &transcriptCache{entries: map[string]*cachedTranscript{}}

// read returns the file's entries, parsing only what is new.
//
// A file that shrank or was replaced is re-read from the beginning: the
// cached prefix is no longer a prefix of anything.
func (c *transcriptCache) read(path string) []transcriptEntry {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	prev, ok := c.entries[path]
	if ok && prev.size == fi.Size() && prev.modTime == fi.ModTime().UnixNano() {
		return prev.entries
	}
	// Re-read from the start unless the file is the one we parsed plus
	// more. Anything else — a shrink, or a rewrite that lands on the same
	// length — means the cached prefix is not a prefix of this file, and
	// appending to it would splice two different histories together.
	if !ok || fi.Size() < prev.offset {
		prev = &cachedTranscript{}
		c.entries[path] = prev
	} else if prev.offset > 0 && !samePrefix(path, prev) {
		prev = &cachedTranscript{}
		c.entries[path] = prev
	}

	f, err := os.Open(path)
	if err != nil {
		return prev.entries
	}
	defer f.Close()

	fresh, consumed := parseTranscriptFrom(f, prev.offset)
	if len(fresh) > 0 {
		prev.rendered = nil
	}
	prev.entries = append(prev.entries, fresh...)
	prev.offset += consumed
	prev.tailProbe = readProbe(path, prev.offset)
	prev.size = fi.Size()
	prev.modTime = fi.ModTime().UnixNano()
	return prev.entries
}

// samePrefix reports whether the file still begins with the bytes the
// cache last parsed.
//
// Only the boundary is compared, not the whole prefix: a rewrite that
// leaves the last line before the offset untouched and changes something
// earlier is not a thing an append-only log does, and reading megabytes to
// rule it out would undo the point of the cache.
func samePrefix(path string, prev *cachedTranscript) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const probe = 512
	at := prev.offset - probe
	if at < 0 {
		at = 0
	}
	buf := make([]byte, prev.offset-at)
	if _, err := f.ReadAt(buf, at); err != nil {
		return false
	}
	return string(buf) == prev.tailProbe
}

// render returns the file's entries rendered to width, reusing the last
// render when neither the file nor the width has changed.
func (c *transcriptCache) render(path string, width int) []string {
	entries := c.read(path)
	if len(entries) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.entries[path]
	if !ok {
		return renderTranscript(entries, width)
	}
	if cached.rendered != nil && cached.renderWidth == width {
		return cached.rendered
	}
	cached.rendered = renderTranscript(cached.entries, width)
	cached.renderWidth = width
	return cached.rendered
}

// forget drops a file's cache. Used by tests; the monitor watches a handful
// of transcripts for the life of a run, so nothing needs evicting.
func (c *transcriptCache) forget(path string) {
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
}
