package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func jamoKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestNormalizeCJKKeyMapsJamoByPhysicalPosition(t *testing.T) {
	for jamo, want := range map[string]string{
		"ㅂ": "q", "ㅁ": "a", "ㅋ": "z", "ㅓ": "j", "ㅏ": "k",
		"ㅃ": "Q", "ㄲ": "R",
	} {
		if got := normalizeCJKKey(jamoKey(jamo)).String(); got != want {
			t.Errorf("normalizeCJKKey(%q) = %q, want %q", jamo, got, want)
		}
	}
}

func TestNormalizeCJKKeyLeavesEverythingElseAlone(t *testing.T) {
	// Latin keys, digits, and composed syllables pass through: a composed
	// syllable only reaches the TUI as committed text, never as a shortcut.
	for _, k := range []string{"q", "R", "0", "가"} {
		if got := normalizeCJKKey(jamoKey(k)).String(); got != k {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", k, got)
		}
	}
	for _, in := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyCtrlC}, {Type: tea.KeyEsc}} {
		if got := normalizeCJKKey(in); got.String() != in.String() {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", in.String(), got.String())
		}
	}
}

func TestNormalizeCJKKeyLeavesPasteAndAltAlone(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Paste: true}
	if got := normalizeCJKKey(paste); got.String() != paste.String() {
		t.Errorf("pasted jamo was rewritten to %q", got.String())
	}
	alt := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Alt: true}
	if got := normalizeCJKKey(alt); got.String() != alt.String() {
		t.Errorf("alt chord was rewritten to %q", got.String())
	}
}

// Under a Korean input source every shortcut arrives as a jamo. They have to
// map back, or the monitor is unusable until the input source is switched.
func TestHangulShortcutsFireLikeLatin(t *testing.T) {
	t.Run("zoom", func(t *testing.T) {
		// `ㅋ` is the physical `z`, which zooms the focused pane.
		m := press(testModel(3, 3), "ㅋ")
		if m.zoom != m.focus {
			t.Fatalf("zoom = %v, focus = %v; ㅋ (physical z) did not zoom", m.zoom, m.focus)
		}
	})
	t.Run("scrolling", func(t *testing.T) {
		// `ㅓ` is the physical `j`. The radio pane starts focused, so this
		// scrolls it; the assertion is only that the jamo reached the same
		// handler `j` does.
		viaJamo := press(testModel(3, 20), "ㅓ")
		viaLatin := press(testModel(3, 20), "j")
		if viaJamo.radioCur != viaLatin.radioCur {
			t.Fatalf("ㅓ left the radio cursor at %d, but j leaves it at %d",
				viaJamo.radioCur, viaLatin.radioCur)
		}
	})
	t.Run("quit", func(t *testing.T) {
		// `ㅂ` sits on the physical `q` key.
		m := press(testModel(3, 3), "ㅂ")
		if !m.quit {
			t.Fatal("ㅂ (physical q) did not quit")
		}
	})
}

// The picker and the detail view have their own handlers, reached before the
// main switch, so normalization has to happen above all three.
func TestHangulReachesTheOtherHandlers(t *testing.T) {
	t.Run("picker", func(t *testing.T) {
		m := testModel(3, 3)
		m.picker = true
		if m2 := press(m, "ㅂ"); !m2.quit {
			t.Fatal("ㅂ (physical q) did not quit from the run picker")
		}
	})
	t.Run("detail", func(t *testing.T) {
		m := testModel(3, 20)
		m.detail = detailMessage
		// In detail, `q` closes rather than quits — the layering has to
		// survive normalization too.
		if m2 := press(m, "ㅂ"); m2.detail != detailClosed {
			t.Fatal("ㅂ (physical q) did not close the detail view")
		}
	})
}

// ctrl+c must keep quitting: it is not a rune message, so normalization has to
// leave it alone on the way through.
func TestCtrlCStillQuits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*tuiModel)
	}{
		{"main view", func(*tuiModel) {}},
		{"picker", func(m *tuiModel) { m.picker = true }},
		{"detail", func(m *tuiModel) { m.detail = detailMessage }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(3, 20)
			tc.setup(&m)
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if !next.(tuiModel).quit {
				t.Fatalf("ctrl+c did not quit from %s", tc.name)
			}
		})
	}
}
