package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/link"
	"github.com/keyolk/tuikit/tuitest"

	"github.com/keyolk/narwhal/internal/broker"
)

const prURL = "https://github.com/o/r/pull/7"

// stubLauncher records what would have been opened; the real opener never
// runs in a test.
func stubLauncher(t *testing.T) *[]string {
	t.Helper()
	var (
		mu  sync.Mutex
		got []string
	)
	old := link.Launcher
	link.Launcher = func(u string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, u)
		return nil
	}
	t.Cleanup(func() { link.Launcher = old })
	return &got
}

func osc8(u, label string) string { return "\x1b]8;;" + u + "\x1b\\" + label + "\x1b]8;;\x1b\\" }

// keyRun presses key, runs the command it returns and feeds the results
// back, the way Bubble Tea would; it returns the messages too.
func keyRun(t *testing.T, m tuiModel, key string) (tuiModel, []tea.Msg) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	m = next.(tuiModel)
	msgs := tuitest.Drain(cmd, 50*time.Millisecond)
	for _, msg := range msgs {
		switch msg.(type) {
		case link.OpenedMsg, linkOfMsg:
			next, _ = m.Update(msg)
			m = next.(tuiModel)
		}
	}
	return m, msgs
}

func openedMsgs(msgs []tea.Msg) []link.OpenedMsg {
	var out []link.OpenedMsg
	for _, msg := range msgs {
		switch o := msg.(type) {
		case link.OpenedMsg:
			out = append(out, o)
		case linkOfMsg:
			out = append(out, o.OpenedMsg)
		}
	}
	return out
}

func resultWithPRs(t *testing.T, prs ...string) tuiModel {
	t.Helper()
	m := runMouseModel(t, false)
	m.snap.Tasks[0].State = broker.TaskCompleted
	m.snap.Tasks[0].Outcome = "done"
	m.snap.Tasks[0].Artifacts = &broker.Artifacts{PRs: prs}
	m.detail = detailResult
	return m
}

func TestResultLinksThePRLabelToItsURL(t *testing.T) {
	m := resultWithPRs(t, prURL)
	view := m.View()
	if !strings.Contains(view, osc8(prURL, "o/r#7")) {
		t.Fatalf("the PR label should be a hyperlink to %s:\n%q", prURL, view)
	}
	for i, l := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("row %d is %d cells, wider than the %d-cell screen: %q", i, w, m.width, ansi.Strip(l))
		}
	}
}

func TestANonHTTPURLIsNeverLinked(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd"} {
		m := resultWithPRs(t, bad)
		if v := m.View(); strings.Contains(v, "\x1b]8;;") {
			t.Errorf("%s was linked:\n%q", bad, v)
		}
	}
}

// prOutputs is Outputs with one PR, selected.
func prOutputs(t *testing.T, ref string) tuiModel {
	t.Helper()
	m := withOutputs(t)
	m.snap.Tasks[0].Artifacts = &broker.Artifacts{PRs: []string{ref}}
	m = press(m, "o")
	if !m.outputsView {
		t.Fatal("setup: Outputs is not open")
	}
	for i := 0; i < 8 && m.outputs()[m.outputSel].kind != outPR; i++ {
		m = press(m, "j")
	}
	if m.outputs()[m.outputSel].kind != outPR {
		t.Fatal("setup: no PR output to select")
	}
	return m
}

func TestOutputsListLinksThePRAndStillFits(t *testing.T) {
	m := prOutputs(t, prURL)
	if !strings.Contains(m.View(), "\x1b]8;;"+prURL+"\x1b\\") {
		t.Fatalf("the PR row should link to %s:\n%q", prURL, m.View())
	}
	m = press(m, "k") // the PR row unselected draws its label plain
	if !strings.Contains(m.View(), osc8(prURL, "o/r#7")) {
		t.Fatalf("the unselected PR row should link its label:\n%q", m.View())
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("row %d is %d cells, wider than the %d-cell screen: %q", i, w, m.width, ansi.Strip(l))
		}
	}
}

func TestOOnAPROutputOpensItsURL(t *testing.T) {
	opened := stubLauncher(t)
	m, msgs := keyRun(t, prOutputs(t, prURL), "o")
	if len(*opened) != 1 || (*opened)[0] != prURL {
		t.Fatalf("launched %q, want exactly %s", *opened, prURL)
	}
	if o := openedMsgs(msgs); len(o) != 1 || o[0].URL != prURL || o[0].Err != nil {
		t.Fatalf("OpenedMsg = %+v", o)
	}
	if !strings.Contains(toastText(m), "opened "+prURL) {
		t.Fatalf("toast = %q", toastText(m))
	}
}

func TestOOnAPROutputWithABadURLLaunchesNothing(t *testing.T) {
	opened := stubLauncher(t)
	m, _ := keyRun(t, prOutputs(t, "javascript:alert(1)"), "o")
	if len(*opened) != 0 {
		t.Fatalf("launched %q", *opened)
	}
	if !strings.Contains(toastText(m), "open failed") {
		t.Fatalf("toast = %q", toastText(m))
	}
}

func messageModel(t *testing.T, content string) tuiModel {
	t.Helper()
	m := runMouseModel(t, false)
	m.snap.Messages[0].Content = content
	m.radioCur = 0
	m.detail = detailMessage
	return m
}

func TestOOnAMessageOpensItsLink(t *testing.T) {
	opened := stubLauncher(t)
	m, msgs := keyRun(t, messageModel(t, "PR is up: "+prURL+"."), "o")
	if len(*opened) != 1 || (*opened)[0] != prURL {
		t.Fatalf("launched %q, want exactly %s", *opened, prURL)
	}
	if o := openedMsgs(msgs); len(o) != 1 || o[0].URL != prURL {
		t.Fatalf("OpenedMsg = %+v", o)
	}
	if !strings.Contains(toastText(m), "opened "+prURL) {
		t.Fatalf("toast = %q", toastText(m))
	}
}

func TestOOnAMessageWithSeveralLinksOpensTheFirstAndSaysHowMany(t *testing.T) {
	opened := stubLauncher(t)
	m, _ := keyRun(t, messageModel(t, "see "+prURL+" and https://example.com/b"), "o")
	if len(*opened) != 1 || (*opened)[0] != prURL {
		t.Fatalf("launched %q, want the first, %s", *opened, prURL)
	}
	if !strings.Contains(toastText(m), "opened "+prURL+" (the first of 2 links)") {
		t.Fatalf("toast = %q", toastText(m))
	}
}

func TestOOnAMessageWithoutALinkSaysSo(t *testing.T) {
	for _, body := range []string{"no link here", "javascript:alert(1) file:///etc/passwd"} {
		opened := stubLauncher(t)
		m, msgs := keyRun(t, messageModel(t, body), "o")
		if len(*opened) != 0 || len(openedMsgs(msgs)) != 0 {
			t.Fatalf("%q: launched %q", body, *opened)
		}
		if !strings.Contains(toastText(m), "no link in this message") {
			t.Fatalf("%q: toast = %q", body, toastText(m))
		}
	}
}

func TestOpenLinkIsInTheMessageFooterAndHelp(t *testing.T) {
	if link.Action != "link.open" {
		t.Fatalf("the dispatcher matches %q; link.Action is %q", "link.open", link.Action)
	}
	m := messageModel(t, "x")
	if !strings.Contains(footerOf(m), "open link") {
		t.Fatalf("footer = %q", footerOf(m))
	}
	help := strings.Join(keys.HelpLines(m.helpSections(), 200, keys.HelpStyle{}), "\n")
	if !strings.Contains(help, "open the link in the message") {
		t.Fatalf("help lacks the open-link key:\n%s", help)
	}
}
