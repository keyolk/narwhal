// attention.go says what a run needs from its person, for whatever shows
// it to them where they already are.
//
// The monitor has every answer, and the person is not in it: they are in
// the Claude Code session that started the run, with its cockpit beside
// it. A checkpoint raised five questions on a run started from a session
// nobody was watching the monitor of; they went unanswered until it timed
// out and went on with its own recommendations. What the person is asked
// for, and what has come back, has to reach the session.
//
// Attention reads a snapshot and returns the items: questions waiting for
// the operator, workers judged stuck, and a run that has finished or
// failed. Pure, so the daemon's endpoint and a test agree on what a run
// needs.
package broker

import (
	"fmt"
	"strings"
	"time"
)

// AttentionKind is what an item asks of the person.
type AttentionKind string

const (
	AttentionQuestion AttentionKind = "question"
	AttentionStuck    AttentionKind = "stuck"
	AttentionDone     AttentionKind = "done"
	AttentionFailed   AttentionKind = "failed"
)

// AttentionItem is one thing a run needs from its person, or tells them.
type AttentionItem struct {
	Kind    AttentionKind `json:"kind"`
	RunID   string        `json:"run_id"`
	Flow    string        `json:"flow,omitempty"`
	Request string        `json:"request"`
	Origin  *Origin       `json:"origin,omitempty"`
	At      time.Time     `json:"at"`
	// Title is the item in one line, for a list.
	Title string `json:"title"`
	// Question is the question, for a question item: its options,
	// recommendation, context and grounds, and the id to answer.
	Question *Question `json:"question,omitempty"`
	// TaskID is the stuck task.
	TaskID string `json:"task_id,omitempty"`
	// Summary is a finished run's answer, its first lines; Artifacts how
	// many things it produced.
	Summary   string `json:"summary,omitempty"`
	Artifacts int    `json:"artifacts,omitempty"`
}

// attentionWindow is how long a finished run stays an item: long enough to
// be seen the next morning, short enough not to become a list of history.
const attentionWindow = 24 * time.Hour

// Attention returns what the run needs from its person, newest first
// within kind: questions, then stuck workers, then how it ended.
func Attention(s Snapshot, now time.Time) []AttentionItem {
	base := AttentionItem{RunID: s.RunID, Flow: s.FlowID(), Request: firstLine(flatPrompt(s.Prompt)), Origin: s.Origin}
	var out []AttentionItem

	for _, q := range s.Questions {
		if !q.ForOperator() {
			continue
		}
		q := q
		it := base
		it.Kind, it.At, it.Question = AttentionQuestion, q.AskedAt, &q
		who := q.TaskID
		if who == PlanAsker {
			who = "planner"
		}
		if q.RaisedBy != "" {
			who = q.RaisedBy
		}
		it.Title = fmt.Sprintf("#%d %s · %s", q.ID, who, clipRunes(q.Text, 120))
		out = append(out, it)
	}

	if s.State == RunActive {
		for _, t := range s.Tasks {
			if t.State != TaskDispatched {
				continue
			}
			if v, ok := lastVerdict(s.Verdicts, t.ID, QuestionStuck); ok && v.Action == ActionFlagged {
				it := base
				it.Kind, it.At, it.TaskID = AttentionStuck, v.At, t.ID
				it.Title = fmt.Sprintf("%s looks stuck (p=%.2f)", t.ID, v.P)
				out = append(out, it)
			}
		}
	}

	if s.State == RunDone || s.State == RunFailed {
		end := endedAt(s)
		if !end.IsZero() && now.Sub(end) <= attentionWindow {
			res := s.Result()
			it := base
			it.At, it.Artifacts = end, res.Artifacts.Count()
			it.Summary = clipRunes(proseSentence(res.Answer), 200)
			failed := 0
			for _, t := range s.Tasks {
				if t.State == TaskFailed {
					failed++
				}
			}
			if s.State == RunFailed || failed > 0 && strings.TrimSpace(res.Answer) == "" {
				it.Kind = AttentionFailed
				it.Title = fmt.Sprintf("failed · %d of %d tasks", failed, len(s.Tasks))
			} else {
				it.Kind = AttentionDone
				it.Title = fmt.Sprintf("done · %d tasks · %d outputs", len(s.Tasks), it.Artifacts)
			}
			out = append(out, it)
		}
	}
	return out
}

// lastVerdict is the most recent verdict of a kind on a task.
func lastVerdict(vs []Verdict, taskID, question string) (Verdict, bool) {
	var last Verdict
	found := false
	for _, v := range vs {
		if v.TaskID == taskID && v.Question == question && (!found || v.At.After(last.At)) {
			last, found = v, true
		}
	}
	return last, found
}

// endedAt is when the run's last task finished.
func endedAt(s Snapshot) time.Time {
	var end time.Time
	for _, t := range s.Tasks {
		if t.EndedAt.After(end) {
			end = t.EndedAt
		}
	}
	return end
}

// flatPrompt is a request on one line.
func flatPrompt(p string) string {
	return strings.Join(strings.Fields(p), " ")
}
