package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The node pane reads as prose: paragraphs carry it, and the calls between
// two of them sit on one line under the first.
func TestNarrativeFoldsToolRunsUnderTheParagraph(t *testing.T) {
	entries := []transcriptEntry{
		{kind: "text", text: "Looking for where the token is refreshed."},
		{kind: "tool", text: "Grep  refresh"},
		{kind: "result", text: "internal/auth/token.go:42"},
		{kind: "tool", text: "Read  auth/token.go"},
		{kind: "tool", text: "Read  auth/cache.go"},
		{kind: "tool", text: "Edit  auth/token.go"},
		{kind: "tool", text: "Bash  go test ./internal/auth/..."},
		{kind: "thinking", text: "The cache expiry is the culprit."},
		{kind: "tool", text: "Bash  go vet ./..."},
	}
	got := ansi.Strip(strings.Join(renderNarrative(entries, 100), "\n"))
	want := []string{
		"Looking for where the token is refreshed.",
		"→ Grep, Read, Read +2",
		"The cache expiry is the culprit.",
		"→ Bash",
	}
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("missing %q after offset %d in:\n%s", w, at, got)
		}
		at += i + len(w)
	}
	for _, leak := range []string{"token.go:42", "go test", "auth/cache.go"} {
		if strings.Contains(got, leak) {
			t.Errorf("narrative shows call detail %q:\n%s", leak, got)
		}
	}
}

// `s` is where every call is listed, so it must not fold.
func TestSessionViewKeepsTheFullFeed(t *testing.T) {
	entries := []transcriptEntry{
		{kind: "text", text: "Reading."},
		{kind: "tool", text: "Read  a.go"},
		{kind: "tool", text: "Read  b.go"},
		{kind: "tool", text: "Read  c.go"},
		{kind: "tool", text: "Read  d.go"},
	}
	got := ansi.Strip(strings.Join(renderTranscript(entries, 100), "\n"))
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if !strings.Contains(got, f) {
			t.Errorf("full feed lost %s:\n%s", f, got)
		}
	}
}

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"Splitting the loader. Then the API.":     "Splitting the loader.",
		"## Plan\nRead go.mod first. Then build.": "Plan",
		"\n\n- bump 1.5 to 1.6 now":               "bump 1.5 to 1.6 now",
		"설정 로더를 먼저 나눈다. 그다음 API.":                 "설정 로더를 먼저 나눈다.",
		"Is this the one? Probably.":              "Is this the one?",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// The box leads with what the worker last said, not its last tool, and
// never with the assignment it was handed.
func TestSummarizeLiveTakesTheWorkersLatestProse(t *testing.T) {
	now := time.Now()
	entries := []transcriptEntry{
		{at: now.Add(-9 * time.Second), kind: "text", user: true, text: "Investigate the auth module."},
		{at: now.Add(-5 * time.Second), kind: "text", text: "Reading the token code. It is long."},
		{at: now.Add(-2 * time.Second), kind: "tool", text: "Read  token.go"},
	}
	a := summarizeLive(entries, now)
	if a.said != "Reading the token code." {
		t.Errorf("said = %q", a.said)
	}

	a = summarizeLive(entries[:1], now)
	if a.said != "" {
		t.Errorf("the assignment was taken for the worker's words: %q", a.said)
	}
}

func TestRunningBoxShowsWhatItsWorkerSaid(t *testing.T) {
	m := liveModel(t)
	m.activity = map[string]agentLive{
		"alpha-worker": {tool: "Edit  internal/api.go", said: "Splitting the loader.", last: time.Now()},
	}
	if got := m.liveDetail("alpha-worker"); got != "Splitting the loader." {
		t.Errorf("liveDetail = %q", got)
	}
}
