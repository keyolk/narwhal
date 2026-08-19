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
	// saved is the last-written fingerprint per run, so the tick only
	// touches disk when the graph actually changed.
	saved map[string]runFingerprint
	// cursors is how far the radio has been read per run, so a request is
	// applied once rather than on every tick.
	cursors map[string]broker.IntakeCursors
	stop    chan struct{}
	// done closes when the loop has returned, so Stop can wait for a tick
	// that is mid-write.
	done chan struct{}
}

// NewDispatcher creates a dispatcher for a session.
func NewDispatcher(sess *Session) *Dispatcher {
	return &Dispatcher{
		sess:    sess,
		running: make(map[string]string),
		stop:    make(chan struct{}),
	}
}

// AdoptRunning seeds the running set with workers that survived a restart.
//
// Without this the first tick sees a dispatched task with nothing tracked
// against it, calls that a failed dispatch, and launches a second worker
// on a task that is already being worked — turning a recovery into the
// duplicate it was meant to prevent.
func (d *Dispatcher) AdoptRunning(running map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, agentID := range running {
		d.running[key] = agentID
	}
}

// Start begins the loop. Call Stop to end it.
func (d *Dispatcher) Start() {
	d.done = make(chan struct{})
	go d.loop()
}

// Stop ends the loop and waits for the current tick to finish.
//
// Waiting matters because a tick writes to disk. Returning early let a
// half-finished tick outlive whatever set up its environment — in tests
// that meant persisting a run to the developer's real ~/.narwhal after
// t.Setenv had restored HOME, and three test runs ended up in the run
// picker beside real ones.
func (d *Dispatcher) Stop() {
	select {
	case <-d.stop:
		// already stopped
	default:
		close(d.stop)
	}
	if d.done != nil {
		<-d.done
	}
}

func (d *Dispatcher) loop() {
	defer close(d.done)
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
		// A canceled run is done being driven. Cancel kills the workers and
		// sets the state, but nothing used to read it back, so the next
		// tick cheerfully launched them again — a run cancelled before its
		// first task started would start it anyway. Reap and persist still
		// run: the processes that were killed need recording, and the
		// final state is worth keeping.
		if run.CurrentState() == broker.RunCanceled {
			d.reap(runID, run, l.ActiveWorkers())
			d.persistRun(runID, run)
			d.retireIfSettled(runID, run, l.ActiveWorkers())
			continue
		}
		// Read the radio before touching the graph. Workers are given six
		// wrapper scripts that mutate the run — split, dep-add, dep-remove,
		// file-claim, file-release, escalate — and every one of them was
		// silently inert here: the daemon only reaped and dispatched, so a
		// worker's split request sat on the radio unread. Nearly every run
		// is interactive, so the documented half of the worker protocol did
		// not work where it was actually used.
		d.intake(runID, run)
		// Before reaping: a worker that could not reach the broker wrote
		// its answer to disk and the task is still dispatched. reap would
		// see an exited process with no completion and call it a failed
		// dispatch, throwing away work that was done and recorded.
		harvestOrphanedOutcomes(runID, run)
		d.reap(runID, run, l.ActiveWorkers())
		d.dispatchReady(runID, run)
		// Persist after the graph has settled for this tick, so a crash
		// or a restart leaves the run readable rather than erasing it.
		// The write is gated on an actual change, so an idle run costs
		// nothing.
		d.persistRun(runID, run)
		d.retireIfSettled(runID, run, l.ActiveWorkers())
	}
}

// retireIfSettled drops a run's launcher once there is nothing left to do.
//
// DropLauncher existed and was never called, so a launcher lived for the
// life of the daemon. ActiveRuns drives the monitor's run picker, this tick,
// and the guard that refuses to stop while workers run — so finished runs
// piled up in all three: the picker filled with runs that ended hours ago,
// and the stop guard could be tripped by one of them.
//
// Settled means every task is terminal and no worker is alive. An
// interactive run has no natural end — the user can add work at any time —
// so the run itself is left in the broker and can be dispatched again if a
// new task appears; only the launcher is released. The run's final state is
// persisted first, so retiring never loses the record.
func (d *Dispatcher) retireIfSettled(runID string, run *broker.Run, active []string) {
	if len(active) > 0 {
		return
	}
	tasks := run.SnapshotTasks()
	if len(tasks) == 0 {
		// A run with no tasks yet is one whose first spawn is still in
		// flight, not a finished one.
		return
	}
	for _, t := range tasks {
		switch t.State {
		case broker.TaskCompleted, broker.TaskFailed:
		default:
			return
		}
	}

	d.mu.Lock()
	for key := range d.running {
		if runOf(key) == runID {
			// A worker is still being tracked even though the launcher
			// reports none alive; let the next tick reap it first.
			d.mu.Unlock()
			return
		}
	}
	delete(d.saved, runID)
	delete(d.cursors, runID)
	d.mu.Unlock()

	if run.CurrentState() == broker.RunActive {
		// Nothing else sets this on the interactive path, so an
		// interactive run stayed "active" forever — including in the
		// monitor header and in every persisted snapshot.
		//
		// Which terminal state mirrors the batch coordinator: any failed
		// task makes the run failed. It used to say done unconditionally,
		// so a run whose every task failed was recorded as a success —
		// s1786665438376-1 on disk is exactly that, 0/2 and "done".
		//
		// The coordinator also fails a run with unreached tasks, and that
		// clause is deliberately not copied. It means "the batch timeout
		// expired with work outstanding", and there is no such deadline
		// here: an interactive run with an unreached task is idle, not
		// failed, and retireIfSettled has already returned above in that
		// case.
		state := broker.RunDone
		for _, t := range tasks {
			if t.State == broker.TaskFailed {
				state = broker.RunFailed
				break
			}
		}
		run.SetState(state)
		_ = d.persistRun(runID, run)
	}
	d.sess.DropLauncher(runID)
	log.Printf("[dispatch] %s settled; launcher retired", runID)
}

// intake applies the graph-mutating requests a run's workers have posted
// since the last tick.
func (d *Dispatcher) intake(runID string, run *broker.Run) {
	d.mu.Lock()
	if d.cursors == nil {
		d.cursors = make(map[string]broker.IntakeCursors)
	}
	cur := d.cursors[runID]
	d.mu.Unlock()

	cur.Split = run.IntakeSplitRequests(cur.Split)
	cur.Graph = run.IntakeGraphRequests(cur.Graph)

	d.mu.Lock()
	d.cursors[runID] = cur
	d.mu.Unlock()
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
		// Give up whatever files the worker still held. A claim outlives
		// the process that made it, so a worker that exits before its
		// FILE_RELEASE strands the path for the rest of the run and every
		// peer that asks for it is told to negotiate with a dead task.
		run.ReleaseTaskFiles(task.ID)
		switch task.CurrentState() {
		case broker.TaskCompleted, broker.TaskFailed:
			// Worker declared its own outcome; nothing to record.
		default:
			// On a canceled run the worker did not exit — it was killed,
			// by us. Recording that as a failed dispatch would blame the
			// task for the user's decision and, before the tick learned to
			// skip canceled runs, would have driven a retry.
			if run.CurrentState() == broker.RunCanceled {
				log.Printf("[dispatch] %s/%s killed by cancel", runID, task.ID)
				task.CancelDispatch("run canceled")
				continue
			}
			// A worker that posted findings did its job even if it never
			// called task-done — retrying it would redo work already on
			// the radio, and on the third retry the breaker would fail a
			// task whose output exists.
			if run.AgentPostedToRadio("worker-" + task.ID) {
				log.Printf("[dispatch] %s/%s exited without task-done but posted to radio; marking complete",
					runID, task.ID)
				task.CompleteDispatch("completed via radio activity", run)
				continue
			}
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
