// overlays.go draws the two things that open over a screen rather than
// replacing it: the `?` key reference and the `:` action palette.
//
// Help used to be a screen of its own. Opening it hid what you had asked
// about — the pane, the cursor, the footer — and closing it was the only
// way to look again. It now paints over the dimmed frame, and both it and
// the palette are read from the binding table (keymap.go), so they list
// exactly what the keys do here, innermost context first.
package main

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/input"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/render"
)

var overlayBox = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("6")).
	Padding(0, 1)

// overlayGeom is an overlay's size: the box, and the text inside it.
type overlayGeom struct {
	boxW, boxH     int
	innerW, innerH int
}

func (m tuiModel) overlayGeom(maxW, maxH int) overlayGeom {
	g := overlayGeom{boxW: min(maxW, m.width-2), boxH: min(maxH, m.height-2)}
	// Two border cells and one of padding each side; two border rows.
	g.innerW, g.innerH = max(1, g.boxW-4), max(1, g.boxH-2)
	return g
}

// overlay paints box over the dimmed screen. On a terminal too small for a
// box with a frame around it, the box is the screen.
func (m tuiModel) overlay(base string, lines []string, g overlayGeom) string {
	box := overlayBox.Render(strings.Join(lines, "\n"))
	if m.width < 24 || m.height < 8 {
		return render.FitLines(strings.Join(lines, "\n"), max(1, m.width), max(1, m.height))
	}
	return render.Center(render.Dim(base), box, m.width, m.height)
}

// helpIntro is the reference's orientation: where the screens are, not
// keys — those come from the table below it.
const helpIntro = "home → flow → run → task: enter digs in, esc backs out. Keys for this screen come first."

// helpSections are the keys of the screen under the overlay first, then
// every other screen's, each action once.
func (m tuiModel) helpSections() []keys.Section {
	stacks := append([][]keys.Ctx{m.screenStack()}, keyStacks...)
	// No State: the reference describes each key, not the label it wears
	// on one screen right now ("→ brief").
	return m.keymap().Help(keys.Opts{}, stacks...)
}

// helpRows is the rows the key list has inside the help box: the box less
// its title, intro, a blank and its footer.
func (m tuiModel) helpGeom() (overlayGeom, int) {
	g := m.overlayGeom(100, m.height)
	return g, max(1, g.innerH-4)
}

func (m tuiModel) viewHelpOverlay(base string) string {
	lines, g := m.helpBox()
	return m.overlay(base, lines, g)
}

// helpBox is the help overlay's text, without the frame.
func (m tuiModel) helpBox() ([]string, overlayGeom) {
	g, rows := m.helpGeom()
	list, _ := keys.RenderHelp(m.helpSections(), g.innerW, rows, m.helpOff, helpStyle)
	lines := []string{
		styCyanBold.Render("Narwhal monitor keys") + styDim.Render(render.Truncate("  · rebind in "+abbreviatePath(keys.ConfigPath(prefsApp)), max(0, g.innerW-20))),
		styDim.Render(render.Truncate(helpIntro, g.innerW)),
		"",
	}
	lines = append(lines, list...)
	for len(lines) < g.innerH-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.keyFooter([]keys.Ctx{ctxHelp}, g.innerW))
	return lines, g
}

// overlayRect is where overlay draws a box holding lines: centred, or the
// whole screen on a terminal too small for a frame.
func (m tuiModel) overlayRect(lines []string) mouse.Rect {
	if m.width < 24 || m.height < 8 {
		return mouse.Rect{W: m.width, H: m.height}
	}
	box := overlayBox.Render(strings.Join(lines, "\n"))
	bw, bh := lipgloss.Size(box)
	return mouse.Rect{X: max(0, (m.width-bw)/2), Y: max(0, (m.height-bh)/2), W: bw, H: bh}
}

// clampHelp stores the offset RenderHelp would draw, so holding j does not
// run the offset past the last page.
func (m *tuiModel) clampHelp() {
	g, rows := m.helpGeom()
	_, m.helpOff = keys.RenderHelp(m.helpSections(), g.innerW, rows, m.helpOff, keys.HelpStyle{})
}

// handleHelpKey scrolls the reference; any other key only closes it, so
// the key that dismissed it does nothing else.
func (m tuiModel) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	_, rows := m.helpGeom()
	b, _ := m.keymap().Resolve([]keys.Ctx{ctxHelp}, msg, m.keyOpts())
	switch b.Action {
	case "quit":
		m.quit = true
		return m, tea.Quit
	case "help.down":
		m.helpOff++
	case "help.up":
		m.helpOff--
	case "help.pagedown":
		m.helpOff += rows / 2
	case "help.pageup":
		m.helpOff -= rows / 2
	case "help.close":
		m.help = false
		return m, nil
	default:
		// Read, not used: an unbound key closes it and does nothing else.
		m.help = false
		return m, nil
	}
	m.clampHelp()
	return m, nil
}

// openPalette opens the action finder over the current screen.
func (m *tuiModel) openPalette() {
	m.palette, m.paletteQuery, m.paletteSel = true, input.Field{}, 0
}

// paletteEntries are the actions the screen under the palette can run,
// matching what has been typed. Numbered actions need a digit, which a
// palette has no way to give, so they are NoPalette and left to their keys.
func (m tuiModel) paletteEntries() []keys.Entry {
	return keys.Filter(m.keymap().Entries(m.screenStack(), m.keyOpts()), m.paletteQuery.Value())
}

// paletteRows is how many matches the box lists at once.
func (m tuiModel) paletteRows() int { return max(1, min(12, m.height-8)) }

func (m tuiModel) viewPaletteOverlay(base string) string {
	lines, g := m.paletteBox()
	return m.overlay(base, lines, g)
}

// paletteWindow is the matches drawn: es[start:end].
func (m tuiModel) paletteWindow(n int, g overlayGeom) (int, int) {
	start, end, _ := render.Window(n, g.innerH-2, m.paletteSel, 0)
	return start, end
}

// paletteBox is the palette overlay's text, without the frame. Its first
// line is the query and the matches follow from the second.
func (m tuiModel) paletteBox() ([]string, overlayGeom) {
	g := m.overlayGeom(72, m.paletteRows()+5)
	es := m.paletteEntries()
	lines := []string{styCyanBold.Render(": ") + fieldView(m.paletteQuery, g.innerW-2)}
	start, end := m.paletteWindow(len(es), g)
	keyW := 0
	for _, e := range es[start:end] {
		keyW = max(keyW, ansi.StringWidth(keys.Names{}.Name(e.Keys[0])))
	}
	for i := start; i < end; i++ {
		e := es[i]
		k := keys.Names{}.Name(e.Keys[0])
		line := render.Pad(k, keyW+2) + e.Help
		line = render.Pad(line, g.innerW)
		if i == m.paletteSel {
			line = stySel.Render(line)
		} else {
			line = styCyan.Render(render.Pad(k, keyW+2)) + render.Truncate(e.Help, max(0, g.innerW-keyW-2))
		}
		lines = append(lines, line)
	}
	if len(es) == 0 {
		lines = append(lines, styDim.Render("no action matches"))
	}
	if more := len(es) - end; more > 0 {
		lines = append(lines, styDim.Render(fmt.Sprintf("↓ %d more", more)))
	}
	for len(lines) < g.innerH-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:g.innerH-1], m.keyFooter([]keys.Ctx{ctxPalette}, g.innerW))
	return lines, g
}

// handlePaletteKey edits the query and runs the chosen action as if its
// key had been pressed on the screen underneath.
func (m tuiModel) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	es := m.paletteEntries()
	b, _ := m.keymap().ResolveRaw([]keys.Ctx{ctxPalette}, msg, m.keyOpts())
	switch b.Action {
	case "quit":
		m.quit = true
		return m, tea.Quit
	case "palette.close":
		m.palette = false
		return m, nil
	case "palette.down":
		m.paletteSel = min(m.paletteSel+1, max(0, len(es)-1))
		return m, nil
	case "palette.up":
		m.paletteSel = max(0, m.paletteSel-1)
		return m, nil
	case "palette.run":
		return m.runPaletteSel()
	}
	if f, _ := m.paletteQuery.Update(msg); f.Value() != m.paletteQuery.Value() {
		m.paletteSel = 0
		m.paletteQuery = f
	} else {
		m.paletteQuery = f // a cursor move
	}
	return m, nil
}

// runPaletteSel closes the palette and runs the selected match as if its key
// had been pressed on the screen underneath. A click on a row calls it too.
func (m tuiModel) runPaletteSel() (tea.Model, tea.Cmd) {
	es := m.paletteEntries()
	m.palette = false
	if m.paletteSel >= len(es) {
		return m, nil
	}
	if bd, ok := m.bindingIn(m.screenStack(), es[m.paletteSel].Action); ok {
		return m.do(bd, 1)
	}
	return m, nil
}

// bindingIn is action's binding in the innermost context of stack that
// has one.
func (m tuiModel) bindingIn(stack []keys.Ctx, action string) (keys.Binding, bool) {
	bs := m.keymap().Bindings()
	for _, ctx := range stack {
		if i := slices.IndexFunc(bs, func(b keys.Binding) bool { return b.Ctx == ctx && b.Action == action }); i >= 0 {
			return bs[i], true
		}
	}
	return keys.Binding{}, false
}
