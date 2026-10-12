package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

func withQuestions(t *testing.T) tuiModel {
	t.Helper()
	m := fixtureRun(t)
	now := time.Now()
	m.snap.Questions = []broker.Question{
		{ID: 1, TaskID: "api", Text: "Reject an expired key or warn?", Assumed: "reject", AskedAt: now},
		{ID: 2, TaskID: "client", Text: "Keep the old state shape for the mobile client?", Assumed: "no", Blocking: true, AskedAt: now},
		{ID: 3, TaskID: "schema", Text: "Name the field LockKey?", Assumed: "yes", AskedAt: now, Answer: "yes",
			AcceptedAssumption: true, AnsweredAt: now},
	}
	return m
}

// The run's header and steering line say there are questions, and which
// wait.
func TestOpenQuestionsAreShownOnTheRunView(t *testing.T) {
	got := ansi.Strip(withQuestions(t).View())
	if !strings.Contains(got, "? 2 questions · 1 waiting (Q)") {
		t.Errorf("header does not count the questions:\n%s", got)
	}
	if !strings.Contains(got, "the workers asked 2 questions, 1 waiting for you") {
		t.Errorf("the steering line does not point at them:\n%s", got)
	}
}

// Q lists them, the waiting one first; answers are written, kept and sent
// in one call.
func TestQuestionsAreAnsweredTogether(t *testing.T) {
	var sent struct {
		RunID   string `json:"run_id"`
		Answers []struct {
			ID        int    `json:"id"`
			Answer    string `json:"answer"`
			AsAssumed bool   `json:"as_assumed"`
		} `json:"answers"`
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"answered":2,"reopened":["client"]}`))
	}))
	defer srv.Close()
	m := withQuestions(t)
	m.live.BrokerURL = srv.URL

	m = press(m, "Q")
	if !m.questionsView {
		t.Fatal("Q did not open the questions")
	}
	view := ansi.Strip(m.View())
	first, second := strings.Index(view, "#2 client"), strings.Index(view, "#1 api")
	if first < 0 || second < first || !strings.Contains(view, "(waiting for you)") || !strings.Contains(view, "answered: as assumed") {
		t.Fatalf("questions view:\n%s", view)
	}
	// #2: write an answer; #1: take the assumption.
	m = press(m, "enter")
	m = typeKeys(m, "yes, keep it for one release")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	m = press(m, "y")
	if len(m.pendingAnswers) != 2 || calls != 0 {
		t.Fatalf("pending %v, sent %d times — answers must wait for S", m.pendingAnswers, calls)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("S sent nothing")
	}
	next, _ = m.Update(cmd())
	m = next.(tuiModel)
	if calls != 1 || len(sent.Answers) != 2 || sent.Answers[0].ID != 1 || !sent.Answers[0].AsAssumed ||
		sent.Answers[1].Answer != "yes, keep it for one release" {
		t.Errorf("sent %+v in %d calls", sent, calls)
	}
	if len(m.pendingAnswers) != 0 {
		t.Error("sent answers are still pending")
	}
	if n := ansi.Strip(m.noticeLine(200)); !strings.Contains(n, "reopened client") {
		t.Errorf("notice = %q", n)
	}
}

func TestTheQuestionsViewDrawsAtAnySize(t *testing.T) {
	m := withQuestions(t)
	m.questionsView = true
	for w := 0; w <= 12; w++ {
		for h := 0; h <= 12; h++ {
			m.width, m.height = w, h
			_ = m.View()
		}
	}
}

// A question with options is answered by its number; one waiting for the
// run's review is listed but not answerable.
func TestAnOptionIsPickedByItsNumber(t *testing.T) {
	m := fixtureRun(t)
	now := time.Now()
	m.snap.Questions = []broker.Question{
		{ID: 1, TaskID: "api", Text: "sqlite or postgres?", Options: []string{"sqlite", "postgres"}, Recommended: 1,
			Context: "single user, 5k rows a day", RaisedBy: "synthesis", AskedAt: now},
		{ID: 2, TaskID: "client", Text: "Keep v1?", Assumed: "yes", Candidate: true, AskedAt: now},
	}
	m = press(m, "Q")
	view := ansi.Strip(m.View())
	for _, want := range []string{"1) sqlite  ← recommended", "2) postgres", "single user, 5k rows a day",
		"raised by synthesis", "waiting for the run's review", "1 in review"} {
		if !strings.Contains(view, want) {
			t.Errorf("questions view lacks %q:\n%s", want, view)
		}
	}
	m = press(m, "2")
	if p := m.pendingAnswers[1]; p.text != "postgres" {
		t.Errorf("2 picked %+v", p)
	}
	// The candidate cannot be answered from here.
	m.questionSel = 1
	m = press(m, "y")
	if _, ok := m.pendingAnswers[2]; ok {
		t.Error("a question in review took an answer")
	}
	if open, _ := openQuestionCount(m.snap); open != 1 {
		t.Errorf("the header counts %d open, want 1 (the candidate is not the operator's yet)", open)
	}
}
