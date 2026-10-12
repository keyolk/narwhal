package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keyolk/narwhal/internal/broker"
)

func radioModel(t *testing.T, msgs ...*broker.Message) tuiModel {
	t.Helper()
	m := testModel(1, 0)
	m.width, m.height = 100, 30
	at := time.Date(2026, 10, 12, 17, 0, 0, 0, time.Local)
	for i, msg := range msgs {
		msg.Seq = int64(i + 1)
		msg.CreatedAt = at.Add(time.Duration(i) * time.Minute)
	}
	m.snap.Messages = msgs
	m.radioCur = len(msgs) - 1
	return m
}

func claim(who, path string) *broker.Message {
	return &broker.Message{Sender: "worker-" + who,
		Content: broker.FormatFileClaimRequest(broker.FileClaimPrefix, who, []string{path})}
}

// A card is the message as prose: two lines at most, its markdown
// flattened, and a tag that repeats its sender dropped.
func TestARadioCardIsTwoLinesOfProse(t *testing.T) {
	long := "[synthesis] **결과** " + strings.Repeat("split-tui 재검증 완료 — 워커 보고와 일치한다. ", 20)
	m := radioModel(t, &broker.Message{Sender: "worker-synthesis", Content: long})
	got := ansi.Strip(m.viewRadio(100, 10))
	lines := strings.Split(got, "\n")
	card := []string{}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) != "" {
			card = append(card, l)
		}
	}
	if len(card) != 2 {
		t.Fatalf("card is %d lines:\n%s", len(card), got)
	}
	if strings.Contains(got, "[synthesis]") || strings.Contains(got, "**") {
		t.Errorf("tag or markers shown:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimRight(card[1], " "), "…") {
		t.Errorf("a cut card does not say so: %q", card[1])
	}
	for _, l := range lines {
		if n := runewidth.StringWidth(l); n > 100 {
			t.Errorf("line is %d cells: %q", n, l)
		}
	}
}

// A run of claims and releases is one line, and one stop for the cursor.
func TestCoordinationTrafficFolds(t *testing.T) {
	m := radioModel(t,
		&broker.Message{Sender: "worker-api", Content: "found it"},
		claim("api", "a.go"), claim("ci", "b.go"), claim("api", "c.go"),
		&broker.Message{Sender: "worker-ci", Content: "done"},
	)
	got := ansi.Strip(m.viewRadio(100, 10))
	if !strings.Contains(got, "⋮ 3 claims and releases · api, ci") || strings.Contains(got, "a.go") {
		t.Errorf("claims not folded:\n%s", got)
	}
	m.focus = focusRadio
	m.radioCur = 0
	m.stepRadio(1)
	if m.radioCur != 1 {
		t.Fatalf("down from the first card: %d", m.radioCur)
	}
	m.stepRadio(1)
	if m.radioCur != 4 {
		t.Errorf("down from the folded card landed on %d, want 4", m.radioCur)
	}
}

// Urgent is marked by a bar and weight, not painted red; a sender is named
// once for a run of its messages.
func TestUrgentIsMarkedNotPainted(t *testing.T) {
	forceColor(t)
	m := radioModel(t,
		&broker.Message{Sender: "worker-ci", Priority: broker.PriorityUrgent, Content: "master is red"},
		&broker.Message{Sender: "worker-ci", Content: "fixed it"},
	)
	out := m.viewRadio(100, 10)
	if strings.Contains(out, styRedBold.Render("master is red")) {
		t.Error("an urgent body is painted red")
	}
	if !strings.Contains(out, styRedBold.Render("▌")) {
		t.Error("an urgent card has no bar")
	}
	if n := strings.Count(ansi.Strip(out), " ci "); n != 1 {
		t.Errorf("sender named %d times for a run of two:\n%s", n, ansi.Strip(out))
	}
}
