// uistate.go keeps the layout a person set up across restarts: which pane
// had the keys, which tab the right column showed, boxes or lanes, the
// graph's width and radio or brief. Everything else (cursors, scroll) is
// about one run and starts fresh.
//
// It lives in $XDG_STATE_HOME/narwhal/ui.json, written by the kit's Saver:
// debounced, atomic, off the UI loop, and flushed before quitting.
package main

import (
	"github.com/keyolk/tuikit/prefs"
)

const prefsApp = "narwhal"

// uiState is what is saved. Panes are stored by name, not by the
// focusPane constant, so reordering the constants cannot reinterpret a
// saved file.
type uiState struct {
	Focus      string `json:"focus"`
	Reader     string `json:"reader"`
	BoxMode    bool   `json:"box_mode"`
	WidthDelta int    `json:"width_delta"`
	BriefTab   bool   `json:"brief_tab"`
}

var paneNames = map[focusPane]string{focusTasks: "graph", focusNode: "task", focusTimeline: "timeline", focusRadio: "radio"}

func paneByName(name string, def focusPane) focusPane {
	for p, n := range paneNames {
		if n == name {
			return p
		}
	}
	return def
}

func (m tuiModel) uiState() uiState {
	return uiState{
		Focus:      paneNames[m.focus],
		Reader:     paneNames[m.reader],
		BoxMode:    m.boxMode,
		WidthDelta: m.widthDelta,
		BriefTab:   m.briefTab,
	}
}

// defaultUIState is a fresh model's layout.
func defaultUIState() uiState {
	return newTUIModel(nil, 0, 0, true).uiState()
}

// applyUIState restores s over a fresh model.
func (m *tuiModel) applyUIState(s uiState) {
	m.focus = paneByName(s.Focus, m.focus)
	m.reader = paneByName(s.Reader, m.reader)
	m.boxMode = s.BoxMode
	m.widthDelta = s.WidthDelta
	m.briefTab = s.BriefTab
}

// enablePrefs restores the saved layout and turns saving on. A file that
// cannot be read costs the layout, not the monitor: the error is a toast.
func (m *tuiModel) enablePrefs() {
	s, err := prefs.Load(prefsApp, defaultUIState())
	m.applyUIState(s)
	if err != nil {
		m.notifyFrom(srcPrefs, "", err)
	}
	m.saver = prefs.NewSaver[uiState](prefsApp, 0)
	m.prefsOn = true
}
