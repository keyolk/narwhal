package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
	if m.quit || string(m.steerBuf) != "qj" {
		t.Errorf("keys were taken as shortcuts: quit=%v buf=%q", m.quit, string(m.steerBuf))
	}
}

// An instruction typed in Korean must arrive in Korean. Outside the line,
// jamo are mapped to shortcuts; inside it, that mapping would garble it.
func TestKoreanIsTypedNotMappedToShortcuts(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	m = typeKeys(m, "ㅂㅓ")
	if string(m.steerBuf) != "ㅂㅓ" || m.quit {
		t.Errorf("buf = %q quit=%v", string(m.steerBuf), m.quit)
	}
}

func TestEscCancelsWithoutSending(t *testing.T) {
	m := steerModel(t, "http://x")
	m = press(m, "i")
	m = typeKeys(m, "abc")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)
	if m.steering || len(m.steerBuf) != 0 || cmd != nil {
		t.Errorf("esc: steering=%v buf=%q cmd=%v", m.steering, string(m.steerBuf), cmd != nil)
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

// A run read from disk has no daemon to post to; say so instead of failing
// quietly.
func TestSteeringAFinishedRunSaysWhy(t *testing.T) {
	m := steerModel(t, "")
	m = press(m, "i")
	m = typeKeys(m, "anything")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if cmd != nil {
		t.Error("tried to send to a run with no daemon")
	}
	if !strings.Contains(ansi.Strip(m.viewFooter()), "no live daemon") {
		t.Errorf("footer = %q", ansi.Strip(m.viewFooter()))
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
