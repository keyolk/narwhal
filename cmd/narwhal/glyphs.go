// glyphs.go picks the icon set for task state and message priority.
//
// Nerd Font icons read better than ASCII at a glance — a filled check and a
// spinner-like circle carry state faster than "✓" and "▶". But a terminal
// without a patched font renders them as tofu boxes, which is worse than
// plain Unicode. So the set is chosen at startup and can be forced either
// way.
//
// Detection is deliberately conservative: assume no Nerd Font unless the
// environment says otherwise. A false negative costs slightly plainer
// icons; a false positive fills the pane with boxes.
package main

import (
	"os"
	"strings"
)

// iconSet is the glyph vocabulary for one rendering mode.
type iconSet struct {
	taskCompleted  string
	taskDispatched string
	taskReady      string
	taskFailed     string
	taskBlocked    string
	taskPending    string

	prioUrgent string
	prioFYI    string
	prioNormal string

	runActive   string
	runDone     string
	runFailed   string
	runCanceled string
}

// nerdIcons uses Nerd Font private-use codepoints.
var nerdIcons = iconSet{
	taskCompleted:  "", // nf-fa-check
	taskDispatched: "", // nf-fa-refresh
	taskReady:      "", // nf-fa-circle
	taskFailed:     "", // nf-fa-times
	taskBlocked:    "", // nf-fa-ban
	taskPending:    "", // nf-fa-clock_o

	prioUrgent: "", // nf-fa-warning
	prioFYI:    "", // nf-fa-info_circle
	prioNormal: "", // nf-fa-comment

	runActive:   "",
	runDone:     "",
	runFailed:   "",
	runCanceled: "",
}

// unicodeIcons is the portable fallback: BMP symbols every modern terminal
// font ships.
var unicodeIcons = iconSet{
	taskCompleted:  "✓",
	taskDispatched: "▶",
	taskReady:      "○",
	taskFailed:     "✗",
	taskBlocked:    "⊘",
	taskPending:    "·",

	prioUrgent: "!",
	prioFYI:    "i",
	prioNormal: "·",

	runActive:   "▶",
	runDone:     "✓",
	runFailed:   "✗",
	runCanceled: "⊘",
}

// icons is the active set, resolved once at startup.
var icons = resolveIcons()

// resolveIcons decides which set to use.
//
//	NARWHAL_ICONS=nerd|unicode   explicit override, wins over everything
//	TERM_PROGRAM / font hints    terminals that ship a patched font
//
// Everything else falls back to plain Unicode.
func resolveIcons() iconSet {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NARWHAL_ICONS"))) {
	case "nerd":
		return nerdIcons
	case "unicode", "ascii", "plain":
		return unicodeIcons
	}
	if hasNerdFont() {
		return nerdIcons
	}
	return unicodeIcons
}

// hasNerdFont guesses whether the terminal can render Nerd Font glyphs.
//
// There is no reliable query for this, so we look for the signals that do
// exist: a font name that says so, or a terminal that ships one by default.
// Ghostty and WezTerm both bundle a patched font; Kitty and Alacritty do
// not, so they are left out even though many users configure one — the
// override exists for that case.
func hasNerdFont() bool {
	for _, key := range []string{"NARWHAL_FONT", "TERM_FONT"} {
		if isNerdFontName(os.Getenv(key)) {
			return true
		}
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return true
	}
	return false
}

func isNerdFontName(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "nerd") || strings.Contains(n, "nf-")
}
