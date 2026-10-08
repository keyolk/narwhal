// brief.go is the operator's view of a run: what needs a decision, where
// each worker is, and what became of each instruction sent.
//
// The radio is the workers talking to each other. It is the right record
// and the wrong summary: the one urgent line sits among forty claims and
// acknowledgements, a worker's state has to be pieced together from its
// last few posts, and an instruction from the operator scrolls away with
// no sign of whether anyone picked it up. Brief answers those three
// questions from the same snapshot, as the second tab of pane 3: `3` on
// the focused pane switches between the radio and the brief.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// briefDecisionsShown caps the decision list so the worker and instruction
// sections still fit.
const briefDecisionsShown = 6

// briefDecision is one thing the operator may need to act on.
type briefDecision struct {
	glyph, who, what string
	at               string
	urgent           bool
}

// briefDecisions collects, newest first: failed tasks, urgent messages,
// escalations, and Jev verdicts that changed a task's course.
//
// Only while they can still matter. An urgent post from a task that has
// since completed was dealt with one way or another; on a finished run on
// disk, five of them led the list and said nothing about what to do now.
func briefDecisions(s broker.Snapshot) []briefDecision {
	state := make(map[string]broker.TaskState, len(s.Tasks))
	for _, t := range s.Tasks {
		state[t.ID] = t.State
	}
	open := func(id string) bool {
		st, ok := state[id]
		return !ok || (st != broker.TaskCompleted)
	}
	var out []briefDecision
	for _, t := range s.Tasks {
		if t.State == broker.TaskFailed {
			out = append(out, briefDecision{glyph: "✗", who: t.ID,
				what: "failed: " + firstLine(t.Outcome), urgent: true})
		}
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m == nil || m.Sender == "main" {
			continue
		}
		who := strings.TrimPrefix(m.Sender, "worker-")
		if !open(who) {
			continue
		}
		at := ""
		if !m.CreatedAt.IsZero() {
			at = m.CreatedAt.Local().Format("15:04")
		}
		switch {
		case m.Priority == broker.PriorityUrgent:
			out = append(out, briefDecision{glyph: "!", who: who, what: oneLine(m.Content), at: at, urgent: true})
		case strings.HasPrefix(m.Content, broker.ModelEscalatePrefix),
			m.Sender == "coordinator" && strings.HasPrefix(m.Content, "escalating"):
			out = append(out, briefDecision{glyph: "⇡", who: who, what: oneLine(m.Content), at: at})
		}
	}
	for i := len(s.Verdicts) - 1; i >= 0; i-- {
		v := s.Verdicts[i]
		if !open(v.TaskID) {
			continue
		}
		if v.Action == broker.ActionRetry || v.Action == broker.ActionEscalate {
			out = append(out, briefDecision{glyph: "◇", who: v.TaskID,
				what: fmt.Sprintf("jev %s %.2f → %s", v.Question, v.P, v.Action)})
		}
	}
	return out
}

// briefWorkerLine is one task: its state and the gist of its latest post.
func (m tuiModel) briefWorkerLine(t broker.TaskSnapshot) string {
	last := ""
	for i := len(m.snap.Messages) - 1; i >= 0; i-- {
		msg := m.snap.Messages[i]
		if msg != nil && msg.Sender == "worker-"+t.ID && !isProtocolLine(msg.Content) {
			last = oneLine(msg.Content)
			break
		}
	}
	if last == "" && t.State == broker.TaskCompleted {
		last = firstLine(t.Outcome)
	}
	if last == "" {
		last = string(t.State)
	}
	return last
}

// isProtocolLine reports whether a radio body is coordination (a claim, a
// split) rather than something a worker said.
func isProtocolLine(content string) bool {
	for _, p := range []string{
		broker.FileClaimPrefix, broker.FileReleasePrefix, broker.SplitRequestPrefix,
		broker.DepAddPrefix, broker.DepRemovePrefix, broker.ModelEscalatePrefix,
	} {
		if strings.HasPrefix(content, p) {
			return true
		}
	}
	return false
}

// briefInstruction is one operator message and whether it was answered.
type briefInstruction struct {
	at, text string
	// answeredBy is the addressed workers that have posted since, or, for
	// an instruction to everyone, any worker that has.
	answeredBy []string
	addressed  []string
	// routed says the router chose addressed, not the operator.
	routed bool
}

// routedTo is the workers the router relayed an instruction to, read back
// from its relay on the radio.
func routedTo(s broker.Snapshot, content string) []string {
	for _, m := range s.Messages {
		if m == nil || m.Sender != "coordinator" || !strings.HasPrefix(m.Content, broker.RelayPrefix) ||
			!strings.HasSuffix(m.Content, content) || len(m.Mentions) == 0 {
			continue
		}
		var out []string
		for _, id := range m.Mentions {
			out = append(out, strings.TrimPrefix(id, "worker-"))
		}
		return out
	}
	return nil
}

// briefInstructions lists the operator's messages, oldest first, each with
// who has spoken since. A worker posting after an instruction is the only
// sign on the radio that it was read; nothing records an explicit ack.
func briefInstructions(s broker.Snapshot) []briefInstruction {
	tasks := make(map[string]bool, len(s.Tasks))
	for _, t := range s.Tasks {
		tasks[t.ID] = true
	}
	var out []briefInstruction
	for i, m := range s.Messages {
		if m == nil || m.Sender != "main" {
			continue
		}
		in := briefInstruction{text: oneLine(m.Content), addressed: recipients(m, "", tasks)}
		// An instruction that named nobody may have been relayed by the
		// router; then the workers it went to are the ones to hear from.
		if len(in.addressed) == 0 {
			in.addressed = routedTo(s, m.Content)
			in.routed = len(in.addressed) > 0
		}
		if !m.CreatedAt.IsZero() {
			in.at = m.CreatedAt.Local().Format("15:04")
		}
		want := map[string]bool{}
		for _, id := range in.addressed {
			want[id] = true
		}
		seen := map[string]bool{}
		for _, later := range s.Messages[i+1:] {
			if later == nil || !strings.HasPrefix(later.Sender, "worker-") {
				continue
			}
			id := strings.TrimPrefix(later.Sender, "worker-")
			if (len(want) == 0 || want[id]) && !seen[id] {
				seen[id] = true
				in.answeredBy = append(in.answeredBy, id)
			}
		}
		sort.Strings(in.answeredBy)
		out = append(out, in)
	}
	return out
}

// viewBrief renders the operator's view in the radio pane's place.
func (m tuiModel) viewBrief(width, height int) string {
	rows := []string{m.channelTitle(width)}
	section := func(name string) {
		rows = append(rows, styTitle.Render(" "+name))
	}
	// These lines are styled before they are fitted, so the cut has to
	// skip escape sequences: truncate counts them as cells, which ends a
	// line early and can split a colour code in half.
	line := func(s string) {
		rows = append(rows, ansi.Truncate(s, width, "…"))
	}

	section("Needs a decision")
	ds := briefDecisions(m.snap)
	if len(ds) == 0 {
		line(styDim.Render("   nothing"))
	}
	for i, d := range ds {
		if i == briefDecisionsShown {
			line(styDim.Render(fmt.Sprintf("   … %d more", len(ds)-i)))
			break
		}
		style := styYellow
		if d.urgent {
			style = styRedBold
		}
		line("  " + style.Render(d.glyph) + " " + styCyan.Render(padRight(d.who, 12)) + " " +
			d.what + styDim.Render("  "+d.at))
	}

	section("Workers")
	for _, t := range m.sortedTasks() {
		icon, style := taskIconStyle(t.State)
		if t.State == broker.TaskDispatched {
			icon = spinnerAt(m.frame)
		}
		line("  " + style.Render(icon) + " " + styCyan.Render(padRight(t.ID, 12)) + " " + m.briefWorkerLine(t))
	}

	section("Your instructions")
	ins := briefInstructions(m.snap)
	if len(ins) == 0 {
		line(styDim.Render("   none yet — press i to send one"))
	}
	for _, in := range ins {
		// Who answered goes before the text: the text is long, and the
		// answer is the part that tells you whether to follow up.
		mark, state := styYellow.Render("…"), styYellow.Render(padRight("waiting", 14))
		if len(in.answeredBy) > 0 {
			mark, state = styGreen.Render("✓"), styGreen.Render(padRight(strings.Join(in.answeredBy, ","), 14))
		}
		via := ""
		if in.routed {
			// Who it was relayed to, so a routing mistake is visible.
			via = styMagenta.Render("→" + strings.Join(in.addressed, ",") + " ")
		}
		line("  " + mark + " " + styDim.Render(in.at) + " " + state + " " + via + in.text)
	}
	return padRows(rows, width, height)
}
