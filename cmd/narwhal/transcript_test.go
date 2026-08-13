package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// writeTranscript writes a session transcript where Claude Code would,
// returning the cwd it is filed under.
//
// It asks transcriptPath where that is rather than re-deriving the
// encoding: a copy of the rule in the test would have to repeat the
// symlink resolution too, and the first version of this helper did not —
// on macOS t.TempDir() sits under /var, which is a symlink to /private/var,
// so every lookup missed.
func writeTranscript(t *testing.T, sessionID string, lines []string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	path := transcriptPath(cwd, sessionID)
	if path == "" {
		t.Fatal("transcriptPath returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cwd
}

// sampleTranscript is the shape a real worker session has: a prompt, some
// reasoning, tool calls, and their results.
func sampleTranscript() []string {
	return []string{
		`{"type":"user","timestamp":"2026-08-13T14:33:02Z","message":{"content":"investigate the auth module"}}`,
		`{"type":"assistant","timestamp":"2026-08-13T14:33:07Z","message":{"content":[{"type":"text","text":"Starting the watcher first."}]}}`,
		`{"type":"assistant","timestamp":"2026-08-13T14:33:08Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"bash /home/x/.narwhal/sessions/r1/agents/worker-a/scripts/send worklog \"found it\""}}]}}`,
		`{"type":"user","timestamp":"2026-08-13T14:33:10Z","message":{"content":[{"type":"tool_result","content":"{\"Seq\":2}"}]}}`,
		`{"type":"assistant","timestamp":"2026-08-13T14:33:15Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/repo/internal/auth/token.go"}}]}}`,
		`{"type":"attachment","timestamp":"2026-08-13T14:33:16Z"}`,
		`{"type":"queue-operation","timestamp":"2026-08-13T14:33:17Z"}`,
	}
}

func TestTranscriptPathEncodesTheWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := transcriptPath("/private/tmp/my-repo", "abc-123")
	want := filepath.Join(home, ".claude", "projects", "-private-tmp-my-repo", "abc-123.jsonl")
	if got != want {
		t.Fatalf("transcriptPath = %q, want %q", got, want)
	}
}

func TestTranscriptPathResolvesSymlinks(t *testing.T) {
	// On macOS /tmp is a symlink to /private/tmp, and Claude files the
	// transcript under the resolved path. A run whose cwd was recorded as
	// /tmp/x would otherwise look for a directory that does not exist.
	home := t.TempDir()
	t.Setenv("HOME", home)

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	viaLink := transcriptPath(link, "abc-123")
	viaReal := transcriptPath(real, "abc-123")
	if viaLink != viaReal {
		t.Errorf("symlinked cwd resolved to a different path:\n  %s\n  %s", viaLink, viaReal)
	}
}

func TestTranscriptPathNeedsASessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := transcriptPath("/tmp/x", ""); got != "" {
		t.Fatalf("transcriptPath with no session id = %q, want empty", got)
	}
}

func TestReadTranscriptKeepsOnlyActivity(t *testing.T) {
	cwd := writeTranscript(t, "s1", sampleTranscript())
	entries := readTranscript(transcriptPath(cwd, "s1"))

	// Attachments and queue operations say nothing about what the worker
	// is doing and must not appear.
	if len(entries) != 5 {
		for _, e := range entries {
			t.Logf("%s %s %q", e.at.Format("15:04:05"), e.kind, e.text)
		}
		t.Fatalf("got %d entries, want 5 (prompt, text, tool, result, tool)", len(entries))
	}

	kinds := make([]string, len(entries))
	for i, e := range entries {
		kinds[i] = e.kind
	}
	want := []string{"text", "text", "tool", "result", "tool"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}

func TestReadTranscriptSurvivesAPartialLine(t *testing.T) {
	// The file is appended to while the monitor reads it, so the last line
	// is routinely half-written. That must not blank the whole pane.
	lines := append(sampleTranscript(), `{"type":"assistant","timestamp":"2026-08`)
	cwd := writeTranscript(t, "s2", lines)

	entries := readTranscript(transcriptPath(cwd, "s2"))
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want the 5 complete ones", len(entries))
	}
}

func TestReadTranscriptOfAMissingFileIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := readTranscript(transcriptPath("/tmp/nope", "s9")); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestToolCallsLeadWithTheScriptName(t *testing.T) {
	// Wrapper script paths are long and identical up to the last segment.
	// Leading with the path pushes the arguments — the part that says what
	// the worker did — off the line.
	cwd := writeTranscript(t, "s3", sampleTranscript())
	entries := readTranscript(transcriptPath(cwd, "s3"))

	var bash string
	for _, e := range entries {
		if e.kind == "tool" && strings.HasPrefix(e.text, "Bash") {
			bash = e.text
		}
	}
	if bash == "" {
		t.Fatal("no Bash tool entry")
	}
	if strings.Contains(bash, "/.narwhal/sessions/") {
		t.Errorf("the wrapper path was not trimmed: %q", bash)
	}
	if !strings.Contains(bash, "send worklog") {
		t.Errorf("the arguments were lost: %q", bash)
	}
}

func TestFileToolsShowAShortPath(t *testing.T) {
	cwd := writeTranscript(t, "s4", sampleTranscript())
	entries := readTranscript(transcriptPath(cwd, "s4"))

	var read string
	for _, e := range entries {
		if strings.HasPrefix(e.text, "Read") {
			read = e.text
		}
	}
	if !strings.Contains(read, "auth/token.go") {
		t.Errorf("Read entry = %q, want the last two path segments", read)
	}
}

func TestRenderTranscriptMarksToolsAndText(t *testing.T) {
	cwd := writeTranscript(t, "s5", sampleTranscript())
	out := renderTranscript(readTranscript(transcriptPath(cwd, "s5")), 100)
	joined := strings.Join(out, "\n")

	if !strings.Contains(joined, "→ Bash") {
		t.Errorf("tool calls are not marked:\n%s", joined)
	}
	if !strings.Contains(joined, "· Starting the watcher") {
		t.Errorf("assistant text is not marked:\n%s", joined)
	}
	if !strings.Contains(joined, "14:33:0") {
		t.Errorf("no timestamps:\n%s", joined)
	}
}

func TestLongResultsAreClipped(t *testing.T) {
	// A tool result can be an entire file. The feed is for seeing the
	// shape of what a worker is doing, not for reading its inputs.
	var big strings.Builder
	for i := 0; i < 50; i++ {
		big.WriteString("line of output\n")
	}
	payload, _ := json.Marshal(big.String())
	lines := []string{
		`{"type":"user","timestamp":"2026-08-13T14:33:10Z","message":{"content":[{"type":"tool_result","content":` +
			string(payload) + `}]}}`,
	}
	cwd := writeTranscript(t, "s6", lines)

	out := renderTranscript(readTranscript(transcriptPath(cwd, "s6")), 100)
	if len(out) > 4 {
		t.Fatalf("a 50-line result rendered as %d lines:\n%s", len(out), strings.Join(out, "\n"))
	}
	if !strings.Contains(strings.Join(out, "\n"), "more lines") {
		t.Errorf("clipping is silent — the reader cannot tell output was dropped:\n%s",
			strings.Join(out, "\n"))
	}
}

func TestSessionViewShowsActivityWhileTheWorkerRuns(t *testing.T) {
	// The regression this replaces: the session view read the captured
	// stdout log, which a --print worker leaves empty until it exits — so
	// for the whole time you would want to watch, the pane said there was
	// no log.
	cwd := writeTranscript(t, "sess-live", sampleTranscript())

	m := testModel(0, 0)
	m.width, m.height = 100, 30
	m.snap.RunID = "run-live"
	m.live.CWD = cwd
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha", State: broker.TaskDispatched, Dispatches: 1},
	}
	m.detail = detailSession

	// Record the session id where the launcher would.
	idPath := m.sessionIDPath("alpha")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idPath, []byte("sess-live\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := m.viewSessionDetail()
	if !strings.Contains(out, "send worklog") {
		t.Errorf("the running worker's activity is not shown:\n%s", out)
	}
	if strings.Contains(out, "not dispatched") {
		t.Errorf("a running worker was reported as not started:\n%s", out)
	}
}
