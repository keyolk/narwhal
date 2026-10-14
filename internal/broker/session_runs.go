// session_runs.go is a run as the Claude Code session that started it
// lists it: its tasks and how far each has got, and what it produced.
//
// The cockpit beside a session shows the session's work — its subagents,
// background shells, Monitors — and what it made. A narwhal run started
// from the session is work of the same kind, and was missing from both: it
// was in the monitor only. RunSummary is that run in the shape the cockpit
// draws, without the radio, the verdicts or the transcripts it has no room
// for; those stay a press away, in `narwhal monitor --run`.
package broker

import (
	"sort"
	"time"
)

// RunSummary is a run for a list.
type RunSummary struct {
	RunID     string        `json:"run_id"`
	Flow      string        `json:"flow,omitempty"`
	Request   string        `json:"request"`
	State     RunState      `json:"state"`
	StartedAt time.Time     `json:"started_at"`
	EndedAt   time.Time     `json:"ended_at,omitempty"`
	Done      int           `json:"done"`
	Failed    int           `json:"failed"`
	Running   int           `json:"running"`
	Tasks     []TaskSummary `json:"tasks"`
	// Questions waiting for the person, so a list can mark the run.
	Waiting int `json:"waiting,omitempty"`
	// Answer is the result's first prose sentence; Artifacts what it made.
	Answer    string    `json:"answer,omitempty"`
	Artifacts Artifacts `json:"artifacts"`
}

// TaskSummary is a task for a list.
type TaskSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	State     TaskState `json:"state"`
	Model     string    `json:"model,omitempty"`
	Deps      []string  `json:"deps,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
}

// Summarize is the run as a list shows it.
func Summarize(s Snapshot) RunSummary {
	res := s.Result()
	out := RunSummary{RunID: s.RunID, Flow: s.FlowID(), Request: flatPrompt(s.Prompt), State: s.State,
		StartedAt: time.Unix(s.StartedAt, 0), Answer: clipRunes(proseSentence(res.Answer), 200), Artifacts: res.Artifacts}
	for _, t := range s.Tasks {
		switch t.State {
		case TaskCompleted:
			out.Done++
		case TaskFailed:
			out.Failed++
		case TaskDispatched:
			out.Running++
		}
		out.Tasks = append(out.Tasks, TaskSummary{ID: t.ID, Name: t.Name, State: t.State, Model: t.Model,
			Deps: t.Deps, StartedAt: t.StartedAt, EndedAt: t.EndedAt, Outcome: clipRunes(proseSentence(t.Outcome), 160)})
	}
	// The order the run was planned in: started first, then by id.
	sort.SliceStable(out.Tasks, func(i, j int) bool {
		a, b := out.Tasks[i], out.Tasks[j]
		if a.StartedAt.IsZero() != b.StartedAt.IsZero() {
			return !a.StartedAt.IsZero()
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.ID < b.ID
	})
	if s.State == RunDone || s.State == RunFailed {
		out.EndedAt = endedAt(s)
	}
	for _, q := range s.Questions {
		if q.ForOperator() {
			out.Waiting++
		}
	}
	return out
}
