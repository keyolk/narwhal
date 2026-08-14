// Package broker implements the in-process message broker for Narwhal runs.
//
// The broker owns three layers of state, mirroring the design split between
// Orca-style graph engineering and AgentRadio-style passive awareness:
//
//   - Graph layer: Run, Task, Dispatch. Tasks form a DAG via Deps edges.
//     A task transitions pending → ready → dispatched → completed/failed.
//     Existing tasks are immutable; new tasks can be added (split-request)
//     but never edited in place.
//
//   - Radio layer: Thread, Message. Each Run has a radio channel with named
//     threads (planning, worklog, results). Messages carry a monotonic Seq
//     cursor so watchers can drain without losing or duplicating entries.
//
//   - Identity: each agent gets a cryptographic token that doubles as its
//     broker endpoint path, so the sender identity is derived from the URL
//     rather than trusting a field in the message body.
//
// The broker is intentionally process-local and in-memory for phase 1.
// Persistence is a later concern; the first goal is proving that directly
// executed Claude Code workers can send and receive messages via background
// Bash watchers with real completion notifications.
package broker

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RunState is the lifecycle status of a Run.
type RunState string

const (
	RunActive   RunState = "active"
	RunDone     RunState = "done"
	RunFailed   RunState = "failed"
	RunCanceled RunState = "canceled"
)

// TaskState is the lifecycle status of a Task within the DAG.
type TaskState string

const (
	TaskPending    TaskState = "pending"
	TaskReady      TaskState = "ready"
	TaskDispatched TaskState = "dispatched"
	TaskCompleted  TaskState = "completed"
	TaskFailed     TaskState = "failed"
	TaskBlocked    TaskState = "blocked"
)

// Priority classifies a radio message so receivers can triage without
// stopping their foreground work.
type Priority string

const (
	PriorityFYI    Priority = "fyi"    // no reply needed; receivers take note
	PriorityNormal Priority = "normal" // reply at a natural break
	PriorityUrgent Priority = "urgent" // receiver's current work may be affected
)

// DispatchStatus tracks one worker attempt at a Task.
type DispatchStatus string

const (
	DispatchRunning DispatchStatus = "running"
	DispatchDone    DispatchStatus = "done"
	DispatchFailed  DispatchStatus = "failed"
	DispatchTimeout DispatchStatus = "timeout"
)

// MaxDispatchFailures is the circuit-breaker threshold. After this many
// failed dispatches a task is marked TaskFailed rather than retried.
const MaxDispatchFailures = 3

// SplitRequestPrefix marks a radio message as a request to add a new task
// to the run. The coordinator scans for messages with this prefix on the
// planning thread and creates the requested task. This is the only way the
// graph grows mid-run: existing tasks are immutable, new ones are appended.
//
// Format: "SPLIT_REQUEST|<taskId>|<name>|<assignment>|<dep1,dep2,...>"
const SplitRequestPrefix = "SPLIT_REQUEST"

// DepEdgePrefix marks a radio message as a request to add or remove a
// dependency edge on an existing task. Unlike split-request (which mints
// a new task), a dep-edge request mutates only the dependency list —
// the task itself (id, name, assignment) stays immutable. This is the
// generalization of split-request that Dynamic Graph Communication
// (arXiv:2501.00165) motivates: workers discover relationships
// mid-run and adjust the graph without a planner round.
//
// Format: "DEP_ADD|<taskId>|<dep1,dep2,...>"
//
//	"DEP_REMOVE|<taskId>|<dep1,dep2,...>"
const (
	DepAddPrefix    = "DEP_ADD"
	DepRemovePrefix = "DEP_REMOVE"
)

// File-claim prefixes let workers coordinate writes to shared files.
// Cursor's swarm experiment cut merge conflicts from 70,000 to under
// 1,000 by giving each agent clear ownership rather than letting them
// all write freely; this is the same idea at radio granularity.
//
// A worker claims the files it is about to modify. The coordinator keeps
// the claim map and answers conflicting claims by telling the second
// worker who holds the file, so it can negotiate on the radio instead of
// silently overwriting. Releasing is explicit — a worker that finishes
// with a file gives it up so a peer can take it.
//
// Format: "FILE_CLAIM|<taskId>|<path1,path2,...>"
//
//	"FILE_RELEASE|<taskId>|<path1,path2,...>"
const (
	FileClaimPrefix   = "FILE_CLAIM"
	FileReleasePrefix = "FILE_RELEASE"
)

// ModelEscalatePrefix marks a radio message as a request to retry a task on
// a stronger model. A worker that finds its area harder than the planner
// assumed asks for the escalation rather than producing a thin answer on a
// model that cannot do the work.
//
// The benchmark showed why this matters: haiku investigators aced narrow
// tasks but missed rubrics that needed opus-level reading, and nothing in
// the run could notice and correct it. Escalation is the correction path.
//
// Format: "MODEL_ESCALATE|<taskId>|<model>|<reason>"
const ModelEscalatePrefix = "MODEL_ESCALATE"

// ParseModelEscalateRequest extracts the fields from a MODEL_ESCALATE body.
// The model may be empty, meaning "one tier up, coordinator's choice".
func ParseModelEscalateRequest(content string) (taskID, model, reason string, ok bool) {
	if !strings.HasPrefix(content, ModelEscalatePrefix) {
		return "", "", "", false
	}
	rest := content[len(ModelEscalatePrefix):]
	if len(rest) == 0 || rest[0] != '|' {
		return "", "", "", false
	}
	parts := strings.SplitN(rest[1:], "|", 3)
	if len(parts) < 2 {
		return "", "", "", false
	}
	if len(parts) == 3 {
		reason = parts[2]
	}
	return parts[0], parts[1], reason, true
}

// FormatModelEscalateRequest builds a MODEL_ESCALATE message body.
func FormatModelEscalateRequest(taskID, model, reason string) string {
	return ModelEscalatePrefix + "|" + taskID + "|" + model + "|" + reason
}

// NextModelTier returns the model one step stronger than current, and
// ok=false when there is nothing stronger to escalate to. The ladder is
// deliberately short: these are tiers Narwhal asks for, and the backend
// decides which concrete model serves each tier.
func NextModelTier(current string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "", "haiku":
		return "sonnet", true
	case "sonnet":
		return "opus", true
	default:
		return "", false
	}
}

// ParseSplitRequest extracts the fields from a split-request message body.
// Returns ok=false if the body is not a well-formed split request.
func ParseSplitRequest(content string) (taskID, name, assignment string, deps []string, ok bool) {
	if len(content) < len(SplitRequestPrefix) || content[:len(SplitRequestPrefix)] != SplitRequestPrefix {
		return "", "", "", nil, false
	}
	rest := content[len(SplitRequestPrefix):]
	if len(rest) == 0 || rest[0] != '|' {
		return "", "", "", nil, false
	}
	parts := strings.SplitN(rest[1:], "|", 4)
	if len(parts) < 3 {
		return "", "", "", nil, false
	}
	taskID = parts[0]
	name = parts[1]
	assignment = parts[2]
	if len(parts) == 4 && parts[3] != "" {
		deps = strings.Split(parts[3], ",")
	}
	return taskID, name, assignment, deps, true
}

// FormatSplitRequest builds a split-request message body. Used by the
// wrapper script and by tests.
func FormatSplitRequest(taskID, name, assignment string, deps []string) string {
	depStr := ""
	if len(deps) > 0 {
		depStr = strings.Join(deps, ",")
	}
	return SplitRequestPrefix + "|" + taskID + "|" + name + "|" + assignment + "|" + depStr
}

// ParseDepEdgeRequest extracts the fields from a DEP_ADD or DEP_REMOVE
// message body. action is the prefix that matched. Returns ok=false if
// the body is not well-formed.
func ParseDepEdgeRequest(content string) (action, taskID string, deps []string, ok bool) {
	return parsePrefixedList(content, DepAddPrefix, DepRemovePrefix)
}

// FormatDepEdgeRequest builds a DEP_ADD or DEP_REMOVE message body.
func FormatDepEdgeRequest(action, taskID string, deps []string) string {
	return action + "|" + taskID + "|" + strings.Join(deps, ",")
}

// ParseFileClaimRequest extracts the fields from a FILE_CLAIM or
// FILE_RELEASE message body. action is the prefix that matched.
func ParseFileClaimRequest(content string) (action, taskID string, paths []string, ok bool) {
	return parsePrefixedList(content, FileClaimPrefix, FileReleasePrefix)
}

// FormatFileClaimRequest builds a FILE_CLAIM or FILE_RELEASE message body.
func FormatFileClaimRequest(action, taskID string, paths []string) string {
	return action + "|" + taskID + "|" + strings.Join(paths, ",")
}

// parsePrefixedList parses the "<PREFIX>|<taskId>|<a,b,c>" shape shared by
// the dep-edge and file-claim messages. The trailing list may be empty.
func parsePrefixedList(content string, prefixes ...string) (action, taskID string, items []string, ok bool) {
	for _, p := range prefixes {
		if !strings.HasPrefix(content, p) {
			continue
		}
		rest := content[len(p):]
		if len(rest) == 0 || rest[0] != '|' {
			return "", "", nil, false
		}
		parts := strings.SplitN(rest[1:], "|", 2)
		taskID = parts[0]
		if len(parts) == 2 && parts[1] != "" {
			items = strings.Split(parts[1], ",")
		}
		return p, taskID, items, true
	}
	return "", "", nil, false
}

// Run is a namespace holding one or more task graphs plus a radio channel.
// A Run is "a namespace, not a DAG" — it can hold several unrelated graphs.
type Run struct {
	mu          sync.RWMutex
	ID          string
	Prompt      string
	CWD         string
	State       RunState
	CreatedAt   time.Time
	Coordinator string // agent id currently bound as coordinator
	Tasks       map[string]*Task
	Threads     map[string]*Thread
	seqCounter  int64       // monotonic message sequence, global per Run
	messages    []*Message  // append-only log, indexed by Seq-1
	msgMu       sync.Mutex  // guards messages slice and watcher signaling
	watchers    []watchSink // active long-poll sessions waiting for new messages

	// fileClaims maps a path to the task that claimed it. Guarded by
	// claimMu rather than mu so a claim check never contends with graph
	// traversal.
	fileClaims map[string]string
	claimMu    sync.RWMutex
}

// ClaimFiles records ownership of paths for a task. Paths already held by
// another task are returned as conflicts and are NOT reassigned — the
// caller tells the requesting worker who holds them so it can negotiate on
// the radio. Re-claiming a path the same task already owns is a no-op.
func (r *Run) ClaimFiles(taskID string, paths []string) (conflicts map[string]string) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	if r.fileClaims == nil {
		r.fileClaims = make(map[string]string)
	}
	for _, p := range paths {
		if owner, held := r.fileClaims[p]; held && owner != taskID {
			if conflicts == nil {
				conflicts = make(map[string]string)
			}
			conflicts[p] = owner
			continue
		}
		r.fileClaims[p] = taskID
	}
	return conflicts
}

// ReleaseFiles gives up paths held by a task. Paths held by a different
// task are left alone: a worker cannot release someone else's claim.
func (r *Run) ReleaseFiles(taskID string, paths []string) {
	r.claimMu.Lock()
	defer r.claimMu.Unlock()
	for _, p := range paths {
		if r.fileClaims[p] == taskID {
			delete(r.fileClaims, p)
		}
	}
}

// FileOwner returns the task holding a path, or "" if unclaimed.
func (r *Run) FileOwner(path string) string {
	r.claimMu.RLock()
	defer r.claimMu.RUnlock()
	return r.fileClaims[path]
}

// FileClaims returns a copy of the claim map, for snapshots and the monitor.
func (r *Run) FileClaims() map[string]string {
	r.claimMu.RLock()
	defer r.claimMu.RUnlock()
	out := make(map[string]string, len(r.fileClaims))
	for k, v := range r.fileClaims {
		out[k] = v
	}
	return out
}

// watchSink is fed to long-poll watchers so PostMessage can wake them.
type watchSink struct {
	agentID string
	wake    chan struct{}
}

// MessagesSince returns all messages with Seq > after, in order. This is
// the core of both drain (non-blocking) and watch (after long-poll resolves).
func (r *Run) MessagesSince(after int64) []*Message {
	r.msgMu.Lock()
	defer r.msgMu.Unlock()
	var out []*Message
	for _, m := range r.messages {
		if m.Seq > after {
			out = append(out, m)
		}
	}
	return out
}

// MessagesMentioning returns messages with Seq > after that mention agentID
// OR were posted to any thread the agent can see (in phase 1, all threads).
func (r *Run) MessagesMentioning(after int64, agentID string) []*Message {
	r.msgMu.Lock()
	defer r.msgMu.Unlock()
	var out []*Message
	for _, m := range r.messages {
		if m.Seq <= after {
			continue
		}
		// Mention check, or broadcast (no mentions = all).
		if len(m.Mentions) == 0 {
			out = append(out, m)
			continue
		}
		for _, mention := range m.Mentions {
			if mention == agentID {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// RegisterWatch adds a long-poll wake channel for an agent. Returns a
// removal function to call when the watch ends. PostMessage will signal
// the channel when a new message arrives mentioning the agent (or a
// broadcast message with no mentions).
func (r *Run) RegisterWatch(agentID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	sink := watchSink{agentID: agentID, wake: ch}
	r.msgMu.Lock()
	r.watchers = append(r.watchers, sink)
	r.msgMu.Unlock()
	remove := func() {
		r.msgMu.Lock()
		defer r.msgMu.Unlock()
		for i, w := range r.watchers {
			if w == sink {
				r.watchers = append(r.watchers[:i], r.watchers[i+1:]...)
				return
			}
		}
	}
	return ch, remove
}

// Task is a unit of work within a Run. Deps define DAG edges: a task is
// ready only when all Deps are completed. Tasks are immutable once created;
// the only mutation allowed is state transition and dispatch append.
type Task struct {
	mu         sync.RWMutex
	ID         string
	RunID      string
	Name       string
	Assignment string
	Deps       []string
	State      TaskState
	CreatedAt  time.Time
	Dispatches []*Dispatch
	// Model overrides the launcher's default worker model for this task.
	// Empty means use the launcher default. Set by the planner when a task
	// needs a specific model — e.g. a synthesis task on opus, investigation
	// tasks on haiku. See bench/run_hybrid.sh and the Cursor economics note
	// in README.md.
	Model string
}

// Dispatch is one attempt to execute a Task. A retry mints a new Dispatch;
// the previous one stays in history with its status and failure count.
type Dispatch struct {
	ID           string
	TaskID       string
	AgentID      string
	FailureCount int
	Heartbeat    time.Time
	StartedAt    time.Time
	Status       DispatchStatus
	Output       string
}

// Thread is a named conversation channel within a Run's radio layer.
// Typical threads: "planning", "worklog", "results".
type Thread struct {
	mu           sync.RWMutex
	ID           string
	RunID        string
	Name         string
	Participants []string
	CreatedAt    time.Time
}

// Message is one radio message. Seq is a monotonic cursor global per Run
// (not per thread) so a watcher can drain across all threads with a single
// cursor and never lose or duplicate a message.
type Message struct {
	Seq       int64
	RunID     string
	ThreadID  string
	Sender    string
	Mentions  []string
	Priority  Priority
	Content   string
	CreatedAt time.Time
}

// Broker is the process-local, in-memory store for all Runs. It is safe
// for concurrent use by the HTTP server, the launcher, and any viewers.
type Broker struct {
	mu   sync.RWMutex
	runs map[string]*Run
}

// New returns a fresh empty Broker.
func New() *Broker {
	return &Broker{runs: make(map[string]*Run)}
}

// CreateRun initializes a new Run with no tasks or threads.
func (b *Broker) CreateRun(id, prompt, cwd, coordinator string) *Run {
	r := &Run{
		ID:          id,
		Prompt:      prompt,
		CWD:         cwd,
		State:       RunActive,
		CreatedAt:   time.Now(),
		Coordinator: coordinator,
		Tasks:       make(map[string]*Task),
		Threads:     make(map[string]*Thread),
	}
	b.mu.Lock()
	b.runs[id] = r
	b.mu.Unlock()
	return r
}

// GetRun returns a Run by id, or nil if not found.
func (b *Broker) GetRun(id string) *Run {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.runs[id]
}

// AddTask creates a new task in the run. Existing tasks are immutable, so
// addition is the only way to grow the graph (split-request path).
func (r *Run) AddTask(id, name, assignment string, deps []string) *Task {
	t := &Task{
		ID:         id,
		RunID:      r.ID,
		Name:       name,
		Assignment: assignment,
		Deps:       append([]string(nil), deps...),
		State:      TaskPending,
		CreatedAt:  time.Now(),
	}
	r.mu.Lock()
	r.Tasks[id] = t
	// Recompute readiness: a task with all deps completed is ready.
	for _, t := range r.Tasks {
		t.recomputeReady(r)
	}
	r.mu.Unlock()
	return t
}

// PendingDeps returns the ids of a task's dependencies that have not
// completed yet, in a stable order.
//
// This exists to gate completion rather than dispatch. The synthesis task
// is launched immediately — it spends its life draining the radio as peers
// post, which is the whole reason it does not wait its turn — but it must
// not declare itself done while a peer is still working. Blocking dispatch
// on deps would serialize it; blocking completion keeps the parallelism
// and still guarantees the final answer saw every finding.
func (r *Run) PendingDeps(taskID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t := r.Tasks[taskID]
	if t == nil {
		return nil
	}
	t.mu.RLock()
	deps := append([]string(nil), t.Deps...)
	t.mu.RUnlock()

	var pending []string
	for _, d := range deps {
		dep := r.Tasks[d]
		if dep == nil {
			// A dep naming a task that does not exist cannot block: the
			// graph layer already draws these as unreachable rather than
			// silently dropping them, and waiting forever on a typo is
			// worse than proceeding.
			continue
		}
		dep.mu.RLock()
		state := dep.State
		dep.mu.RUnlock()
		if state != TaskCompleted && state != TaskFailed {
			pending = append(pending, d)
		}
	}
	sort.Strings(pending)
	return pending
}

// DispatchableTasks returns every task a dispatcher may launch now: the
// ready ones, plus synthesis tasks still pending on their deps.
//
// A synthesis worker's job is to be listening while its peers work — it
// keeps a watcher on the radio and folds findings in as they land — which
// only works if it is alive at the same time as them. Its deps are still
// real: task-done is refused until they finish (see PendingDeps). So the
// dependency is a completion gate, not a dispatch gate.
//
// This lives on the Run because there are two dispatchers — the batch
// coordinator and the daemon — and a rule that only one of them knows is a
// rule that silently does not apply to interactive runs.
func (r *Run) DispatchableTasks() []*Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Task
	for _, t := range r.Tasks {
		t.mu.RLock()
		state := t.State
		isSynth := isSynthesisName(t.Name, t.Assignment)
		t.mu.RUnlock()
		if state == TaskReady || (state == TaskPending && isSynth) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IsSynthesis reports whether this task is the synthesis step.
func (t *Task) IsSynthesis() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return isSynthesisName(t.Name, t.Assignment)
}

// isSynthesisName reports whether a task is the synthesis step — the one
// that drains peer findings and writes the final answer. The planner marks
// it by convention rather than with a flag, since the task is created
// through the same API as any other.
func isSynthesisName(name, assignment string) bool {
	if strings.Contains(strings.ToLower(name), "synthesis") {
		return true
	}
	return strings.Contains(strings.ToLower(assignment), "synthesis task")
}

// AgentPostedToRadio reports whether an agent sent any message to the
// run's radio channel.
//
// A worker that posted findings but exited without calling task-done still
// produced usable output — peers and the synthesis task can drain it — so
// the dispatcher marks the task complete rather than retrying and spending
// another full worker run on work already done.
//
// This lives on the Run because both dispatchers need it. The check
// existed on the batch coordinator only, and the interactive path failed
// tasks whose workers had posted their findings — the same
// one-dispatcher-knows-the-rule split that DispatchableTasks had.
func (r *Run) AgentPostedToRadio(agentID string) bool {
	for _, m := range r.MessagesSince(0) {
		if m.Sender == agentID {
			return true
		}
	}
	return false
}

// GetTask returns a task by id within the run.
func (r *Run) GetTask(id string) *Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Tasks[id]
}

// SetState updates the run's lifecycle state. Used by the coordinator to
// transition a run to done/failed when the dispatch loop finishes.
func (r *Run) SetState(s RunState) {
	r.mu.Lock()
	r.State = s
	r.mu.Unlock()
}

// CurrentState reads the run's lifecycle state under the lock.
//
// SetState takes the lock, so reading the field directly is a data race —
// and the dispatch loop reads it on every tick while an HTTP handler can be
// setting it.
func (r *Run) CurrentState() RunState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.State
}

// SnapshotTasks returns a stable view of every task's id and state, for
// callers that need to classify the graph without holding locks.
func (r *Run) SnapshotTasks() []TaskSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TaskSnapshot, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		t.mu.RLock()
		out = append(out, TaskSnapshot{
			ID:         t.ID,
			Name:       t.Name,
			Assignment: t.Assignment,
			Deps:       append([]string(nil), t.Deps...),
			State:      t.State,
			Dispatches: len(t.Dispatches),
			Model:      t.Model,
		})
		t.mu.RUnlock()
	}
	return out
}

// CurrentState returns the task's state under a read lock.
func (t *Task) CurrentState() TaskState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.State
}

// SetModel changes the model tier this task's worker runs on. Used by the
// coordinator when a worker escalates; the next dispatch picks it up.
func (t *Task) SetModel(model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Model = model
}

// CurrentModel returns the task's model tier under a read lock.
func (t *Task) CurrentModel() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Model
}

// DispatchCount returns how many dispatch attempts this task has had.
func (t *Task) DispatchCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.Dispatches)
}

// ReadyTasks returns all tasks currently in TaskReady state, suitable for
// parallel dispatch by the coordinator loop.
func (r *Run) ReadyTasks() []*Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Task
	for _, t := range r.Tasks {
		t.mu.RLock()
		if t.State == TaskReady {
			out = append(out, t)
		}
		t.mu.RUnlock()
	}
	return out
}

// recomputeReady transitions a task from pending to ready when all its
// deps are completed. Must be called under the Run write lock.
func (t *Task) recomputeReady(r *Run) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != TaskPending {
		return
	}
	for _, depID := range t.Deps {
		dep, ok := r.Tasks[depID]
		if !ok {
			return // dep does not exist yet, stay pending
		}
		dep.mu.RLock()
		if dep.State != TaskCompleted {
			dep.mu.RUnlock()
			return
		}
		dep.mu.RUnlock()
	}
	t.State = TaskReady
}

// AddDep appends dependency edges to an existing task. The task itself
// (id, name, assignment) stays immutable — only the dep list changes.
// This is the dep-edge path that generalizes split-request: a worker
// discovers a relationship mid-run and adjusts the graph without a planner
// round. Adding a dep on a completed task is fine; recomputeReady is
// called by the coordinator's next tick.
func (t *Task) AddDep(deps []string, r *Run) {
	t.mu.Lock()
	t.Deps = append(t.Deps, deps...)
	t.mu.Unlock()
}

// RemoveDep removes dependency edges from an existing task. Edges that no
// longer hold are dropped; a task that loses its last dep becomes ready
// if it was pending only because of that dep.
func (t *Task) RemoveDep(deps []string) {
	t.mu.Lock()
	set := make(map[string]bool, len(deps))
	for _, d := range deps {
		set[d] = true
	}
	filtered := t.Deps[:0]
	for _, d := range t.Deps {
		if !set[d] {
			filtered = append(filtered, d)
		}
	}
	t.Deps = filtered
	t.mu.Unlock()
}

// StartDispatch records a new dispatch attempt on a task and marks it
// dispatched. Returns the new Dispatch.
func (t *Task) StartDispatch(id, agentID string) *Dispatch {
	d := &Dispatch{
		ID:        id,
		TaskID:    t.ID,
		AgentID:   agentID,
		StartedAt: time.Now(),
		Heartbeat: time.Now(),
		Status:    DispatchRunning,
	}
	t.mu.Lock()
	t.Dispatches = append(t.Dispatches, d)
	t.State = TaskDispatched
	t.mu.Unlock()
	return d
}

// CompleteDispatch marks the latest dispatch done and the task completed,
// then recomputes dependents' readiness.
func (t *Task) CompleteDispatch(output string, r *Run) {
	t.mu.Lock()
	if len(t.Dispatches) > 0 {
		t.Dispatches[len(t.Dispatches)-1].Status = DispatchDone
		t.Dispatches[len(t.Dispatches)-1].Output = output
	}
	t.State = TaskCompleted
	t.mu.Unlock()
	r.mu.Lock()
	for _, other := range r.Tasks {
		other.recomputeReady(r)
	}
	r.mu.Unlock()
}

// FailDispatch marks the latest dispatch failed. If the total number of
// failed dispatches on this task reaches MaxDispatchFailures the task is
// marked failed; otherwise it returns to ready for a retry.
func (t *Task) FailDispatch(reason string, r *Run) {
	t.mu.Lock()
	failedCount := 0
	if len(t.Dispatches) > 0 {
		d := t.Dispatches[len(t.Dispatches)-1]
		d.Status = DispatchFailed
		d.Output = reason
	}
	for _, d := range t.Dispatches {
		if d.Status == DispatchFailed {
			failedCount++
		}
	}
	if failedCount >= MaxDispatchFailures {
		t.State = TaskFailed
		t.mu.Unlock()
		return
	}
	t.State = TaskReady
	t.mu.Unlock()
}

// CancelDispatch marks a task's in-flight dispatch as failed and puts the
// task in a terminal state.
//
// This is not FailDispatch: that one is the circuit breaker's path and
// returns the task to ready so it can be retried. A task whose worker was
// killed because the user cancelled the run must not be retried — becoming
// ready again is exactly how a cancelled run kept relaunching.
func (t *Task) CancelDispatch(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Dispatches) > 0 {
		d := t.Dispatches[len(t.Dispatches)-1]
		if d.Status == DispatchRunning {
			d.Status = DispatchFailed
			d.Output = reason
		}
	}
	if t.State != TaskCompleted {
		t.State = TaskFailed
	}
}

// CreateThread opens a named conversation thread in the run's radio channel.
func (r *Run) CreateThread(id, name string, participants []string) *Thread {
	th := &Thread{
		ID:           id,
		RunID:        r.ID,
		Name:         name,
		Participants: append([]string(nil), participants...),
		CreatedAt:    time.Now(),
	}
	r.mu.Lock()
	r.Threads[id] = th
	r.mu.Unlock()
	return th
}

// Standard thread ids. Every run gets these four; anything else is created
// on demand by the planner or a worker.
//
// EnvironmentThread carries facts about the shared execution environment
// rather than findings about the question: the build is broken, a tool is
// missing, a quota was hit. Those belong somewhere every worker reads by
// default — Cursor's swarm calls the same idea a field guide. Without a
// dedicated thread the first worker to hit a broken build reports it into
// worklog, where it reads as one more finding, and the next three workers
// rediscover it independently.
const (
	PlanningThread    = "planning"
	WorklogThread     = "worklog"
	ResultsThread     = "results"
	EnvironmentThread = "environment"
)

// CreateStandardThreads opens the four threads every run has. Callers that
// build a run by hand (batch CLI, daemon, plan endpoint) use this so the
// thread set cannot drift between them.
func (r *Run) CreateStandardThreads(participants ...string) {
	if len(participants) == 0 {
		participants = []string{"main"}
	}
	for _, id := range []string{
		PlanningThread, WorklogThread, ResultsThread, EnvironmentThread,
	} {
		r.CreateThread(id, id, participants)
	}
}

// PostMessage appends a message to the run's radio channel, stores it in
// the append-only log, and wakes any active long-poll watchers. The caller
// must set Sender; the broker assigns Seq atomically. Returns the stored
// message (safe to serialize without holding any lock).
func (r *Run) PostMessage(threadID, sender string, mentions []string, priority Priority, content string) *Message {
	seq := atomic.AddInt64(&r.seqCounter, 1)
	m := &Message{
		Seq:       seq,
		RunID:     r.ID,
		ThreadID:  threadID,
		Sender:    sender,
		Mentions:  append([]string(nil), mentions...),
		Priority:  priority,
		Content:   content,
		CreatedAt: time.Now(),
	}
	// Store in log and wake watchers under the message lock so a concurrent
	// drain/watch sees a consistent view (message is in the log before the
	// wake signal is sent).
	r.msgMu.Lock()
	r.messages = append(r.messages, m)
	watchers := append([]watchSink(nil), r.watchers...)
	r.msgMu.Unlock()
	for _, w := range watchers {
		// Only wake watchers who are mentioned (or for broadcast messages).
		shouldWake := len(m.Mentions) == 0
		if !shouldWake {
			for _, mention := range m.Mentions {
				if mention == w.agentID {
					shouldWake = true
					break
				}
			}
		}
		if shouldWake {
			select {
			case w.wake <- struct{}{}:
			default: // watcher already has a pending wake; skip
			}
		}
	}
	return m
}

// Snapshot is a point-in-time view of a Run's graph and radio state, used
// by the HTTP API and the viewer. It is safe to serialize and hand to
// callers without holding any locks.
type Snapshot struct {
	RunID  string   `json:"run_id"`
	Prompt string   `json:"prompt"`
	State  RunState `json:"state"`
	// CWD and StartedAt describe the run itself rather than its graph.
	// They were missing, which meant a snapshot read back from disk could
	// not say where the run happened or when — the two things that tell
	// runs apart in a list, since a run id is only a timestamp.
	CWD       string           `json:"cwd,omitempty"`
	StartedAt int64            `json:"started_at,omitempty"`
	Tasks     []TaskSnapshot   `json:"tasks"`
	Threads   []ThreadSnapshot `json:"threads"`
	Messages  []*Message       `json:"messages"`
}

type TaskSnapshot struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Assignment string    `json:"assignment"`
	Deps       []string  `json:"deps"`
	State      TaskState `json:"state"`
	Dispatches int       `json:"dispatches"`
	Model      string    `json:"model,omitempty"`
}

type ThreadSnapshot struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
}

// Snapshot produces a consistent view of the run for API responses.
func (r *Run) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		RunID:     r.ID,
		Prompt:    r.Prompt,
		State:     r.State,
		CWD:       r.CWD,
		StartedAt: r.CreatedAt.Unix(),
	}
	for _, t := range r.Tasks {
		t.mu.RLock()
		s.Tasks = append(s.Tasks, TaskSnapshot{
			ID:         t.ID,
			Name:       t.Name,
			Assignment: t.Assignment,
			Deps:       append([]string(nil), t.Deps...),
			State:      t.State,
			Dispatches: len(t.Dispatches),
		})
		t.mu.RUnlock()
	}
	for _, th := range r.Threads {
		th.mu.RLock()
		s.Threads = append(s.Threads, ThreadSnapshot{
			ID:           th.ID,
			Name:         th.Name,
			Participants: append([]string(nil), th.Participants...),
		})
		th.mu.RUnlock()
	}
	// Include the message log so snapshots are self-contained.
	r.msgMu.Lock()
	s.Messages = append([]*Message(nil), r.messages...)
	r.msgMu.Unlock()
	return s
}
