// markdown.go renders the markdown workers write, for a terminal.
//
// Workers answer in markdown: tables of commits, ## headings, **bold**
// verdicts, fenced logs. The panes printed it as it came, so a commit table
// read as a fence of pipes and dashes and every emphasis carried its
// asterisks. glamour would render it, but its output's width and line
// count are its own, and these panes scroll and lay out by exactly those
// two numbers. This is the part of markdown that workers actually use,
// rendered to a width the caller chooses, one terminal line per string.
package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// span is a run of text in one style.
type span struct {
	text  string
	style lipgloss.Style
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdBullet  = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	// No backreference in RE2, so one alternative per rule character.
	mdRule     = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
)

// renderMarkdown renders text to lines at most width cells wide. base is
// the style of plain text — italic for thinking, say — and inline markup
// is layered on it.
func renderMarkdown(text string, width int, base lipgloss.Style) []string {
	width = max(width, 8)
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	var out []string
	blank := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```"):
			// A fence: the block verbatim, dimmed, cut rather than
			// wrapped — a log wrapped mid-line is harder to read than
			// one cut at the edge.
			blank()
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				out = append(out, styDim.Render("  "+ansi.Truncate(strings.ReplaceAll(lines[i], "\t", "    "), width-2, "…")))
			}
			blank()
		case strings.HasPrefix(trim, "|") && i+1 < len(lines) && mdTableSep.MatchString(lines[i+1]):
			var rows []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				rows = append(rows, lines[i])
			}
			i--
			blank()
			out = append(out, renderTable(rows, width, base)...)
			blank()
		case trim == "":
			blank()
		case mdRule.MatchString(trim):
			out = append(out, styDim.Render(strings.Repeat("─", min(width, 24))))
		default:
			if m := mdHeading.FindStringSubmatch(trim); m != nil {
				blank()
				style := styTitle
				if len(m[1]) <= 2 {
					style = styCyanBold
				}
				out = append(out, wrapSpans(inline(m[2], style), width, "")...)
				continue
			}
			if m := mdBullet.FindStringSubmatch(line); m != nil {
				indent := strings.Repeat(" ", min(len(m[1]), 6))
				mark := "• "
				if m[2][0] >= '0' && m[2][0] <= '9' {
					mark = m[2] + " "
				}
				first := append([]span{{indent + mark, styDim}}, inline(m[3], base)...)
				out = append(out, wrapSpans(first, width, indent+strings.Repeat(" ", runewidth.StringWidth(mark)))...)
				continue
			}
			if rest, ok := strings.CutPrefix(trim, ">"); ok {
				for _, l := range wrapSpans(inline(strings.TrimSpace(rest), styDim), width-2, "") {
					out = append(out, styDim.Render("│ ")+l)
				}
				continue
			}
			out = append(out, wrapSpans(inline(trim, base), width, "")...)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// renderTable lays a markdown table out in aligned columns, the header in
// bold. Columns too wide for the width share the shortfall, widest first,
// and their cells are cut.
func renderTable(rows []string, width int, base lipgloss.Style) []string {
	var cells [][]string
	for i, r := range rows {
		if i == 1 && mdTableSep.MatchString(r) {
			continue
		}
		r = strings.TrimSpace(r)
		r = strings.TrimSuffix(strings.TrimPrefix(r, "|"), "|")
		var row []string
		for _, c := range strings.Split(r, "|") {
			row = append(row, strings.TrimSpace(c))
		}
		cells = append(cells, row)
	}
	cols := 0
	for _, r := range cells {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return nil
	}
	w := make([]int, cols)
	for _, r := range cells {
		for j, c := range r {
			w[j] = max(w[j], runewidth.StringWidth(plainInline(c)))
		}
	}
	const gap = 2
	for total := sum(w) + gap*(cols-1); total > width; total = sum(w) + gap*(cols-1) {
		widest := 0
		for j := range w {
			if w[j] > w[widest] {
				widest = j
			}
		}
		if w[widest] <= 4 {
			break
		}
		w[widest]--
	}
	var out []string
	for i, r := range cells {
		var b strings.Builder
		for j := 0; j < cols; j++ {
			c := ""
			if j < len(r) {
				c = r[j]
			}
			style := base
			if i == 0 {
				style = base.Bold(true)
			}
			text := renderSpans(inline(c, style))
			if runewidth.StringWidth(plainInline(c)) > w[j] {
				text = ansi.Truncate(text, w[j], "…")
			}
			b.WriteString(text + strings.Repeat(" ", max(0, w[j]-ansi.StringWidth(text))))
			if j < cols-1 {
				b.WriteString(strings.Repeat(" ", gap))
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
		if i == 0 && len(cells) > 1 {
			out = append(out, styDim.Render(strings.Repeat("─", min(width, sum(w)+gap*(cols-1)))))
		}
	}
	return out
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// inline splits a line into styled spans: **bold**, `code`, and the rest
// in base. Markers that do not close are left as written.
func inline(s string, base lipgloss.Style) []span {
	var out []span
	for s != "" {
		bold := strings.Index(s, "**")
		code := strings.IndexByte(s, '`')
		switch {
		case bold >= 0 && (code < 0 || bold < code):
			end := strings.Index(s[bold+2:], "**")
			if end < 0 {
				out = append(out, span{s, base})
				return out
			}
			if bold > 0 {
				out = append(out, span{s[:bold], base})
			}
			out = append(out, span{s[bold+2 : bold+2+end], base.Bold(true)})
			s = s[bold+2+end+2:]
		case code >= 0:
			end := strings.IndexByte(s[code+1:], '`')
			if end < 0 {
				out = append(out, span{s, base})
				return out
			}
			if code > 0 {
				out = append(out, span{s[:code], base})
			}
			out = append(out, span{s[code+1 : code+1+end], styCyan})
			s = s[code+1+end+1:]
		default:
			out = append(out, span{s, base})
			return out
		}
	}
	return out
}

// plainInline is a line with its inline markers removed.
func plainInline(s string) string {
	var b strings.Builder
	for _, sp := range inline(s, lipgloss.NewStyle()) {
		b.WriteString(sp.text)
	}
	return b.String()
}

func renderSpans(sps []span) string {
	var b strings.Builder
	for _, sp := range sps {
		b.WriteString(sp.style.Render(sp.text))
	}
	return b.String()
}

// wrapSpans breaks styled text into lines of at most width cells, at
// spaces where it can and inside a word where it must. Lines after the
// first start with hang.
func wrapSpans(sps []span, width int, hang string) []string {
	hangW := runewidth.StringWidth(hang)
	width = max(width, hangW+4)
	var out []string
	var cur strings.Builder
	curW, empty, pendingSpace := 0, true, false
	newLine := func() {
		out = append(out, cur.String())
		cur.Reset()
		cur.WriteString(hang)
		curW, empty, pendingSpace = hangW, true, false
	}
	put := func(text string, style lipgloss.Style) {
		for text != "" {
			w := runewidth.StringWidth(text)
			sp := 0
			if pendingSpace && !empty {
				sp = 1
			}
			if curW+sp+w <= width {
				if sp == 1 {
					cur.WriteString(" ")
				}
				cur.WriteString(style.Render(text))
				curW += sp + w
				empty, pendingSpace = false, false
				return
			}
			if !empty && w <= width-hangW {
				// The word fits on a line of its own: start one.
				newLine()
				continue
			}
			// Longer than any line: break it at the edge.
			if sp == 1 {
				cur.WriteString(" ")
				curW++
			}
			fit := runewidth.Truncate(text, width-curW, "")
			if fit == "" {
				newLine()
				continue
			}
			cur.WriteString(style.Render(fit))
			text = text[len(fit):]
			newLine()
		}
	}
	for _, s := range sps {
		for i, part := range strings.Split(s.text, " ") {
			if i > 0 {
				pendingSpace = true
			}
			if part != "" {
				put(part, s.style)
			}
		}
	}
	if !empty || len(out) == 0 {
		out = append(out, cur.String())
	}
	return out
}

// flattenMarkdown turns a message into one line of plain prose for a
// preview: fences, table rules and markers dropped, cells and lines run
// together.
func flattenMarkdown(s string) string {
	var parts []string
	inFence := false
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || t == "" || mdTableSep.MatchString(t) || mdRule.MatchString(t) {
			continue
		}
		if m := mdHeading.FindStringSubmatch(t); m != nil {
			t = m[2]
		}
		if m := mdBullet.FindStringSubmatch(t); m != nil {
			t = m[3]
		}
		t = strings.TrimSpace(strings.Trim(t, "|"))
		t = strings.ReplaceAll(t, " | ", " · ")
		parts = append(parts, plainInline(t))
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}
