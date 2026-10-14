package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

func steerModel(t *testing.T, url string) tuiModel {
	t.Helper()
	m := testModel(0, 0)
	m.live.RunID, m.live.BrokerURL = "r1", url
	m.snap.Tasks = []broker.TaskSnapshot{{ID: "api"}, {ID: "schema"}}
	return m
}

func typeKeys(m tuiModel, s string) tuiModel {
	for _, r := range s {
		var msg tea.KeyMsg
		if r == ' ' {
			msg = tea.KeyMsg{Type: tea.KeySpace}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		next, _ := m.Update(msg)
		m = next.(tuiModel)
	}
	return m
}

func TestIOpensTheCommandLineAndKeysAreTyped(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	if !m.steering {
		t.Fatal("i did not open the command line")
	}
	// q would quit and j would move the cursor; here they are text.
	m = typeKeys(m, "qj")
	if m.quit || m.steerBuf.Value() != "qj" {
		t.Errorf("keys were taken as shortcuts: quit=%v buf=%q", m.quit, m.steerBuf.Value())
	}
}

// An instruction typed in Korean must arrive in Korean. Outside the line,
// jamo are mapped to shortcuts; inside it, that mapping would garble it.
func TestKoreanIsTypedNotMappedToShortcuts(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	m = typeKeys(m, "ㅂㅓ")
	if m.steerBuf.Value() != "ㅂㅓ" || m.quit {
		t.Errorf("buf = %q quit=%v", m.steerBuf.Value(), m.quit)
	}
}

func TestEscCancelsWithoutSending(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	m = typeKeys(m, "abc")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)
	if m.steering || m.steerBuf.Len() != 0 || cmd != nil {
		t.Errorf("esc: steering=%v buf=%q cmd=%v", m.steering, m.steerBuf.Value(), cmd != nil)
	}
}

func TestEnterPostsToTheControlRouteAsTheOperator(t *testing.T) {
	var mu sync.Mutex
	var got steerRequest
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := steerModel(t, srv.URL)
	m = press(m, "i")
	m = typeKeys(m, "!@schema LockKey 필드 추가해 줘")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("enter did not send")
	}
	res := cmd()
	sent, ok := res.(steerSentMsg)
	if !ok || sent.err != nil {
		t.Fatalf("send result = %#v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/api/v1/control/send" {
		t.Errorf("posted to %s", path)
	}
	if got.RunID != "r1" || got.Priority != "urgent" || got.Content != "@schema LockKey 필드 추가해 줘" {
		t.Errorf("request = %+v", got)
	}
	if len(got.Mentions) != 1 || got.Mentions[0] != "worker-schema" {
		t.Errorf("mentions = %v, want [worker-schema]", got.Mentions)
	}
}

func TestSendResultShowsInTheFooter(t *testing.T) {
	m := steerModel(t, "http://x")
	next, _ := m.Update(steerSentMsg{text: "hold off on the API"})
	m = next.(tuiModel)
	if foot := ansi.Strip(m.viewFooter()); !strings.Contains(foot, "sent: hold off on the API") {
		t.Errorf("footer = %q", foot)
	}
	next, _ = m.Update(steerSentMsg{text: "x", err: http.ErrServerClosed})
	m = next.(tuiModel)
	if foot := ansi.Strip(m.viewFooter()); !strings.Contains(foot, "not sent") {
		t.Errorf("a failed send is not reported: %q", foot)
	}
}

// A finished run whose answer came from several tasks cannot be resumed
// without saying which; say so rather than guess.
func TestAnUnaddressedInstructionToAFinishedRunWithNoAnswerTaskSaysWhy(t *testing.T) {
	m := steerModel(t, "")
	m.snap.State = broker.RunDone
	m = press(m, "i")
	m = typeKeys(m, "anything")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if cmd != nil {
		t.Error("sent an instruction with no task to resume")
	}
	if !strings.Contains(ansi.Strip(m.viewFooter()), "say which with @task") {
		t.Errorf("footer = %q", ansi.Strip(m.viewFooter()))
	}
}

// Addressed to a task, the same instruction goes to the daemon running
// now — the one that ran the run may be gone — and reopens the task.
func TestAnInstructionToAFinishedTaskReachesTheRunningDaemon(t *testing.T) {
	var got steerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"seq":9,"reopened":["schema"]}`))
	}))
	defer srv.Close()
	saved := daemonURL
	daemonURL = func() (string, error) { return srv.URL, nil }
	t.Cleanup(func() { daemonURL = saved })

	m := steerModel(t, "")
	m.snap.State = broker.RunDone
	m = press(m, "i")
	m = typeKeys(m, "@schema also cover the legacy payload")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("an instruction to a finished task was not sent")
	}
	next, _ = m.Update(cmd())
	m = next.(tuiModel)
	if len(got.Mentions) != 1 || got.Mentions[0] != "worker-schema" {
		t.Errorf("mentions = %v", got.Mentions)
	}
	if foot := ansi.Strip(m.viewFooter()); !strings.Contains(foot, "reopened schema") {
		t.Errorf("footer does not say the task was reopened: %q", foot)
	}
}

// From a task's view, i starts an instruction already addressed to it.
func TestInstructingFromATaskAddressesIt(t *testing.T) {
	m := fixtureRun(t)
	m.openTask("client", time.Time{})
	m = press(m, "i")
	if !m.steering || m.steerBuf.Value() != "@client " {
		t.Fatalf("steering=%v buf=%q, want the line open with @client", m.steering, m.steerBuf.Value())
	}
	m = typeKeys(m, "더 짧게")
	if m.steerBuf.Value() != "@client 더 짧게" {
		t.Errorf("typed into the task view rather than the line: %q", m.steerBuf.Value())
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "» @client 더 짧게") {
		t.Errorf("the task view does not show the line being typed:\n%s", view)
	}
}

// The command line replaces the hint row; it must not add a row and push
// the panes up.
func TestCommandLineKeepsTheFooterHeight(t *testing.T) {
	m := steerModel(t, "http://x")
	before := strings.Count(m.viewFooter(), "\n")
	m = press(m, "i")
	if after := strings.Count(m.viewFooter(), "\n"); after != before {
		t.Errorf("footer rows %d → %d", before+1, after+1)
	}
}

func TestParseSteerIgnoresBlankAndStrangers(t *testing.T) {
	tasks := map[string]bool{"api": true}
	if _, ok := parseSteer("  !  ", "r", tasks); ok {
		t.Error("a blank instruction was accepted")
	}
	req, _ := parseSteer("@nobody and @worker-api", "r", tasks)
	if len(req.Mentions) != 1 || req.Mentions[0] != "worker-api" {
		t.Errorf("mentions = %v", req.Mentions)
	}
}

// The line is the kit's text field: the cursor moves, a word goes with
// ctrl+w, and what is typed lands at the cursor, not at the end.
func TestTheCommandLineEditsAtTheCursor(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	m = typeKeys(m, "check lock")
	for _, k := range []tea.KeyMsg{{Type: tea.KeyCtrlW}, {Type: tea.KeyLeft}} {
		next, _ := m.Update(k)
		m = next.(tuiModel)
	}
	m = typeKeys(m, "s")
	if got := m.steerBuf.Value(); got != "checks " {
		t.Errorf("buf = %q, want %q", got, "checks ")
	}
	if !m.steering {
		t.Error("editing keys closed the line")
	}
}
