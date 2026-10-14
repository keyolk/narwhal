// attention.go is what asks for the operator: a "needs you" band under the
// run's header and badges on the pane titles where the thing is.
//
// Questions were one item among the header's counts, the same weight as
// "2 running", and a failed task was a red number in the progress counts.
// Neither said "this is yours to act on". The band is drawn only when
// something is — a band that is always there is read past like furniture,
// so with nothing to do it costs no row at all.
package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// failedTasks is how many of the run's tasks failed.
func (m tuiModel) failedTasks() int {
	n := 0
	for _, t := range m.snap.Tasks {
		if t.State == broker.TaskFailed {
			n++
		}
	}
	return n
}

// needsYouBand is the band, or "" when nothing waits on the operator.
func (m tuiModel) needsYouBand(w int) string {
	var items []string
	if open, blocking := openQuestionCount(m.snap); open > 0 {
		q := styYellow.Render(fmt.Sprintf("? %d question%s", open, plural(open)))
		if blocking > 0 {
			q += styRedBold.Render(fmt.Sprintf(" · %d waiting", blocking))
		}
		items = append(items, q+styDim.Render(" (Q)"))
	}
	if n := m.failedTasks(); n > 0 {
		items = append(items, styRedBold.Render(fmt.Sprintf("✗ %d task%s failed", n, plural(n)))+styDim.Render(" (marked in the graph)"))
	}
	if len(items) == 0 {
		return ""
	}
	band := styYellowBold.Render("▌ needs you  ") + strings.Join(items, styDim.Render("   "))
	return ansi.Truncate(band, max(1, w), "…")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// graphBadge marks the graph's title with the failed tasks in it.
func (m tuiModel) graphBadge() string {
	if n := m.failedTasks(); n > 0 {
		return styRedBold.Render(fmt.Sprintf(" ✗%d", n))
	}
	return ""
}

// radioBadge marks pane 4's tab with the questions waiting for an answer:
// the workers ask them on the radio.
func (m tuiModel) radioBadge() string {
	if open, _ := openQuestionCount(m.snap); open > 0 {
		return styYellowBold.Render(fmt.Sprintf(" ?%d", open))
	}
	return ""
}

// badgedPaneTitle is numberedPaneTitle with a badge after the label, where
// the rule would start.
func badgedPaneTitle(n int, label, badge string, focused bool, width int) string {
	if badge == "" {
		return numberedPaneTitle(n, label, focused, width)
	}
	title := numberedPaneTitle(n, label, focused, width-ansi.StringWidth(badge))
	// The title is "<n> <label>" then " ───": the badge goes between.
	plain := ansi.Strip(title)
	head := len(strconv.Itoa(n)) + 1 + len(label)
	if !strings.HasPrefix(plain[len(strconv.Itoa(n))+1:], label) {
		return title
	}
	cut := ansi.Cut(title, 0, head)
	rest := ansi.Cut(title, head, ansi.StringWidth(title))
	return cut + badge + rest
}
