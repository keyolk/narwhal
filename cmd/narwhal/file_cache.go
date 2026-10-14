package main

import (
	"os"
	"strings"
	"sync"
)

// fileCache keeps small per-worker files read by the render path, keyed by
// path and invalidated by size and mtime.
//
// View asks for a worker's captured output and session id on every frame,
// and the animation tick makes that several times a second. Reading the
// whole output log each time put disk IO on the render path; a stat is
// enough to know nothing changed. Package-level for the same reason as
// globalTranscripts: the model is copied on every update. The mutex is
// there because the poll goroutine reads session ids too (gatherLive).
type fileCache struct {
	mu      sync.Mutex
	entries map[string]cachedFile
}

type cachedFile struct {
	size    int64
	modTime int64
	lines   []string
}

var globalFiles = &fileCache{entries: map[string]cachedFile{}}

// lines returns the file split into lines with trailing newlines dropped,
// or nil when it is missing or empty. The slice is shared between callers
// and must not be modified.
func (c *fileCache) lines(path string) []string {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		c.mu.Lock()
		delete(c.entries, path)
		c.mu.Unlock()
		return nil
	}
	size, mod := fi.Size(), fi.ModTime().UnixNano()

	c.mu.Lock()
	prev, ok := c.entries[path]
	c.mu.Unlock()
	if ok && prev.size == size && prev.modTime == mod {
		return prev.lines
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	if text := strings.TrimRight(string(data), "\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	c.mu.Lock()
	c.entries[path] = cachedFile{size: size, modTime: mod, lines: lines}
	c.mu.Unlock()
	return lines
}
