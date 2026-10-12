package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

func homeModel(t *testing.T, runs ...store.LiveRun) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := newTUIModel(runs, 0, 0, true)
	m.mergeRuns(runs)
	m.homeCWD = "/src/repo"
	m.width, m.height = 110, 30
	return m
}

// pressRun is press, then the command a key returned, run to its message
// and fed back: what Bubble Tea does between a key and the next frame.
func pressRun(m tuiModel, key string) tuiModel {
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	m = next.(tuiModel)
	if cmd != nil {
		if out := cmd(); out != nil {
			if started, ok := out.(requestStartedMsg); ok {
				next, _ = m.Update(started)
				m = next.(tuiModel)
			}
		}
	}
	return m
}

// Home lists every request, in progress before finished, whatever order
// they arrive in.
func TestHomeGroupsInProgressAboveFinished(t *testing.T) {
	now := time.Now().Unix()
	m := homeModel(t,
		store.LiveRun{RunID: "old", CWD: "/src/repo", Prompt: "audit the gateway", StartedAt: now - 7200, State: "done", Tasks: 3, Done: 3},
		store.LiveRun{RunID: "live", BrokerURL: "http://x", CWD: "/src/repo", Prompt: "add a lock API", StartedAt: now - 600, State: "active", Tasks: 5, Done: 2, Running: 2},
	)
	got := ansi.Strip(m.View())
	ip, live, fin, old := strings.Index(got, "In progress · 1"), strings.Index(got, "add a lock API"),
		strings.Index(got, "Finished · 1"), strings.Index(got, "audit the gateway")
	if ip < 0 || live < ip || fin < live || old < fin {
		t.Fatalf("not grouped in order:\n%s", got)
	}
	if m.runs[0].RunID != "live" {
		t.Errorf("cursor order is not screen order: first run %q", m.runs[0].RunID)
	}
	for _, w := range []string{"2/5", "2 working", "3/3", "New request", "in /src/repo"} {
		if !strings.Contains(got, w) {
			t.Errorf("home lacks %q:\n%s", w, got)
		}
	}
}

// With nothing yet, home says how to start rather than exiting.
func TestHomeWithNoRequestsSaysHowToStart(t *testing.T) {
	got := ansi.Strip(homeModel(t).View())
	if !strings.Contains(got, "Press n to start the first request") {
		t.Errorf("empty home:\n%s", got)
	}
}

// n, a request typed in Korean, enter: the request is started where
// narwhal was launched, and its run opens.
func TestANewRequestStartsAndOpens(t *testing.T) {
	var gotCWD, gotPrompt string
	saved := startRequest
	startRequest = func(cwd, prompt string) (store.LiveRun, error) {
		gotCWD, gotPrompt = cwd, prompt
		return store.LiveRun{RunID: "plan-1", BrokerURL: "http://d", CWD: cwd, Prompt: prompt,
			StartedAt: time.Now().Unix(), State: "active"}, nil
	}
	t.Cleanup(func() { startRequest = saved })

	m := homeModel(t)
	m = press(m, "n")
	if !m.composing {
		t.Fatal("n did not open the request line")
	}
	// q is text here, not quit; Korean arrives as typed.
	for _, k := range []string{"q", "u", "e", "u", "e", " ", "정", "리"} {
		m = press(m, k)
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "» queue 정리▏") {
		t.Fatalf("typed line not shown:\n%s", got)
	}
	m = pressRun(m, "enter")
	if gotCWD != "/src/repo" || gotPrompt != "queue 정리" {
		t.Fatalf("started %q in %q", gotPrompt, gotCWD)
	}
	if m.picker || m.live.RunID != "plan-1" {
		t.Fatalf("the new run did not open: picker=%v run=%q", m.picker, m.live.RunID)
	}
}

// A request that does not start says why, and keeps what was typed.
func TestARequestThatFailsToStartSaysWhy(t *testing.T) {
	saved := startRequest
	startRequest = func(string, string) (store.LiveRun, error) {
		return store.LiveRun{}, errors.New("daemon did not become ready within 10s")
	}
	t.Cleanup(func() { startRequest = saved })

	m := press(homeModel(t), "n", "x")
	m = pressRun(m, "enter")
	if !m.picker {
		t.Fatal("left home on a failed start")
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "✗ daemon did not become ready") {
		t.Errorf("no reason shown:\n%s", got)
	}
	if string(m.composeBuf) != "x" {
		t.Errorf("the typed request was lost: %q", string(m.composeBuf))
	}
}

// Opening a request shows its graph and the steering line; esc goes home.
func TestOpeningARequestShowsTheGraphAndTheSteeringLine(t *testing.T) {
	m := homeModel(t, store.LiveRun{RunID: "r1", BrokerURL: "http://x", Prompt: "p", State: "active", Tasks: 1})
	m = press(m, "enter")
	m.snap = broker.Snapshot{RunID: "r1", State: broker.RunActive, Prompt: "p",
		Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskDispatched}}}
	got := ansi.Strip(m.View())
	for _, w := range []string{"‹ Narwhal", "1 Graph", "press i to tell the workers"} {
		if !strings.Contains(got, w) {
			t.Errorf("run view lacks %q:\n%s", w, got)
		}
	}
	if m = press(m, "esc"); !m.picker {
		t.Error("esc did not go home")
	}
}

// A finished request's steering line says why it cannot be steered.
func TestAFinishedRequestSaysItCannotBeSteered(t *testing.T) {
	m := homeModel(t, store.LiveRun{RunID: "r1", Prompt: "p", State: "done", Tasks: 1, Done: 1})
	m = press(m, "enter")
	m.snap = broker.Snapshot{RunID: "r1", State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}}}
	if got := ansi.Strip(m.steerBar()); !strings.Contains(got, "this request has finished") {
		t.Errorf("steer bar: %q", got)
	}
}

// The first frame comes before the terminal's size is known. Every view
// must draw at width and height 0 without panicking — the home screen's
// box did not, and that was the program's first frame.
func TestEveryViewDrawsBeforeTheSizeIsKnown(t *testing.T) {
	m := fixtureRun(t)
	m.width, m.height = 0, 0
	views := map[string]func(tuiModel) tuiModel{
		"home":      func(m tuiModel) tuiModel { m.picker = true; return m },
		"composing": func(m tuiModel) tuiModel { m.picker, m.composing = true, true; return m },
		"run":       func(m tuiModel) tuiModel { return m },
		"task":      func(m tuiModel) tuiModel { return press(m, "1", "enter") },
		"message":   func(m tuiModel) tuiModel { return press(m, "1", "4", "enter") },
		"help":      func(m tuiModel) tuiModel { return press(m, "?") },
	}
	for name, open := range views {
		t.Run(name, func(t *testing.T) {
			for _, w := range []int{0, 1, 5, 12} {
				v := open(m)
				v.width, v.height = w, w
				_ = v.View()
			}
		})
	}
}
