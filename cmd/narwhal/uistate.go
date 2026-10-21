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
	"github.com/keyolk/tuikit/split"
)

const prefsApp = "narwhal"

// uiState is what is saved. Panes are stored by name, not by the
// focusPane constant, so reordering the constants cannot reinterpret a
// saved file.
type uiState struct {
	Focus    string `json:"focus"`
	Reader   string `json:"reader"`
	BoxMode  bool   `json:"box_mode"`
	BriefTab bool   `json:"brief_tab"`
	// The column boundaries the person moved, as fractions (split_band.go);
	// an unmoved view saves nothing and follows the width.
	RunSplit     split.State `json:"run_split,omitzero"`
	FlowSplit    split.State `json:"flow_split,omitzero"`
	OutputsSplit split.State `json:"outputs_split,omitzero"`
	// WidthDelta is the graph width adjustment in columns an older narwhal
	// saved. It is read to migrate (migrateWidthDelta) and kept only until
	// the first size turns it into RunSplit.
	WidthDelta int `json:"width_delta,omitzero"`
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
		Focus:        paneNames[m.focus],
		Reader:       paneNames[m.reader],
		BoxMode:      m.boxMode,
		BriefTab:     m.briefTab,
		RunSplit:     m.splitState(bandRun),
		FlowSplit:    m.splitState(bandFlow),
		OutputsSplit: m.splitState(bandOutputs),
		WidthDelta:   m.legacyWidthDelta,
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
	m.briefTab = s.BriefTab
	m.restoreSplit(bandRun, s.RunSplit)
	m.restoreSplit(bandFlow, s.FlowSplit)
	m.restoreSplit(bandOutputs, s.OutputsSplit)
	m.legacyWidthDelta = s.WidthDelta
	// A size already known (a restore after the first WindowSizeMsg)
	// migrates at once.
	m.migrateWidthDelta()
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
}
