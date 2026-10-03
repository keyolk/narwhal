// verdicts.go renders the Jev judgements a run's dispatcher made.
//
// A judgement can change a task's fate — retry a worker that exited, or
// move a task up a model tier — and a fate changed off-screen reads as a
// task that failed or switched models for no reason. So every verdict is
// shown where the task is: on the node it touched, and summed in the
// header.
//
// The bar is the probability, and its colour is what the dispatcher did
// with it. Sharp verdicts acted; fallbacks were too close to call and kept
// the old behaviour; undecided ones got no answer at all. The colours say
// which, because "0.42" alone does not tell you whether it mattered.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

const verdictBarWidth = 10

// verdictBar draws p as a filled bar of verdictBarWidth cells.
func verdictBar(p float64) string {
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	filled := int(p*verdictBarWidth + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", verdictBarWidth-filled)
}

// verdictStyle is the colour of what the dispatcher did with a verdict.
func verdictStyle(v broker.Verdict) lipgloss.Style {
	switch {
	case !v.Decided:
		return styDim
	case v.Asker != "":
		// A worker's own question: the answer is the worker's to act on,
		// so it reads as information rather than as an action taken.
		return styCyan
	case v.Action == broker.ActionFallback:
		return styYellow
	case v.Action == broker.ActionEscalate:
		return styMagenta
	default:
		return styGreen
	}
}

// verdictRow is one verdict on one line: question, bar, p, what happened.
func verdictRow(v broker.Verdict, width int) string {
	if v.Asker != "" {
		return workerVerdictRow(v, width)
	}
	style := verdictStyle(v)
	label := fmt.Sprintf(" %s %-10s ", icons.fieldJudge, v.Question)
	if !v.Decided {
		return truncate(styDim.Render(label+strings.Repeat("·", verdictBarWidth)+" undecided → kept"), width)
	}
	action := v.Action
	if action == broker.ActionFallback {
		action = "kept"
	}
	tail := fmt.Sprintf(" %.2f → %s", v.P, action)
	if v.Latency > 0 {
		tail += styDim.Render(fmt.Sprintf("  %dms", v.Latency))
	}
	return truncate(styDim.Render(label)+style.Render(verdictBar(v.P))+style.Render(tail), width)
}

// workerVerdictRow is a question a worker asked: the bar and probability,
// then the question itself, which is the part worth reading.
func workerVerdictRow(v broker.Verdict, width int) string {
	head := styDim.Render(fmt.Sprintf(" %s asked ", icons.fieldJudge))
	if !v.Decided {
		return truncate(head+styDim.Render(strings.Repeat("·", verdictBarWidth)+" no answer  ")+v.Ask, width)
	}
	style := verdictStyle(v)
	return ansiTruncate(head+style.Render(verdictBar(v.P))+style.Render(fmt.Sprintf(" %.2f  ", v.P))+v.Ask, width)
}

// taskVerdicts returns the verdicts made about one task, oldest first.
func taskVerdicts(vs []broker.Verdict, taskID string) []broker.Verdict {
	var out []broker.Verdict
	for _, v := range vs {
		if v.TaskID == taskID {
			out = append(out, v)
		}
	}
	return out
}

// verdictSummary is the header's one-line account of every verdict on the
// run, or "" when there are none — a run nobody judged has nothing to say
// here, and a "jev 0" would be furniture.
func verdictSummary(vs []broker.Verdict) string {
	if len(vs) == 0 {
		return ""
	}
	var sharp, kept, undecided, asked int
	for _, v := range vs {
		switch {
		case v.Asker != "":
			asked++
		case !v.Decided:
			undecided++
		case v.Sharp():
			sharp++
		default:
			kept++
		}
	}
	parts := []string{styDim.Render(fmt.Sprintf("jev %d", len(vs)))}
	if sharp > 0 {
		parts = append(parts, styGreen.Render(fmt.Sprintf("%d sharp", sharp)))
	}
	if kept > 0 {
		parts = append(parts, styYellow.Render(fmt.Sprintf("%d kept", kept)))
	}
	if undecided > 0 {
		parts = append(parts, styDim.Render(fmt.Sprintf("%d undecided", undecided)))
	}
	if asked > 0 {
		parts = append(parts, styCyan.Render(fmt.Sprintf("%d asked", asked)))
	}
	return strings.Join(parts, styDim.Render(" · "))
}

// ansiTruncate fits a styled line to width without counting escape codes as
// cells or cutting one in half.
func ansiTruncate(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}
