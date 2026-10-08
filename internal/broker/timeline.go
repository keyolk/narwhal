// timeline.go assembles a run's history as the operator would tell it:
// what started, what was decided, what was asked of whom, what finished.
//
// Nothing kept that history. The radio is the workers talking to each
// other, the Brief is the run as it stands now — a decision drops off it
// once its task completes — and the synthesis outcome exists only at the
// end and says what was found, not how the run got there. Reading back
// "what happened in this run" meant interleaving the three by hand.
//
// It is derived from the snapshot alone, so the monitor and the markdown
// export tell the same story and a run read back from disk tells it too.
// Workers' transcripts are deliberately not a source: they are not part of
// the run record, and on this machine none of the twelve most recent
// workers' transcripts were still on disk.
package broker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TimelineKind says what sort of thing happened.
type TimelineKind string

const (
	TimelineRun         TimelineKind = "run"
	TimelineStarted     TimelineKind = "started"
	TimelineCompleted   TimelineKind = "completed"
	TimelineFailed      TimelineKind = "failed"
	TimelineInstruction TimelineKind = "instruction"
	TimelineRelay       TimelineKind = "relay"
	TimelineUrgent      TimelineKind = "urgent"
	TimelineVerdict     TimelineKind = "verdict"
	TimelineAsked       TimelineKind = "asked"
)

// FromRadio reports whether the event is a radio message lifted into the
// timeline, as opposed to something only the task states and verdicts
// record. A view that shows the radio beside the timeline drops these, or
// every instruction and urgent post is on screen twice.
func (k TimelineKind) FromRadio() bool {
	switch k {
	case TimelineInstruction, TimelineRelay, TimelineUrgent:
		return true
	}
	return false
}

// TimelineEvent is one line of a run's history.
type TimelineEvent struct {
	At   time.Time
	Kind TimelineKind
	// Who is the task the event is about, "operator", "coordinator", or
	// empty for the run itself.
	Who  string
	Text string
}

// Timeline returns the run's history, oldest first.
//
// Only what changes the story is kept: lifecycle, the operator's
// instructions and where they went, urgent posts, and the judge's calls
// that changed a task's course. A worker's ordinary posts stay on the
// radio — across the runs on disk they outnumber everything here several
// times over, and most are claims and acknowledgements.
func Timeline(s Snapshot) []TimelineEvent {
	var out []TimelineEvent
	add := func(at time.Time, kind TimelineKind, who, text string) {
		if at.IsZero() {
			return
		}
		out = append(out, TimelineEvent{At: at, Kind: kind, Who: who, Text: oneLine(text)})
	}

	if s.StartedAt > 0 {
		add(time.Unix(s.StartedAt, 0), TimelineRun, "", s.Prompt)
	}

	for _, t := range s.Tasks {
		attempt := ""
		if t.Dispatches > 1 {
			attempt = fmt.Sprintf("attempt %d", t.Dispatches)
		}
		add(t.StartedAt, TimelineStarted, t.ID, attempt)
		switch t.State {
		case TaskCompleted:
			add(t.EndedAt, TimelineCompleted, t.ID, firstLine(t.Outcome))
		case TaskFailed:
			add(t.EndedAt, TimelineFailed, t.ID, firstLine(t.Outcome))
		}
	}

	for _, m := range s.Messages {
		if m == nil || strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch {
		case m.Sender == "main":
			add(m.CreatedAt, TimelineInstruction, "operator", m.Content)
		case m.Sender == "coordinator" && strings.HasPrefix(m.Content, RelayPrefix):
			add(m.CreatedAt, TimelineRelay, "coordinator",
				strings.TrimSpace(strings.TrimPrefix(m.Content, RelayPrefix)))
		case m.Priority == PriorityUrgent && !IsProtocol(m.Content):
			add(m.CreatedAt, TimelineUrgent, strings.TrimPrefix(m.Sender, "worker-"), m.Content)
		}
	}

	for _, v := range s.Verdicts {
		switch {
		case v.Asker != "":
			text := v.Ask
			if v.Decided {
				text += fmt.Sprintf(" → %.0f%%", v.P*100)
			}
			add(v.At, TimelineAsked, strings.TrimPrefix(v.Asker, "worker-"), text)
		case v.Question == QuestionRoute:
			// One verdict per worker per instruction; the relay already
			// says where the instruction went.
		case v.Sharp():
			add(v.At, TimelineVerdict, v.TaskID, v.Question+" → "+v.Action)
		}
	}

	// Stable, so events stamped the same second keep the order they were
	// added in: a task's start before its end, an instruction before its
	// relay.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// IsProtocol reports whether a radio message is the workers coordinating —
// a file claim, a split, a dep edge, an escalation — rather than something
// one of them said.
func IsProtocol(content string) bool {
	if _, _, _, _, ok := ParseSplitRequest(content); ok {
		return true
	}
	if _, _, _, ok := ParseFileClaimRequest(content); ok {
		return true
	}
	if _, _, _, ok := ParseDepEdgeRequest(content); ok {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(content), ModelEscalatePrefix)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// firstLine is the opening line of an outcome, which runs to kilobytes.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}
