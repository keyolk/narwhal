package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/toast"

	"github.com/keyolk/narwhal/internal/broker"
)

// keyMsgOf builds the message Bubble Tea sends for a key string.
func keyMsgOf(t *testing.T, k string) tea.KeyMsg {
	t.Helper()
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"backspace": tea.KeyBackspace, " ": tea.KeySpace, "shift+left": tea.KeyShiftLeft, "shift+right": tea.KeyShiftRight,
		"ctrl+c": tea.KeyCtrlC, "ctrl+d": tea.KeyCtrlD, "ctrl+u": tea.KeyCtrlU, "ctrl+e": tea.KeyCtrlE,
		"ctrl+n": tea.KeyCtrlN, "ctrl+p": tea.KeyCtrlP,
	}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	if kt, ok := named[k]; ok {
		msg = tea.KeyMsg{Type: kt}
		if k == " " {
			msg.Runes = []rune{' '}
		}
	}
	if msg.String() != k {
		t.Fatalf("test helper cannot build key %q (got %q)", k, msg.String())
	}
	return msg
}

// One key, one action per context; nothing inner hides quit or help; and
// every action a Korean input source would need has a key it can type (the
// run's G has end, Questions' S has s). The kit judges reachability per
// action, so an uppercase key with a reachable partner is not reported.
func TestKeymapHasNoConflicts(t *testing.T) {
	for _, c := range defaultKeymap.Conflicts(keyStacks...) {
		t.Error(c)
	}
}

// Every key the help lists for a screen runs that action on that screen:
// the reference is the dispatcher's own table, checked key by key.
func TestEveryHelpEntryIsReachable(t *testing.T) {
	km := defaultKeymap
	for _, stack := range keyStacks {
		for _, sec := range km.Help(keys.Opts{}, stack) {
			for _, e := range sec.Entries {
				for _, k := range e.Keys {
					b, ok := km.ResolveRaw(stack, keyMsgOf(t, k), keys.Opts{})
					if !ok || b.Action != e.Action {
						t.Errorf("%v: help says %q runs %s, but it resolves to %q", stack, k, e.Action, b.Action)
					}
				}
			}
		}
	}
}

// Every action in the table is reachable on some screen and handled by a
// dispatcher: a name in the table with no case for it would be a key that
// the footer offers and that does nothing.
func TestEveryActionIsDocumentedAndHandled(t *testing.T) {
	documented := map[string]bool{}
	for _, sec := range defaultKeymap.Help(keys.Opts{}, keyStacks...) {
		for _, e := range sec.Entries {
			documented[e.Action] = true
		}
	}
	var src strings.Builder
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "keymap") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	for _, b := range defaultKeymap.Bindings() {
		if !documented[b.Action] {
			t.Errorf("%s (%s) is not reachable on any screen", b.Action, b.Ctx)
		}
		if !regexp.MustCompile(`case [^:\n]*"` + regexp.QuoteMeta(b.Action) + `"`).MatchString(src.String()) {
			t.Errorf("%s has no case in any dispatcher", b.Action)
		}
	}
}

// keys.yaml rebinds by action or by context; a rebinding that would hide
// help is refused and the defaults kept.
func TestKeysYAMLOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := keys.ConfigPath(prefsApp)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("run.zoom: [Z, x]\n")
	km, err := defaultKeymap.LoadOverrides(path, keyStacks...)
	if err != nil {
		t.Fatal(err)
	}
	m := testModel(3, 3)
	m.km = km
	if m = press(m, "x"); m.zoom != m.focus {
		t.Error("x did not zoom after rebinding")
	}
	if !strings.Contains(footerOf(m), "Z unzoom") {
		t.Errorf("the footer does not show the new key: %q", footerOf(m))
	}

	write("help: [j]\n")
	if _, err := defaultKeymap.LoadOverrides(path, keyStacks...); err == nil {
		t.Error("binding help to j, which the panes claim, was accepted")
	}
}

// The layout survives a restart: written debounced off the UI loop, and
// flushed before quitting.
func TestLayoutIsSavedAndRestored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	m := testModel(3, 3)
	m.enablePrefs()
	m = press(m, "1", "b", ">", "2")
	if !m.saver.Pending() {
		t.Fatal("changing the layout did not schedule a save")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = next.(tuiModel)
	saved, ok := cmd().(prefs.Saved)
	if !ok || saved.Err != nil || !saved.Final {
		t.Fatalf("quit did not flush the layout: %+v", saved)
	}
	if _, cmd := m.Update(saved); cmd == nil {
		t.Error("the final save did not quit")
	}

	r := testModel(3, 3)
	r.enablePrefs()
	if r.focus != focusNode || r.boxMode || r.widthDelta != 4 {
		t.Errorf("restored focus=%v boxMode=%v widthDelta=%d", r.focus, r.boxMode, r.widthDelta)
	}
}

// Without prefs (tests, the panes w opens) nothing is written and quit is
// immediate.
func TestNoPrefsQuitsAtOnce(t *testing.T) {
	m := testModel(3, 3)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q without prefs did not quit")
	}
}

// What became of an instruction is a toast that expires on its own tick,
// not a string that waits for some other message to redraw.
func TestSteerResultIsAToastThatExpires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := testModel(3, 3)
	m.width, m.height = 100, 24
	next, cmd := m.Update(steerSentMsg{text: "check the lock"})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("no expiry tick")
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "✓ sent: check the lock") {
		t.Fatalf("no toast:\n%s", got)
	}
	items := m.toasts.Items()
	exp, ok := m.toasts.ExpiredMsg(items[len(items)-1].ID())
	if !ok {
		t.Fatal("the toast has no tick")
	}
	next, _ = m.Update(exp)
	if got := ansi.Strip(next.(tuiModel).View()); strings.Contains(got, "check the lock") && strings.Contains(got, "✓ sent") {
		t.Error("the toast outlived its tick")
	}

	// A failure stays until a key after it has been read.
	next, _ = m.Update(steerSentMsg{text: "x", err: os.ErrDeadlineExceeded})
	m = next.(tuiModel)
	m.toasts = toast.WithClock(m.toasts, func() time.Time { return time.Now().Add(time.Second) })
	if !strings.Contains(m.noticeLine(100), "not sent") {
		t.Fatal("no error toast")
	}
	m = press(m, "j")
	if strings.Contains(m.noticeLine(100), "not sent") {
		t.Error("the error stayed after a key")
	}
}

// : finds an action by name and runs it on the screen underneath.
func TestPaletteRunsAnAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := testModel(3, 3)
	m = press(m, ":", "r", "e", "s", "u", "l", "t")
	if !m.palette {
		t.Fatal("palette not open")
	}
	if es := m.paletteEntries(); len(es) == 0 || es[0].Action != "run.result" {
		t.Fatalf("top match for 'result': %+v", es)
	}
	m = press(m, "enter")
	if m.palette || m.detail != detailResult {
		t.Errorf("palette=%v detail=%v — enter did not run the result action", m.palette, m.detail)
	}
}

// Typed into the palette, q is a letter, not quit.
func TestPaletteTakesLetters(t *testing.T) {
	m := press(testModel(3, 3), ":", "q")
	if m.quit || string(m.paletteQuery) != "q" {
		t.Errorf("quit=%v query=%q", m.quit, string(m.paletteQuery))
	}
}

// Below 70 columns the focused pane fills the body, and the header says
// why the other is gone.
func TestNarrowTerminalZoomsTheFocusedPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := testModel(3, 3)
	m.width, m.height = 60, 24
	if m.effectiveZoom() != m.focus {
		t.Fatalf("effective zoom %v at 60 cols", m.effectiveZoom())
	}
	if got := ansi.Strip(m.viewHeader()); !strings.Contains(got, "narrow terminal") {
		t.Errorf("header does not say why: %q", got)
	}
	m.width = 100
	if m.effectiveZoom() != -1 {
		t.Error("zoomed at 100 cols")
	}
}

// The band is there only when something waits on the operator.
func TestNeedsYouBandOnlyWhenActionable(t *testing.T) {
	m := testModel(3, 3)
	if b := m.needsYouBand(100); b != "" {
		t.Errorf("band with nothing to do: %q", b)
	}
	m.snap.Tasks[1].State = broker.TaskFailed
	if b := ansi.Strip(m.needsYouBand(100)); !strings.Contains(b, "needs you") || !strings.Contains(b, "1 task failed") {
		t.Errorf("band: %q", b)
	}
	if got := ansi.Strip(m.viewTasks(40, 10)); !strings.Contains(got, "Graph ✗1") {
		t.Errorf("graph title has no badge:\n%s", got)
	}
}

// A numbered action is one binding on 1…9: the footer and the help show it
// as "1-9", the handler counts from the key's position (so a rebinding to
// other keys still picks the first thing with the first key), and the
// palette, which cannot give it a number, leaves it out.
func TestNumberedKeysShowAsARange(t *testing.T) {
	m := testModel(3, 3)
	m.width, m.height = 160, 30
	m.detail = detailResult
	if f := footerOf(m); !strings.Contains(f, "1-9 open a PR or file") {
		t.Errorf("result footer: %q", f)
	}
	found := false
	for _, sec := range defaultKeymap.Help(keys.Opts{}, m.screenStack()) {
		for _, e := range sec.Entries {
			if e.Action == "result.artifact" {
				found = e.KeyLabel == "1-9"
			}
		}
	}
	if !found {
		t.Error("the help does not list result.artifact as 1-9")
	}
	b, ok := defaultKeymap.Resolve(m.screenStack(), keyMsgOf(t, "3"), keys.Opts{})
	if !ok || b.Action != "result.artifact" || b.KeyIndex(keyMsgOf(t, "3"))+1 != 3 {
		t.Errorf("3 resolves to %q at index %d", b.Action, b.KeyIndex(keyMsgOf(t, "3")))
	}
	for _, e := range m.paletteEntries() {
		if e.Action == "result.artifact" || e.Action == "palette" {
			t.Errorf("the palette offers %s", e.Action)
		}
	}
}
