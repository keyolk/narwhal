// dispatch.go runs the daemon's background dispatch loop.
//
// The batch CLI drives its graph with the coordinator package: find ready
// tasks, launch them, reap finished workers, repeat. The daemon originally
// had no equivalent — it only reacted to spawn requests — so a task created
// with unmet deps became ready when its deps completed and then sat there
// forever, because nothing was watching. A DAG you cannot execute is not a
// DAG, so the daemon needs its own loop.
//
// This loop is deliberately thinner than coordinator.Coordinator: it never
// decides a run is finished (an interactive run has no natural end — the
// user may add work at any time) and it does not enforce a global
// concurrency cap across runs, only per run.
package daemon

import (
	"log"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/server"
)

// DispatchInterval is how often the loop looks for newly ready tasks.
// Tasks become ready when a worker calls task-done, which is a human-scale
// event, so sub-second polling would only burn CPU.
const DispatchInterval = 500 * time.Millisecond

// MaxConcurrentPerRun caps simultaneous workers within one run. Workers
// share one ccproxy account pool, so an unbounded fan-out would drain quota
// and starve the user's own interactive session.
const MaxConcurrentPerRun = 4

// Dispatcher watches a session's runs and launches tasks as they become
// ready.
type Dispatcher struct {
	sess *Session

	mu      sync.Mutex
	running map[string]string // taskKey → agentID
	stop    chan struct{}
}

// NewDispatcher creates a dispatcher for a session.
func NewDispatcher(sess *Session) *Dispatcher {
	return &Dispatcher{
		sess:    sess,
		running: make(map[string]string),
		stop:    make(chan struct{}),
	}
}

// Start begins the loop. Call Stop to end it.
func (d *Dispatcher) Start() {
	go d.loop()
}

// Stop ends the loop.
func (d *Dispatcher) Stop() {
	select {
	case <-d.stop:
		// already stopped
	default:
		close(d.stop)
	}
}

func (d *Dispatcher) loop() {
	ticker := time.NewTicker(DispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.tick()
		}
	}
}

// tick reaps exited workers and dispatches whatever became ready.
func (d *Dispatcher) tick() {
	for _, runID := range d.sess.ActiveRuns() {
		run := d.sess.Broker.GetRun(runID)
		l := d.sess.Launcher(runID)
		if run == nil || l == nil {
			continue
		}
		d.reap(runID, run, l.ActiveWorkers())
		d.dispatchReady(runID, run)
	}
}

// reap notices workers that exited. A worker that exits without calling
// task-done is recorded as a failed dispatch, which retries until the
// circuit breaker trips — the same contract the batch coordinator uses, so
// a task cannot silently disappear because its process died.
func (d *Dispatcher) reap(runID string, run *broker.Run, active []string) {
	live := make(map[string]bool, len(active))
	for _, a := range active {
		live[a] = true
	}

	d.mu.Lock()
	var exited []string
	for key, agentID := range d.running {
		if runOf(key) != runID {
			continue
		}
		if !live[agentID] {
			exited = append(exited, key)
		}
	}
	for _, key := range exited {
		delete(d.running, key)
	}
	d.mu.Unlock()

	for _, key := range exited {
		task := run.GetTask(taskOf(key))
		if task == nil {
			continue
		}
		switch task.CurrentState() {
		case broker.TaskCompleted, broker.TaskFailed:
			// Worker declared its own outcome; nothing to record.
		default:
			log.Printf("[dispatch] %s/%s exited without task-done; recording failure",
				runID, task.ID)
			task.FailDispatch("worker exited without calling task-done", run)
		}
	}
}

// dispatchReady launches dispatchable tasks up to the per-run concurrency cap.
func (d *Dispatcher) dispatchReady(runID string, run *broker.Run) {
	// Dispatchable rather than ready: a synthesis task starts alongside
	// its dependencies so it can listen while they work, and the broker
	// gates its completion instead. The batch coordinator uses the same
	// helper — a rule only one dispatcher knew would silently not apply
	// to interactive runs, which is where most runs happen.
	ready := run.DispatchableTasks()
	if len(ready) == 0 {
		return
	}

	d.mu.Lock()
	inFlight := 0
	for key := range d.running {
		if runOf(key) == runID {
			inFlight++
		}
	}
	slots := MaxConcurrentPerRun - inFlight
	d.mu.Unlock()

	if slots <= 0 {
		return
	}

	l := d.sess.Launcher(runID)
	if l == nil {
		return
	}

	for _, task := range ready {
		if slots <= 0 {
			return
		}
		key := runID + "\x00" + task.ID

		d.mu.Lock()
		_, busy := d.running[key]
		if !busy {
			// Claim the slot before launching so a slow SetupAgent cannot
			// let the next tick dispatch the same task twice.
			d.running[key] = "worker-" + task.ID
		}
		d.mu.Unlock()
		if busy {
			continue
		}

		if err := server.DispatchTask(d.sess.Registry, l, run, task); err != nil {
			log.Printf("[dispatch] %s/%s failed: %v", runID, task.ID, err)
			d.mu.Lock()
			delete(d.running, key)
			d.mu.Unlock()
			continue
		}
		log.Printf("[dispatch] %s/%s launched", runID, task.ID)
		slots--
	}
}

// runOf and taskOf split the composite key used in the running map. A
// composite key keeps one map for all runs while allowing two runs to have
// tasks with the same id.
func runOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i]
		}
	}
	return key
}

func taskOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[i+1:]
		}
	}
	return ""
}
