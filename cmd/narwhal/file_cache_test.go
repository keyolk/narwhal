package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileCacheFollowsTheFile(t *testing.T) {
	c := &fileCache{entries: map[string]cachedFile{}}
	path := filepath.Join(t.TempDir(), "claude-output.txt")

	if got := c.lines(path); got != nil {
		t.Fatalf("missing file: %q", got)
	}
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := c.lines(path); len(got) != 1 || got[0] != "one" {
		t.Fatalf("first read: %q", got)
	}
	// A worker appends to its log; the next frame must see it.
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := c.lines(path); len(got) != 2 || got[1] != "two" {
		t.Fatalf("after append: %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := c.lines(path); got != nil {
		t.Fatalf("a removed file is still served from cache: %q", got)
	}
}
