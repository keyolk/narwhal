// feed.go merges what was said to a worker into what the worker did, so a
// task reads as one story: the assignment, the operator's instructions as
// they arrived, the judge's calls on it, and the worker's own reasoning,
// calls and results between them.
//
// The transcript alone could not tell it. The worker's prompt is the
// assignment, but an operator instruction reaches a worker as a watcher
// notification and a drain result — a bell and a JSON blob — so in the feed
// it was nowhere to be seen, and "did it act on what I asked" meant reading
// the radio and the transcript side by side and matching times by eye. The
// instructions and verdicts are in the run snapshot, stamped; they are
// placed into the feed at their times.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/narwhal/internal/broker"
)

// Entry kinds that come from the snapshot rather than the transcript.
const (
	kindOperator = "operator" // an instruction the operator sent this task
	kindJudge    = "judge"    // a verdict on this task, or a question it asked
)

// feedState carries what a render needs to know about entries before the
// current one.
type feedState struct {
	// sawPrompt is set once the transcript's first user text — the
	// assignment, which is the whole of a worker's prompt — has been shown.
	// User text after it is a watcher notification.
	sawPrompt bool
}

var notifySummary = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

// renderEntry formats one entry.
//
// Weights follow who is speaking. The operator's words and the assignment
// carry the warm colour, because they are what everything after them
// answers. A tool call is what the worker did and carries the cool one;
// its result is evidence and recedes; the worker's prose reads at full
// contrast; the judge is magenta, as on the graph.
func renderEntry(e transcriptEntry, width int, st *feedState) []string {
	stamp := ""
	if !e.at.IsZero() {
		stamp = clock(e.at) + " "
	}
	pad := strings.Repeat(" ", len(stamp)) + "  "
	block := func(glyph string, label string, body lipgloss.Style) []string {
		var out []string
		text := e.text
		if label != "" {
			text = label + " " + text
		}
		// Rendered as markdown: workers answer in tables, headings and
		// emphasis, and printed raw those were pipes and asterisks.
		for i, l := range renderMarkdown(text, width-len(stamp)-2, body) {
			if i == 0 {
				out = append(out, styDim.Render(stamp)+glyph+l)
				continue
			}
			out = append(out, pad+l)
		}
		return out
	}
	switch {
	case e.kind == "tool":
		return []string{styDim.Render(stamp) + styCyan.Render("→ ") +
			styCyan.Render(truncate(e.text, width-len(stamp)-2))}
	case e.kind == "result":
		var out []string
		for _, l := range clipLines(e.text, 2) {
			out = append(out, pad+styDim.Render(truncate(oneLine(l), width-len(pad))))
		}
		return out
	case e.kind == "thinking":
		return block(styMagenta.Render("∴ "), "", styThought)
	case e.kind == kindOperator:
		return block(styYellowBold.Render("» "), "you:", styYellow)
	case e.kind == kindJudge:
		return block(styMagenta.Render(icons.fieldJudge+" "), "", styMagenta)
	case e.kind == "text" && e.user && !st.sawPrompt:
		st.sawPrompt = true
		return block(styYellowBold.Render("▶ "), "task:", styYellow)
	case e.kind == "text" && e.user:
		// A watcher's bell. Its summary says what woke the worker; the
		// rest of the notification is plumbing.
		text := "notified"
		if m := notifySummary.FindStringSubmatch(e.text); m != nil {
			text = oneLine(m[1])
		}
		return []string{styDim.Render(stamp) + styDim.Render("⚑ "+truncate(text, width-len(stamp)-2))}
	default:
		return block(styGreen.Render("· "), "", lipgloss.NewStyle())
	}
}

// taskInputs is what the snapshot says was directed at a task, as entries
// to merge into its feed: the operator's instructions that reached it, and
// the judge's calls on it.
func (m tuiModel) taskInputs(taskID string) []transcriptEntry {
	tasks := make(map[string]bool, len(m.snap.Tasks))
	for _, t := range m.snap.Tasks {
		tasks[t.ID] = true
	}
	var out []transcriptEntry
	for _, msg := range m.snap.Messages {
		if msg == nil || msg.Sender != "main" || broker.IsProtocol(msg.Content) {
			continue
		}
		to := recipients(msg, "", tasks)
		if len(to) == 0 {
			to = routedTo(m.snap, msg.Content)
		}
		// An instruction to no one in particular reached every worker.
		if len(to) > 0 && !slices.Contains(to, taskID) {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if len(to) == 0 {
			text += "  (to everyone)"
		}
		out = append(out, transcriptEntry{at: msg.CreatedAt, kind: kindOperator, text: text})
	}
	for _, v := range m.snap.Verdicts {
		if v.TaskID != taskID {
			continue
		}
		switch {
		case v.Asker != "":
			text := "asked the judge: " + v.Ask
			if v.Decided {
				text += fmt.Sprintf(" → %.0f%%", v.P*100)
			}
			out = append(out, transcriptEntry{at: v.At, kind: kindJudge, text: text})
		case v.Question == broker.QuestionRoute:
			// The instruction it routed is already in the feed.
		case v.Sharp():
			out = append(out, transcriptEntry{at: v.At, kind: kindJudge,
				text: fmt.Sprintf("judge: %s %.2f → %s", v.Question, v.P, v.Action)})
		}
	}
	return out
}

// mergeFeed places inputs among the transcript's entries by time. Stable,
// so an input stamped the same second as an entry follows it: the worker
// cannot have answered an instruction in the same second it arrived.
func mergeFeed(transcript, inputs []transcriptEntry) []transcriptEntry {
	if len(inputs) == 0 {
		return transcript
	}
	out := make([]transcriptEntry, 0, len(transcript)+len(inputs))
	out = append(out, transcript...)
	out = append(out, inputs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// inputsSig identifies a set of inputs for the render cache: the inputs
// only ever grow, so their count and the newest time tell two sets apart.
func inputsSig(inputs []transcriptEntry) string {
	if len(inputs) == 0 {
		return ""
	}
	var newest time.Time
	for _, e := range inputs {
		if e.at.After(newest) {
			newest = e.at
		}
	}
	return fmt.Sprintf("%d@%d", len(inputs), newest.UnixNano())
}
