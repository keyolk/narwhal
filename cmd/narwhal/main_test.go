package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test off the developer's own tmux: they run these
// inside tmux, and a real call would open windows in their session. TMUX is
// unset, so tmux.New hands out a client that refuses every call, and a tmux
// that fails loudly is put first on PATH in case anything execs it anyway.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "narwhal-test-tmux-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	decoy := "#!/bin/sh\necho 'narwhal tests: real tmux must not be called' >&2\nexit 99\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(decoy), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
