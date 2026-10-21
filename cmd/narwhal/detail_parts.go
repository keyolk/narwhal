package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/mouse"
)

// detailParts is a full-screen detail view (the result, a message, a task)
// before it is drawn: the lines above the body, the whole body, and how it
// scrolls. One value feeds both render and the drag-selection pane, so the
// pane's rows and offset are the ones drawn.
type detailParts struct {
	// prefix is everything above the body, ending in "\n"; pos is the
	// scroll position label scrollWindow made, which it carries somewhere.
	prefix   func(pos string) string
	body     []string
	avail    int
	scrollAt int
	footer   string
	// x is the column the body starts at; gutter, when set, draws the
	// cells left of it for body line n.
	x      int
	gutter func(n int) string
}

// window is the body lines drawn and the position label.
func (p detailParts) window() (scroll, end int, pos string) {
	return scrollWindow(p.scrollAt, p.avail, len(p.body))
}

func (p detailParts) render(height int) string {
	scroll, end, pos := p.window()
	shown := p.body[scroll:end]
	if p.gutter != nil {
		shown = make([]string, 0, end-scroll)
		for n := scroll; n < end; n++ {
			shown = append(shown, p.gutter(n)+p.body[n])
		}
	}
	return pinFooter(p.prefix(pos)+strings.Join(shown, "\n"), p.footer, height)
}

// rect is where render draws the body on a screen width cells wide and
// height rows tall, after pinFooter's clip.
func (p detailParts) rect(width, height int) mouse.Rect {
	scroll, end, pos := p.window()
	top := strings.Count(p.prefix(pos), "\n")
	rows := min(end-scroll, height-lipgloss.Height(p.footer)-top)
	return mouse.Rect{X: p.x, Y: top, W: max(width-p.x, 0), H: max(rows, 0)}
}
