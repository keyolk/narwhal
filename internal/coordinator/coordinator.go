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

	mu       sync.Mutex
	running  map[string]string // taskID → agentID
	finished map[string]bool   // taskID → true once terminal
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
	RunID      string
	Completed  []string
	Failed     []string
	Unreached  []string // still pending/blocked when the loop stopped
	TimedOut   bool
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
	for _, t := range c.run.SnapshotTasks() {
		switch t.State {
		case broker.TaskCompleted:
			res.Completed = append(res.Completed, t.ID)
		case broker.TaskFailed:
			res.Failed = append(res.Failed, t.ID)
		default:
			res.Unreached = append(res.Unreached, t.ID)
		}
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

	ready := c.run.ReadyTasks()
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
	var exited []string
	for taskID, agentID := range c.running {
		if !active[agentID] {
			exited = append(exited, taskID)
		}
	}
	for _, taskID := range exited {
		delete(c.running, taskID)
	}
	c.mu.Unlock()

	for _, taskID := range exited {
		task := c.run.GetTask(taskID)
		if task == nil {
			continue
		}
		state := task.CurrentState()
		if state == broker.TaskCompleted || state == broker.TaskFailed {
			c.mu.Lock()
			c.finished[taskID] = true
			c.mu.Unlock()
			continue
		}
		// Worker exited without declaring completion.
		log.Printf("[coordinator] %s exited without task-done; recording failure", taskID)
		task.FailDispatch("worker exited without calling task-done", c.run)
		if task.CurrentState() == broker.TaskFailed {
			c.mu.Lock()
			c.finished[taskID] = true
			c.mu.Unlock()
		}
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
