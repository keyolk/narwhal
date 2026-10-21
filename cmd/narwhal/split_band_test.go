package main

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/split"
)

// Positions come from the rendered View: the divider is the column of the
// band's top edge "┬", checked against the "│" (or "┃" while dragged) every
// body row draws there. Nothing is recomputed with the layout under test.

// cellsOfLine splits a stripped line into one string per terminal cell.
func cellsOfLine(line string) []string {
	var out []string
	for _, r := range line {
		out = append(out, string(r))
		for i := 1; i < ansi.StringWidth(string(r)); i++ {
			out = append(out, "")
		}
	}
	return out
}

// divider is the band's divider column and the rows of its body, or x -1
// when no two-column band is on screen.
func divider(m tuiModel) (x, top, bottom int) {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for y, l := range lines {
		cs := cellsOfLine(l)
		if len(cs) == 0 || cs[0] != "╭" {
			continue
		}
		for i, c := range cs {
			if c == "┬" {
				x, top = i, y
			}
		}
		if top != y {
			return -1, 0, 0
		}
		for b := y + 1; b < len(lines); b++ {
			bc := cellsOfLine(lines[b])
			if len(bc) > 0 && bc[0] == "╰" {
				bottom = b
				break
			}
			if x >= len(bc) || (bc[x] != "│" && bc[x] != "┃") {
				return -1, 0, 0
			}
		}
		return x, top, bottom
	}
	return -1, 0, 0
}

func mustDivider(t *testing.T, m tuiModel) (x, y int) {
	t.Helper()
	x, top, bottom := divider(m)
	if x < 0 || bottom-top < 3 {
		t.Fatalf("no band divider on screen:\n%s", ansi.Strip(m.View()))
	}
	return x, top + 2
}

func dividerAt(t *testing.T, m tuiModel) int {
	t.Helper()
	x, _ := mustDivider(t, m)
	return x
}

func TestDraggingTheRunDividerMovesTheColumns(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	x0, y := mustDivider(t, m)
	m = lpress(m, x0, y)
	if !m.splits[bandRun].Dragging() {
		t.Fatal("a press on the divider starts a drag")
	}
	if !strings.Contains(screenRow(m, y), "┃") {
		t.Error("the divider is drawn heavy while dragged")
	}
	m = lmotion(m, x0+10, y)
	m = lrelease(m, x0+10, y)
	if got := dividerAt(t, m); got != x0+10 {
		t.Fatalf("divider at %d after dragging from %d to %d", got, x0, x0+10)
	}
	if len(*c) != 0 || len(highlighted(m)) != 0 {
		t.Fatalf("a divider drag selected text: copies %q", *c)
	}
	// The selection's panes follow the drawn divider.
	for _, p := range m.runPanes() {
		switch p.ID {
		case paneGraph:
			if p.Rect.X+p.Rect.W != x0+10 {
				t.Errorf("graph pane ends at %d, divider at %d", p.Rect.X+p.Rect.W, x0+10)
			}
		case paneReader:
			if p.Rect.X != x0+12 {
				t.Errorf("reader pane starts at %d, want %d (divider and its air)", p.Rect.X, x0+12)
			}
		}
	}
	// A cell the graph gained is the graph's for a click.
	m.focus = focusRadio
	m = send(m, mouseMsg(x0+5, y+1, tea.MouseButtonLeft, tea.MouseActionPress))
	if m.focus != focusTasks {
		t.Fatalf("a click left of the moved divider focused %v, want the graph", m.focus)
	}
}

func TestDividerPressNeverSelectsButAReaderPressStill(t *testing.T) {
	c := captureCopies(t)
	m := runMouseModel(t, false)
	x0, y := mustDivider(t, m)
	m = dragFrom(m, x0, y, x0, y+4)
	if len(*c) != 0 || len(highlighted(m)) != 0 {
		t.Fatalf("a press on the divider started a selection (copies %q)", *c)
	}
	x, ry := cellOf(t, m.View(), "message body")
	m = dragFrom(m, x, ry, x+6, ry)
	if !strings.Contains(lastCopy(c), "messag") {
		t.Fatalf("a drag in the reader no longer selects: %q", lastCopy(c))
	}
}

func TestDoubleClickOnTheRunDividerResets(t *testing.T) {
	m := runMouseModel(t, false)
	x0 := dividerAt(t, m)
	m = press(m, ">", ">")
	moved, y := mustDivider(t, m)
	if moved <= x0 {
		t.Fatal("setup: > should move the divider")
	}
	m = lpress(m, moved, y)
	m = lrelease(m, moved, y)
	m = lpress(m, moved, y)
	if got := dividerAt(t, m); got != x0 {
		t.Fatalf("double click left the divider at %d, want %d", got, x0)
	}
	if m.splitSet[bandRun] {
		t.Error("a reset run split should follow the width again")
	}
}

func TestSplitKeysMoveEachBand(t *testing.T) {
	run := runMouseModel(t, false)
	flow := flowModel(t)
	flow.openFlow("p1", "b2")
	out := press(withOutputs(t), "o")
	if !out.outputsView {
		t.Fatal("setup: Outputs is not open")
	}
	for name, m := range map[string]tuiModel{"run": run, "flow": flow, "outputs": out} {
		x0 := dividerAt(t, m)
		m = press(m, ">")
		grown := dividerAt(t, m)
		if grown <= x0 {
			t.Errorf("%s: > left the divider at %d (was %d)", name, grown, x0)
		}
		m = press(m, "<", "<")
		if got := dividerAt(t, m); got >= x0 {
			t.Errorf("%s: < < left the divider at %d (default %d)", name, got, x0)
		}
		m = press(m, "=")
		if got := dividerAt(t, m); got != x0 {
			t.Errorf("%s: = left the divider at %d, want %d", name, got, x0)
		}
		// Low priority: the hint is there once the footer has the room. In
		// the run view it is the graph's, as it was.
		m.width = 400
		if name == "run" {
			if f := footerOf(m); strings.Contains(f, "width") {
				t.Errorf("run: width hint shown with the radio focused: %q", f)
			}
			m.focus = focusTasks
		}
		if f := footerOf(m); !strings.Contains(f, "width") {
			t.Errorf("%s: footer has no width hint: %q", name, f)
		}
	}
}

func TestSplitKeysAreInHelpAndDoNotConflict(t *testing.T) {
	for _, c := range defaultKeymap.Conflicts(keyStacks...) {
		t.Error(c)
	}
	for _, ctx := range []keys.Ctx{ctxRun, ctxFlow, ctxOutputs} {
		var stack []keys.Ctx
		for _, s := range keyStacks {
			if slices.Contains(s, ctx) && stack == nil {
				stack = s
			}
		}
		for _, k := range []string{"<", ">", "="} {
			b, ok := defaultKeymap.Resolve(stack, keyMsgOf(t, k), keys.Opts{})
			if !ok || !strings.HasPrefix(b.Action, "split.") {
				t.Errorf("%s: %q resolves to %q", ctx, k, b.Action)
			}
		}
	}
	m := runMouseModel(t, false)
	m.height = 200
	m = press(m, "?")
	help := ansi.Strip(m.View())
	for _, want := range []string{"wider left column", "narrower left column", "columns back to the default widths"} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

// The old width_delta grew past the clamp with nothing moving, and the next
// press back did nothing either. Every write clamps now.
func TestPressesPastTheLimitDoNotBank(t *testing.T) {
	for _, box := range []bool{false, true} {
		m := runMouseModel(t, box)
		for range 40 {
			m = press(m, ">")
		}
		top := dividerAt(t, m)
		m = press(m, "<")
		if got := dividerAt(t, m); got >= top {
			t.Errorf("box=%v: one < after the limit left the divider at %d (limit %d)", box, got, top)
		}
		for range 40 {
			m = press(m, "<")
		}
		bottom := dividerAt(t, m)
		m = press(m, ">")
		if got := dividerAt(t, m); got <= bottom {
			t.Errorf("box=%v: one > after the lower limit left the divider at %d (limit %d)", box, got, bottom)
		}
	}
}

func prefsModel(t *testing.T) tuiModel {
	t.Helper()
	m := runMouseModel(t, false)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m.enablePrefs()
	return send2(m, tea.WindowSizeMsg{Width: 100, Height: 28})
}

func quitAndFlush(t *testing.T, m tuiModel) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = next.(tuiModel)
	if saved, ok := cmd().(prefs.Saved); !ok || saved.Err != nil || !saved.Final {
		t.Fatalf("quit did not flush the layout: %+v", saved)
	}
}

func writeUI(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), prefsApp)
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "ui.json"), []byte(body), 0o644))
}

func TestRunSplitSurvivesARestart(t *testing.T) {
	m := prefsModel(t)
	m = press(m, ">", ">")
	want := dividerAt(t, m)
	quitAndFlush(t, m)

	r := runMouseModel(t, false)
	r.enablePrefs()
	r = send2(r, tea.WindowSizeMsg{Width: 100, Height: 28})
	if got := dividerAt(t, r); got != want {
		t.Fatalf("restored divider at %d, want %d", got, want)
	}
}

func TestGarbageSplitInUIJSONKeepsTheDefault(t *testing.T) {
	def := dividerAt(t, runMouseModel(t, false))
	for _, body := range []string{`{"run_split":{"ratio":40}}`, `{"run_split":{"ratio":-1}}`, `{"run_split":{"ratio":1}}`} {
		m := runMouseModel(t, false)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		writeUI(t, body)
		m.enablePrefs()
		if got := dividerAt(t, m); got != def {
			t.Errorf("%s: divider at %d, want the default %d", body, got, def)
		}
	}
}

// width_delta 12 at 100 columns drew the graph 100/3+12 = 45 columns wide,
// after the frame's border and air: the divider at column 47.
func TestWidthDeltaIsMigratedOnceAtTheFirstSize(t *testing.T) {
	m := runMouseModel(t, false)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeUI(t, `{"focus":"radio","width_delta":12}`)
	m.width, m.height = 0, 0
	m.enablePrefs()
	if m.legacyWidthDelta != 12 {
		t.Fatalf("setup: the delta waits for a size, got %d", m.legacyWidthDelta)
	}
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	m = next.(tuiModel)
	if cmd == nil {
		t.Error("the migrated split was not scheduled for saving")
	}
	if got := dividerAt(t, m); got != 47 {
		t.Fatalf("migrated divider at %d, want 47 (graph 45 columns)", got)
	}
	// The next size does not apply the delta again.
	m = send2(m, tea.WindowSizeMsg{Width: 160, Height: 28})
	ratio := m.splits[bandRun].Ratio()
	if math.Abs(ratio-45.0/95) > 1e-9 {
		t.Fatalf("ratio %v after a resize, want the migrated 45/95", ratio)
	}
	quitAndFlush(t, m)
	s, err := prefs.Load(prefsApp, uiState{})
	if err != nil {
		t.Fatal(err)
	}
	if s.WidthDelta != 0 || math.Abs(s.RunSplit.Ratio-45.0/95) > 1e-9 {
		t.Fatalf("saved %+v, want run_split 45/95 and no width_delta", s)
	}
	raw, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), prefsApp, "ui.json"))
	if strings.Contains(string(raw), "width_delta") {
		t.Fatalf("ui.json still has width_delta:\n%s", raw)
	}
}

// Unmoved, the graph follows the width with its old cap: 44 columns at 200.
func TestAnUnmovedRunSplitKeepsTheWideScreenCap(t *testing.T) {
	m := runMouseModel(t, false)
	m.width = 200
	if got := dividerAt(t, m); got != 2+44 {
		t.Fatalf("graph divider at %d on 200 columns, want 46 (the 44-column cap)", got)
	}
	m = press(m, ">", "=")
	if got := dividerAt(t, m); got != 46 {
		t.Fatalf("= left the divider at %d, want 46", got)
	}
}

func TestNarrowTerminalsKeepTheCollapseRulesAndFit(t *testing.T) {
	type view struct {
		name string
		m    func() tuiModel
	}
	views := []view{
		{"run", func() tuiModel { return runMouseModel(t, true) }},
		{"flow", func() tuiModel { m := flowModel(t); m.openFlow("p1", "b2"); return m }},
		{"outputs", func() tuiModel { return press(withOutputs(t), "o") }},
	}
	for _, v := range views {
		for _, size := range []struct{ w, h int }{{60, 24}, {80, 24}, {45, 24}} {
			m := v.m()
			m.width, m.height = size.w, size.h
			for range 20 {
				m = press(m, ">")
			}
			for i, line := range strings.Split(m.View(), "\n") {
				// Under 60 columns the flow's head line was never cut to
				// the width (before this change too); only the band is
				// checked there.
				if size.w < 60 && i == 0 {
					continue
				}
				if w := ansi.StringWidth(line); w > size.w {
					t.Errorf("%s %dx%d: line %d is %d cells wide", v.name, size.w, size.h, i, w)
				}
			}
			x, _, _ := divider(m)
			// The run view zooms under 70 columns; flow needs 54 for both
			// columns and Outputs 43.
			wantBand := map[string]bool{"run": size.w >= 70, "flow": size.w >= 54, "outputs": size.w >= 43}[v.name]
			if (x >= 0) != wantBand {
				t.Errorf("%s %dx%d: two columns drawn = %v, want %v", v.name, size.w, size.h, x >= 0, wantBand)
			}
		}
	}
}

func TestSplitBindingsAreTheKits(t *testing.T) {
	var got []string
	for _, b := range splitBindings(ctxRun, grpLayout, 5) {
		got = append(got, b.Action+"="+strings.Join(b.Keys, ","))
	}
	want := []string{split.ActionGrow + "=>", split.ActionShrink + "=<", split.ActionReset + "=="}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("bindings %v, want %v", got, want)
	}
}
