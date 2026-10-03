// route.go sends an operator instruction that names nobody to the workers
// it concerns.
//
// The operator steers by typing into the monitor while the run goes on.
// "@schema add the field" reaches schema's watcher; "make sure nothing
// logs the token" reaches no one in particular — a broadcast every worker
// may or may not read, depending on whether its watcher wakes for
// unaddressed traffic. Usually it does not, so the instruction sits on the
// radio while the work it was about carries on without it.
//
// For an unaddressed operator message, the judge is asked, per running
// worker, whether the instruction bears on that worker's assignment. Those
// it does are named in a coordinator relay, which wakes them. If it bears
// on none, that is said on the radio: it is new work, and whether to add a
// task for it is the operator's call — a task minted from a misread
// instruction spends a worker run and is not undone by deleting a line.
package broker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// QuestionRoute names the routing judgement.
const QuestionRoute = "route"

// ActionRouted is a verdict that sent an instruction to its worker; a
// routing verdict below threshold keeps ActionFallback.
const ActionRouted = "routed"

// RouteThreshold is the P(relevant) at or above which an instruction may be
// relayed to a worker, and RouteMargin how close to the best-scoring worker
// it must also be.
//
// A threshold alone relayed everything. Measured on a real six-worker run
// (s1788336716669-6), whose assignments share a long preamble about the
// repository, branches and commits: "group the TUI key handlers in one
// file" scored 0.55-0.96 across all six and went to five of them. Asking
// about the worker-specific part of the assignment separated it — split-tui
// 0.84, the rest 0.18-0.43 — but "add race tests to the CI workflow" still
// ran 0.59-0.78. With both, against the live gateway: the TUI instruction
// went to split-tui alone, the CI one to ci alone, and one that really is
// for everyone ("gofmt only the files you touched") to four of the six.
// Where routing is still wrong the Brief shows where each instruction
// went, and @worker resends it to the right one.
const (
	RouteThreshold = 0.6
	RouteMargin    = 0.1
)

// RelayPrefix starts a coordinator relay of an operator instruction, so the
// relay is not itself routed again.
const RelayPrefix = "[relay]"

const routeQuestion = "INSTRUCTION is a new instruction from the operator of a multi-agent coding run. " +
	"ASSIGNMENT is one worker's specific task. Workers share boilerplate (repository, branch rules, " +
	"commit rules) — ignore it. Judge only the part of ASSIGNMENT that is specific to this worker. " +
	"Is INSTRUCTION directed at that specific work, so that this worker in particular must change " +
	"what it does? Answer false if INSTRUCTION is about a different worker's area, even if it " +
	"touches the same repository."

// routeTimeout bounds each per-worker question. They run in parallel, so
// this is also roughly the whole routing cost.
const routeTimeout = 4 * time.Second

// routeInstruction relays one unaddressed operator message to the running
// workers it concerns.
func (r *Run) routeInstruction(m *Message) {
	j := r.currentJudge()
	if j == nil {
		return
	}
	var running []*Task
	for _, t := range r.liveTasks() {
		if t.CurrentState() == TaskDispatched {
			running = append(running, t)
		}
	}
	if len(running) == 0 {
		return
	}

	verdicts := make([]Verdict, len(running))
	var wg sync.WaitGroup
	for i, t := range running {
		wg.Add(1)
		go func(i int, t *Task) {
			defer wg.Done()
			state := "INSTRUCTION:\n" + clip(m.Content, 1500) + "\n\nASSIGNMENT:\n" + clip(t.assignment(), 1500)
			ctx, cancel := context.WithTimeout(context.Background(), routeTimeout)
			defer cancel()
			start := time.Now()
			v := Verdict{At: time.Now(), TaskID: t.ID, Question: QuestionRoute, Action: ActionFallback,
				Ask: clip(m.Content, 200)}
			v.P, v.Decided = j.Ask(ctx, state, QuestionRoute, routeQuestion)
			v.Latency = time.Since(start).Milliseconds()
			verdicts[i] = v
		}(i, t)
	}
	wg.Wait()

	best := 0.0
	for _, v := range verdicts {
		if v.Decided && v.P > best {
			best = v.P
		}
	}
	for i, v := range verdicts {
		if v.Decided && v.P >= RouteThreshold && v.P >= best-RouteMargin {
			verdicts[i].Action = ActionRouted
		}
	}

	var to []string
	for _, v := range verdicts {
		r.recordVerdict(v)
		if v.Action == ActionRouted {
			to = append(to, "worker-"+v.TaskID)
		}
	}
	if len(to) > 0 {
		r.PostMessage(WorklogThread, "coordinator", to, m.Priority,
			fmt.Sprintf("%s from the operator, for %s: %s", RelayPrefix,
				strings.Join(stripWorker(to), ", "), m.Content))
		return
	}
	decided := false
	for _, v := range verdicts {
		decided = decided || v.Decided
	}
	if decided {
		r.PostMessage(WorklogThread, "coordinator", []string{"main"}, PriorityUrgent,
			fmt.Sprintf("%s no running worker's task covers this instruction; it may need a new task: %s",
				RelayPrefix, m.Content))
	}
}

func stripWorker(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.TrimPrefix(id, "worker-")
	}
	return out
}

// needsRouting reports whether a message is an operator instruction that
// names no task, so routing should find its workers.
func (r *Run) needsRouting(m *Message) bool {
	if m == nil || m.Sender != "main" || strings.HasPrefix(m.Content, RelayPrefix) {
		return false
	}
	for _, id := range m.Mentions {
		if r.GetTask(strings.TrimPrefix(id, "worker-")) != nil {
			return false
		}
	}
	for _, f := range strings.Fields(m.Content) {
		if !strings.HasPrefix(f, "@") {
			continue
		}
		id := strings.TrimPrefix(strings.TrimPrefix(strings.Trim(f, "@,.:;!?"), "@"), "worker-")
		if r.GetTask(id) != nil {
			return false
		}
	}
	return true
}

// liveTasks returns the run's tasks themselves rather than snapshots, for
// callers that need their current state.
func (r *Run) liveTasks() []*Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Task, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		out = append(out, t)
	}
	return out
}
