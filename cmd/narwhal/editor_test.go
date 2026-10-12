package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/store"
)

func TestEditorCommentsAreDropped(t *testing.T) {
	got := stripEditorComments("\n\nfix the token log\n\n  keep this indent\n# a comment\n   # another\n\n")
	if got != "fix the token log\n\n  keep this indent" {
		t.Errorf("got %q", got)
	}
}

// $VISUAL wins over $EDITOR, arguments in it work, and vi is the fallback.
func TestTheEditorIsVisualThenEditorThenVi(t *testing.T) {
	for _, tc := range []struct{ visual, editor, want string }{
		{"code --wait", "vim", `code --wait "$1"`},
		{"", "nvim -u NONE", `nvim -u NONE "$1"`},
		{"", "", `vi "$1"`},
	} {
		t.Setenv("VISUAL", tc.visual)
		t.Setenv("EDITOR", tc.editor)
		c := editorCommand("/tmp/x.md")
		if got := c.Args[2]; got != tc.want || c.Args[len(c.Args)-1] != "/tmp/x.md" {
			t.Errorf("VISUAL=%q EDITOR=%q: %q", tc.visual, tc.editor, c.Args)
		}
	}
}

// fakeEditor writes a script that replaces the file with text, standing in
// for a person typing in vim, and records what it was handed.
func fakeEditor(t *testing.T, text string) (seen string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "seen")
	in := filepath.Join(dir, "text")
	must(t, os.WriteFile(in, []byte(text), 0o644))
	script := filepath.Join(dir, "ed")
	must(t, os.WriteFile(script, []byte("#!/bin/sh\ncp \"$1\" "+out+"\ncat "+in+" > \"$1\"\n"), 0o755))
	t.Setenv("VISUAL", script)
	return out
}

// runEditor does what tea.ExecProcess does with openEditor's command —
// runs the editor on the prepared file, then hands back what it saved —
// and feeds the result to the model, running the submit it starts.
func runEditor(t *testing.T, m tuiModel) tuiModel {
	t.Helper()
	path, err := prepareEditorFile(string(m.composeBuf), m.homeCWD)
	if err != nil {
		t.Fatal(err)
	}
	next, cmd := m.Update(finishEditor(path, editorCommand(path).Run()))
	m = next.(tuiModel)
	if cmd != nil {
		if started, ok := cmd().(requestStartedMsg); ok {
			next, _ = m.Update(started)
			m = next.(tuiModel)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the request file was left behind: %s", path)
	}
	return m
}

// A paragraph written in the editor is submitted as written, newlines and
// all; what was already typed on the line went into the editor with it.
func TestARequestWrittenInTheEditorIsSubmitted(t *testing.T) {
	var got string
	saved := startRequest
	startRequest = func(cwd, prompt string) (store.LiveRun, error) {
		got = prompt
		return store.LiveRun{RunID: "plan-1", BrokerURL: "http://d", CWD: cwd, Prompt: prompt,
			StartedAt: time.Now().Unix(), State: "active"}, nil
	}
	t.Cleanup(func() { startRequest = saved })

	seen := fakeEditor(t, "토큰 로그 정리\n\nrequest logger가 헤더를 그대로 찍는다.\n# ignored\n")
	m := press(homeModel(t), "n", "토", "큰")
	m = runEditor(t, m)
	b, _ := os.ReadFile(seen)
	if !strings.HasPrefix(string(b), "토큰\n") || !strings.Contains(string(b), "# It runs in /src/repo.") {
		t.Errorf("the editor was handed %q", b)
	}
	if got != "토큰 로그 정리\n\nrequest logger가 헤더를 그대로 찍는다." {
		t.Errorf("submitted %q", got)
	}
	if m.picker || m.live.RunID != "plan-1" {
		t.Errorf("the new run did not open: picker=%v run=%q", m.picker, m.live.RunID)
	}
}

// Saving an empty file cancels: nothing is submitted.
func TestAnEmptyEditCancels(t *testing.T) {
	fakeEditor(t, "\n# only a comment\n")
	called := false
	saved := startRequest
	startRequest = func(string, string) (store.LiveRun, error) { called = true; return store.LiveRun{}, nil }
	t.Cleanup(func() { startRequest = saved })
	m := runEditor(t, homeModel(t))
	if called || m.starting != "" || !m.picker {
		t.Errorf("an empty edit was submitted: %q", m.starting)
	}
}

// A long request shows on the line as its first line and a count.
func TestAMultiLineRequestShowsItsFirstLine(t *testing.T) {
	m := press(homeModel(t), "n")
	m.composeBuf = []rune("first line\nsecond\nthird")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "» first line▏  (+2 lines)") {
		t.Errorf("line:\n%s", got)
	}
}

// e on home and ctrl-e on the request line both open the editor; e while
// typing is a letter.
func TestTheEditorKeys(t *testing.T) {
	m := homeModel(t)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}); cmd == nil {
		t.Error("e on home did nothing")
	}
	m = press(m, "n", "e")
	if string(m.composeBuf) != "e" {
		t.Errorf("e while typing was not a letter: %q", string(m.composeBuf))
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE}); cmd == nil {
		t.Error("ctrl-e on the line did nothing")
	}
}
