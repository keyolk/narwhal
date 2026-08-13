// Package coordinator drives a Run's task DAG to completion.
//
// The loop is deliberately simple and deterministic, mirroring Orca's
// dependency-driven graph execution:
//
//	tick:
//	  find every task in TaskReady state
//	  dispatch up to maxConcurrency of them in parallel
//	  wait for any worker to finish
//	  completed tasks flip their dependents to ready
//	  repeat until nothing is ready and nothing is running
//
// The coordinator owns dispatch, not planning. What tasks exist and how they
// depend on each other is decided elsewhere — by the caller up front, or by
// a coordinating agent adding tasks mid-run via split-request. Existing tasks
// are never edited; the graph only grows.
package coordinator

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// Config tunes the dispatch loop.
type Config struct {
	// MaxConcurrency caps how many workers run at once. Workers share one
	// ccproxy account pool, so an unbounded fan-out would drain quota and
	// starve the user's interactive session.
	MaxConcurrency int

	// TickInterval is how often the loop re-examines the graph when it is
	// waiting on running workers.
	TickInterval time.Duration

	// Timeout bounds the whole run.
	Timeout time.Duration

	// SynthesisModel overrides the worker model for the synthesis task —
	// the one that integrates peer findings with fidelity. Empty means use
	// the same model as every other worker. The Cursor economics
	// insight: frontier intelligence is needed for decomposition and
	// integration, not for narrow investigation.
	SynthesisModel string
}

// DefaultConfig returns conservative defaults.
func DefaultConfig() Config {
	return Config{
		MaxConcurrency: 3,
		TickInterval:   500 * time.Millisecond,
		Timeout:        30 * time.Minute,
	}
}

// WorkerRunner is the subset of the launcher the coordinator depends on.
// Extracting it as an interface keeps the dispatch loop testable without
// spawning real Claude Code processes.
type WorkerRunner interface {
	SetupAgent(a *broker.Agent, cfg launcher.WorkerConfig) (string, error)
	Launch(agentDir string, cfg launcher.WorkerConfig) error
	ActiveWorkers() []string
}

// Coordinator drives one Run's task graph.
type Coordinator struct {
	run      *broker.Run
	registry *broker.AgentRegistry
	launcher WorkerRunner
	cfg      Config

	mu          sync.Mutex
	running     map[string]string // taskID → agentID
	finished    map[string]bool   // taskID → true once terminal
	splitCursor int64             // last processed message Seq for split-request intake
	depCursor   int64             // last processed message Seq for dep-edge intake
}

// New creates a Coordinator for a run.
func New(run *broker.Run, reg *broker.AgentRegistry, l WorkerRunner, cfg Config) *Coordinator {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = DefaultConfig().MaxConcurrency
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = DefaultConfig().TickInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultConfig().Timeout
	}
	return &Coordinator{
		run:      run,
		registry: reg,
		launcher: l,
		cfg:      cfg,
		running:  make(map[string]string),
		finished: make(map[string]bool),
	}
}

// Result summarizes a finished run.
type Result struct {
	RunID     string
	Completed []string
	Failed    []string
	Unreached []string // still pending/blocked when the loop stopped
	TimedOut  bool
}

// Run drives the graph until every task is terminal, nothing can make
// progress, or the timeout elapses.
func (c *Coordinator) Run() Result {
	deadline := time.Now().Add(c.cfg.Timeout)
	res := Result{RunID: c.run.ID}

	for {
		if time.Now().After(deadline) {
			res.TimedOut = true
			break
		}

		c.reapFinishedWorkers()
		c.intakeSplitRequests()
		c.intakeGraphRequests()
		dispatched := c.dispatchReady()

		c.mu.Lock()
		activeCount := len(c.running)
		c.mu.Unlock()

		if activeCount == 0 && dispatched == 0 {
			// Nothing running and nothing became ready: either everything
			// is done, or the remaining tasks are permanently blocked.
			if !c.hasReadyOrRunning() {
				break
			}
		}

		time.Sleep(c.cfg.TickInterval)
	}

	// Classify the final state.
	tasks := c.run.SnapshotTasks()
	for _, t := range tasks {
		switch t.State {
		case broker.TaskCompleted:
			res.Completed = append(res.Completed, t.ID)
		case broker.TaskFailed:
			res.Failed = append(res.Failed, t.ID)
		default:
			res.Unreached = append(res.Unreached, t.ID)
		}
	}

	// Set the run's terminal state so persisted snapshots reflect the
	// outcome rather than "active".
	switch {
	case res.TimedOut:
		c.run.SetState(broker.RunFailed)
	case len(res.Failed) > 0 || len(res.Unreached) > 0:
		c.run.SetState(broker.RunFailed)
	default:
		c.run.SetState(broker.RunDone)
	}

	return res
}

// dispatchReady launches workers for ready tasks, up to the concurrency cap.
// Returns how many were dispatched this tick.
func (c *Coordinator) dispatchReady() int {
	c.mu.Lock()
	slots := c.cfg.MaxConcurrency - len(c.running)
	c.mu.Unlock()
	if slots <= 0 {
		return 0
	}

	// Dispatchable, not just ready: a synthesis task is launched ahead of
	// its dependencies so it can listen while its peers work. Its deps
	// still hold — the broker refuses its task-done until they finish.
	ready := c.run.DispatchableTasks()
	dispatched := 0
	for _, task := range ready {
		if dispatched >= slots {
			break
		}
		c.mu.Lock()
		_, alreadyRunning := c.running[task.ID]
		alreadyFinished := c.finished[task.ID]
		c.mu.Unlock()
		if alreadyRunning || alreadyFinished {
			continue
		}

		if err := c.dispatchTask(task); err != nil {
			log.Printf("[coordinator] dispatch %s: %v", task.ID, err)
			task.FailDispatch(err.Error(), c.run)
			continue
		}
		dispatched++
	}
	return dispatched
}

// dispatchTask registers an agent for the task, sets up its workspace, and
// launches the worker process.
func (c *Coordinator) dispatchTask(task *broker.Task) error {
	agentID := fmt.Sprintf("worker-%s", task.ID)
	agent := c.registry.Register(agentID, c.run.ID, false)

	cfg := launcher.WorkerConfig{
		AgentID:    agentID,
		TaskID:     task.ID,
		Assignment: task.Assignment,
		Model:      task.CurrentModel(),
	}
	// The synthesis task integrates peer findings — it needs frontier
	// intelligence even when the investigation workers do not. Apply the
	// config-level synthesis model unless the planner already set a
	// per-task model (per-task wins; it is more specific).
	if cfg.Model == "" && c.cfg.SynthesisModel != "" && task.IsSynthesis() {
		cfg.Model = c.cfg.SynthesisModel
	}

	agentDir, err := c.launcher.SetupAgent(agent, cfg)
	if err != nil {
		return fmt.Errorf("setup agent: %w", err)
	}

	dispatchID := fmt.Sprintf("%s-d%d", task.ID, task.DispatchCount()+1)
	task.StartDispatch(dispatchID, agentID)

	if err := c.launcher.Launch(agentDir, cfg); err != nil {
		return fmt.Errorf("launch worker: %w", err)
	}

	c.mu.Lock()
	c.running[task.ID] = agentID
	c.mu.Unlock()

	log.Printf("[coordinator] dispatched %s → %s", task.ID, agentID)
	return nil
}

// reapFinishedWorkers checks which running workers have exited and updates
// bookkeeping. A worker that exits without calling task-done is treated as
// a failed dispatch, which either retries or trips the circuit breaker.
func (c *Coordinator) reapFinishedWorkers() {
	active := make(map[string]bool)
	for _, id := range c.launcher.ActiveWorkers() {
		active[id] = true
	}

	c.mu.Lock()
	type exitedTask struct {
		taskID  string
		agentID string
	}
	var exited []exitedTask
	for taskID, agentID := range c.running {
		if !active[agentID] {
			exited = append(exited, exitedTask{taskID, agentID})
		}
	}
	for _, et := range exited {
		delete(c.running, et.taskID)
	}
	c.mu.Unlock()

	for _, et := range exited {
		task := c.run.GetTask(et.taskID)
		if task == nil {
			continue
		}
		// A worker that exits still holding file claims would lock those
		// paths for the rest of the run, so release them here rather than
		// trusting every exit path to have called FILE_RELEASE.
		c.releaseTaskFiles(et.taskID)
		state := task.CurrentState()
		if state == broker.TaskCompleted || state == broker.TaskFailed {
			c.mu.Lock()
			c.finished[et.taskID] = true
			c.mu.Unlock()
			continue
		}
		// Worker exited without declaring completion. Before failing the
		// dispatch, check whether the worker actually posted findings to the
		// radio — a worker that did its job but forgot the task-done call
		// should not be retried and waste another 10 minutes. The synthesis
		// task can still drain whatever the worker posted.
		if c.run.AgentPostedToRadio(et.agentID) {
			log.Printf("[coordinator] %s exited without task-done but posted to radio; marking complete", et.taskID)
			task.CompleteDispatch("completed via radio activity", c.run)
			c.mu.Lock()
			c.finished[et.taskID] = true
			c.mu.Unlock()
			continue
		}
		log.Printf("[coordinator] %s exited without task-done; recording failure", et.taskID)
		task.FailDispatch("worker exited without calling task-done", c.run)
		if task.CurrentState() == broker.TaskFailed {
			c.mu.Lock()
			c.finished[et.taskID] = true
			c.mu.Unlock()
		}
	}
}

// releaseTaskFiles gives up every path a task still holds. Called when a
// worker exits, so a forgotten FILE_RELEASE cannot strand a path for the
// rest of the run.
func (c *Coordinator) releaseTaskFiles(taskID string) {
	var held []string
	for path, owner := range c.run.FileClaims() {
		if owner == taskID {
			held = append(held, path)
		}
	}
	if len(held) == 0 {
		return
	}
	c.run.ReleaseFiles(taskID, held)
	log.Printf("[coordinator] released %d file(s) held by exited %s", len(held), taskID)
}

// intakeSplitRequests scans the planning thread for split-request messages
// the coordinator has not yet processed and creates the requested tasks.
//
// This is the only path by which the graph grows mid-run. Existing tasks
// are never edited; new ones are appended with their deps. The coordinator
// tracks the last processed message cursor so it does not re-create a task
// on every tick.
func (c *Coordinator) intakeSplitRequests() {
	msgs := c.run.MessagesSince(c.splitCursor)
	for _, m := range msgs {
		if m.ThreadID != "planning" {
			continue
		}
		taskID, name, assignment, deps, ok := broker.ParseSplitRequest(m.Content)
		if !ok {
			continue
		}
		if c.run.GetTask(taskID) != nil {
			// Already created (e.g. two workers requested the same split).
			continue
		}
		c.run.AddTask(taskID, name, assignment, deps)
		log.Printf("[coordinator] split-request accepted: %s (%s) deps=%v from %s",
			taskID, name, deps, m.Sender)
	}
	if len(msgs) > 0 {
		c.splitCursor = msgs[len(msgs)-1].Seq
	}
}

// intakeGraphRequests scans every thread for the messages that mutate the
// run: dep-edge changes, file claims, and model escalations. Unlike
// split-request, these can come on any thread — a worker discovers a
// relationship, is about to write a file, or finds its area too hard, and
// posts to worklog rather than planning.
func (c *Coordinator) intakeGraphRequests() {
	msgs := c.run.MessagesSince(c.depCursor)
	for _, m := range msgs {
		if action, taskID, deps, ok := broker.ParseDepEdgeRequest(m.Content); ok {
			c.applyDepEdge(action, taskID, deps, m.Sender)
			continue
		}
		if action, taskID, paths, ok := broker.ParseFileClaimRequest(m.Content); ok {
			c.applyFileClaim(action, taskID, paths, m.Sender)
			continue
		}
		if taskID, model, reason, ok := broker.ParseModelEscalateRequest(m.Content); ok {
			c.applyModelEscalation(taskID, model, reason, m.Sender)
		}
	}
	if len(msgs) > 0 {
		c.depCursor = msgs[len(msgs)-1].Seq
	}
}

// applyModelEscalation retries a task on a stronger model. The worker asks
// for this when its area turns out to need more than the tier it was given;
// without it, a cheap worker's thin answer is the run's final answer.
//
// The escalation reuses the dispatch-failure path so the circuit breaker
// still bounds retries: a task cannot escalate its way past
// MaxDispatchFailures attempts.
func (c *Coordinator) applyModelEscalation(taskID, model, reason, sender string) {
	task := c.run.GetTask(taskID)
	if task == nil {
		log.Printf("[coordinator] escalation for unknown task %s, ignoring", taskID)
		return
	}

	current := task.CurrentModel()
	target := model
	if target == "" {
		next, ok := broker.NextModelTier(current)
		if !ok {
			log.Printf("[coordinator] %s already at the strongest tier (%s); not escalating",
				taskID, current)
			return
		}
		target = next
	}
	if target == current {
		log.Printf("[coordinator] %s already on %s; not escalating", taskID, target)
		return
	}

	task.SetModel(target)
	log.Printf("[coordinator] escalating %s: %s → %s (%s, from %s)",
		taskID, current, target, reason, sender)

	// Only force a retry if the task is still in flight. A completed task
	// that asks to escalate has already produced its answer; re-running it
	// would discard work the synthesis task may already have drained.
	if task.CurrentState() == broker.TaskDispatched {
		task.FailDispatch("escalated to "+target+": "+reason, c.run)
	}
}

func (c *Coordinator) applyDepEdge(action, taskID string, deps []string, sender string) {
	task := c.run.GetTask(taskID)
	if task == nil {
		log.Printf("[coordinator] dep-edge for unknown task %s, ignoring", taskID)
		return
	}
	switch action {
	case broker.DepAddPrefix:
		task.AddDep(deps, c.run)
		log.Printf("[coordinator] dep-edge added: %s ← %v from %s", taskID, deps, sender)
	case broker.DepRemovePrefix:
		task.RemoveDep(deps)
		log.Printf("[coordinator] dep-edge removed: %s ⊘ %v from %s", taskID, deps, sender)
	}
}

// applyFileClaim records or releases file ownership. A conflicting claim is
// answered on the radio rather than silently dropped: the requesting worker
// has to know who holds the file, or it will go ahead and overwrite.
func (c *Coordinator) applyFileClaim(action, taskID string, paths []string, sender string) {
	switch action {
	case broker.FileClaimPrefix:
		conflicts := c.run.ClaimFiles(taskID, paths)
		if len(conflicts) == 0 {
			log.Printf("[coordinator] files claimed by %s: %v", taskID, paths)
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "FILE_CONFLICT: these paths are already held by another task.\n")
		for p, owner := range conflicts {
			fmt.Fprintf(&b, "  %s → held by %s\n", p, owner)
		}
		fmt.Fprintf(&b, "Coordinate on the radio before writing; do not overwrite.")
		c.run.PostMessage("worklog", "coordinator", []string{sender}, broker.PriorityUrgent, b.String())
		log.Printf("[coordinator] file conflict for %s: %v", taskID, conflicts)
	case broker.FileReleasePrefix:
		c.run.ReleaseFiles(taskID, paths)
		log.Printf("[coordinator] files released by %s: %v", taskID, paths)
	}
}

// hasReadyOrRunning reports whether the graph can still make progress.
func (c *Coordinator) hasReadyOrRunning() bool {
	c.mu.Lock()
	if len(c.running) > 0 {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	return len(c.run.ReadyTasks()) > 0
}
