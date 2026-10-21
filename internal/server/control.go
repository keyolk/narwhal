// control.go exposes the operations an interactive client needs but the
// batch CLI does in-process: spawning workers, draining findings, checking
// progress, and cancelling work.
//
// The MCP server runs as a separate process from the daemon (Claude Code
// spawns it over stdio), so it cannot reach the daemon's launcher through
// memory. Everything it needs has to cross an HTTP boundary, which is what
// this file provides.
//
// Routes live under /api/v1/control/ and require no agent token: the
// listener is localhost-only and the caller is the operator's own session,
// not a worker.
package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/store"
)

// Controller is what the server needs from the daemon to serve control
// routes. Keeping it an interface lets the batch CLI construct a server
// without a launcher at all.
type Controller interface {
	// NewRunID mints a unique run identifier.
	NewRunID() string
	// LauncherFor returns (creating if needed) the launcher for a run.
	LauncherFor(runID, cwd string) *launcher.Launcher
	// Launcher returns the existing launcher for a run, or nil.
	Launcher(runID string) *launcher.Launcher
	// ActiveRuns lists runs that still have a launcher — the ones with
	// work left to drive.
	ActiveRuns() []string
	// KnownRuns lists every run this session has driven, finished ones
	// included. Status listings use this rather than ActiveRuns: a run
	// that just settled would otherwise vanish at the exact moment the
	// user goes looking for its result.
	KnownRuns() []string
}

// SetController attaches a controller, enabling the /control routes.
func (s *Server) SetController(c Controller) { s.control = c }

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request, parts []string) {
	if s.control == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "this broker has no controller; control routes are daemon-only",
		})
		return
	}
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	switch parts[0] {
	case "spawn":
		s.handleSpawn(w, r)
	case "plan":
		s.handlePlan(w, r)
	case "drain":
		s.handleControlDrain(w, r)
	case "status":
		s.handleControlStatus(w, r)
	case "send":
		s.handleControlSend(w, r)
	case "cancel":
		s.handleControlCancel(w, r)
	case "questions":
		s.handleControlQuestions(w, r)
	case "attention":
		s.handleControlAttention(w, r)
	case "runs":
		s.handleControlRuns(w, r)
	case "answer":
		s.handleControlAnswer(w, r)
	default:
		http.NotFound(w, r)
	}
}

// checkDir reports whether a path is usable as a run's working directory.
func checkDir(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cwd %s: %w", path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("cwd %s is not a directory", path)
	}
	return nil
}

// spawnWorkerSpec describes one worker the caller wants launched.
//
// The field set matches the task-create API body in server.go on purpose.
// Check was missing here while that path had it, so a run built through
// narwhal_spawn could not carry an end condition at all — and of the last
// six runs on disk, all six were spawned and all six recorded check=none.
type spawnWorkerSpec struct {
	Name       string   `json:"name"`
	Assignment string   `json:"assignment"`
	Deps       []string `json:"deps"`
	Model      string   `json:"model"`
	Check      string   `json:"check"`
	Criterion  string   `json:"criterion"`
}

// handleSpawn creates a run (or adds to an existing one) and launches a
// worker per spec. Tasks with deps stay pending until their deps complete;
// the caller polls status or drains the radio to learn when that happens.
func (s *Server) handleSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		RunID   string            `json:"run_id"` // empty → create a new run
		CWD     string            `json:"cwd"`
		Prompt  string            `json:"prompt"`
		Workers []spawnWorkerSpec `json:"workers"`
		Origin  *broker.Origin    `json:"origin"`
		// Permissions is the policy every worker of a new run runs under.
		// See broker/permissions.go.
		Permissions *broker.Permissions `json:"permissions"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(req.Workers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at least one worker is required"})
		return
	}
	if req.CWD == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cwd is required"})
		return
	}
	// Reject a bad cwd here rather than letting every worker fail on it.
	// The launcher checks too, but by then the caller has a run whose
	// tasks all failed and a log full of exec errors naming the wrong
	// file — answering now says what is actually wrong.
	if err := checkDir(req.CWD); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if req.Permissions != nil {
		if err := req.Permissions.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	run := s.broker.GetRun(req.RunID)
	// A run's policy is fixed when it is created. Letting a later spawn
	// change it would let one caller widen what another caller's workers
	// may do in the middle of their work.
	if run != nil && req.Permissions != nil && !run.Permissions().Equal(*req.Permissions) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "permissions are fixed when a run is created; this run has a different policy"})
		return
	}
	if run == nil {
		if req.RunID != "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found: " + req.RunID})
			return
		}
		req.RunID = s.control.NewRunID()
		run = s.broker.CreateRun(req.RunID, req.Prompt, req.CWD, "main")
		// Register the operator's own session so it can send and drain.
		s.registry.Register("main", req.RunID, true)
		run.CreateStandardThreads()
		if req.Origin != nil {
			run.SetOrigin(*req.Origin)
		}
		if req.Permissions != nil {
			run.SetPermissions(*req.Permissions)
		}
	}

	l := s.control.LauncherFor(req.RunID, req.CWD)

	// Tasks are created here but launched by the daemon's dispatch loop.
	// Routing every launch through one place means a task cannot be
	// dispatched twice — once by this handler and again by the loop that
	// sees it sitting ready.
	launched := make([]map[string]any, 0, len(req.Workers))
	for _, spec := range req.Workers {
		name := spec.Name
		if name == "" {
			name = fmt.Sprintf("worker-%d", len(run.SnapshotTasks())+1)
		}
		taskID := name
		if run.GetTask(taskID) != nil {
			taskID = fmt.Sprintf("%s-%d", name, len(run.SnapshotTasks())+1)
		}

		task := run.AddTask(taskID, name, spec.Assignment, spec.Deps)
		task.SetModel(spec.Model)
		task.SetCheck(spec.Check)
		task.SetCriterion(spec.Criterion)

		note := "queued; the dispatcher will launch it shortly"
		if task.CurrentState() != broker.TaskReady {
			// A synthesis task is pending on its deps and launched anyway
			// — DispatchableTasks makes that exception so it can listen
			// while its peers work. Reporting it as waiting would describe
			// the state field rather than what happens next.
			if task.IsSynthesis() {
				note = "pending on deps but launched immediately; task-done blocks until they finish"
			} else {
				note = "waiting on deps; will launch when they complete"
			}
		}
		launched = append(launched, map[string]any{
			"task_id": taskID,
			"state":   string(task.CurrentState()),
			"note":    note,
		})
	}

	resp := map[string]any{
		"run_id":      req.RunID,
		"workers":     launched,
		"session_dir": l.SessionDir(),
	}
	if gap := graphGap(run); gap != "" {
		resp["graph_gap"] = gap
	}
	writeJSON(w, http.StatusOK, resp)
}

// graphGap reports what a multi-worker run is missing, or "" when it is
// well-formed.
//
// It exists because the spawn path had no way to say this and the plan path
// did. A planner is told at length to create a synthesis task and to give
// tasks end conditions; a caller of narwhal_spawn was told neither, and the
// runs show it — every run built by a planner carries deps and a synthesis
// task, and the six most recent, all spawned, carry neither, nor a single
// check between them.
//
// Reported rather than refused, for the same reason the duplicate note is:
// a flat fan-out is sometimes exactly right, and a hard error on a judgement
// call teaches callers to route around the check.
func graphGap(run *broker.Run) string {
	tasks := run.SnapshotTasks()
	if len(tasks) < 2 {
		return ""
	}
	hasSynthesis := false
	checked := 0
	for _, t := range tasks {
		if isSynthesisTask(run, t.ID) {
			hasSynthesis = true
		}
		if strings.TrimSpace(t.Check) != "" {
			checked++
		}
	}
	var missing []string
	if !hasSynthesis {
		missing = append(missing, "no synthesis task: "+broker.SynthesisContract)
	}
	if checked == 0 {
		missing = append(missing, "no task carries an end condition: "+broker.CheckContract)
	}
	if len(missing) == 0 {
		return ""
	}
	return "This run has " + strconv.Itoa(len(tasks)) + " tasks. " +
		strings.Join(missing, " ") +
		" Add what is missing with another narwhal_spawn call on this run_id."
}

// isSynthesisTask asks the run rather than re-deriving the naming rule, so
// this cannot disagree with the dispatcher about which task is synthesis.
func isSynthesisTask(run *broker.Run, taskID string) bool {
	t := run.GetTask(taskID)
	return t != nil && t.IsSynthesis()
}

// DispatchTask registers an agent for a ready task, sets up its workspace,
// and launches the worker. Shared by the spawn endpoint and the daemon's
// dispatch loop so both paths create workers identically.
func DispatchTask(reg *broker.AgentRegistry, l *launcher.Launcher, run *broker.Run, task *broker.Task) error {
	agentID := "worker-" + task.ID
	agent := reg.Register(agentID, run.ID, false)
	cfg := launcher.WorkerConfigFor(task)
	cfg.AgentID = agentID
	cfg.Permissions = run.Permissions()
	agentDir, err := l.SetupAgent(agent, cfg)
	if err != nil {
		return fmt.Errorf("setup agent: %w", err)
	}
	if fu := task.CurrentFollowUp(); fu != "" {
		followUpConfig(&cfg, l, task, fu)
	}
	task.StartDispatch(fmt.Sprintf("%s-d%d", task.ID, task.DispatchCount()+1), agentID)
	if err := l.Launch(agentDir, cfg); err != nil {
		task.FailDispatch(err.Error(), run)
		return fmt.Errorf("launch worker: %w", err)
	}
	return nil
}

// followUpConfig points a reopened task's dispatch at the worker that did
// it: its Claude session resumed, with the operator's instruction as the
// next turn. See broker/followup.go.
//
// When that session cannot be resumed — no id was recorded, or its
// transcript is gone — a fresh worker is started instead, told what the
// task concluded and what is now asked. It lacks the first worker's
// context but not the instruction, which is the part that must not be
// lost.
func followUpConfig(cfg *launcher.WorkerConfig, l *launcher.Launcher, task *broker.Task, instruction string) {
	sid := l.RecordedSessionID(cfg.AgentID)
	if sid != "" {
		if _, err := os.Stat(launcher.TranscriptPath(l.CWD(), sid)); err == nil {
			cfg.ResumeSession = sid
			cfg.Prompt = followUpPrompt(task.ID, instruction)
			return
		}
	}
	var b strings.Builder
	b.WriteString(cfg.Assignment)
	if prev := strings.TrimSpace(task.PreviousOutcome()); prev != "" {
		fmt.Fprintf(&b, "\n\n## This task was done once already\n\nIts outcome then was:\n\n%s\n", prev)
	}
	fmt.Fprintf(&b, "\n\n## Follow-up from the operator\n\n%s\n\n"+
		"Build on what was done rather than redoing it. If this is more work than one task should do, "+
		"split new tasks off and dep-add yourself on the ones your outcome needs. Then call task-done "+
		"with the full updated outcome.\n", instruction)
	cfg.Assignment = b.String()
}

// followUpPrompt is the resumed worker's next turn.
//
// It says the task was reopened rather than relying on the worker to infer
// it, because the session's last memory is calling task-done: without
// this, the natural reading is a stray message after the work was over.
// The outcome it asks for is the whole updated answer — task-done records
// one outcome, and a delta would replace the answer with a fragment.
func followUpPrompt(taskID, instruction string) string {
	return fmt.Sprintf("Your task %s was finished, and the operator has reopened it with a follow-up "+
		"instruction:\n\n%s\n\n"+
		"Act on it in the same working directory, with what you already know. Your helper scripts "+
		"(send, drain, watch, artifact, task-done, …) have been reissued at the same paths; start a "+
		"radio watcher again if the instruction involves peers.\n\n"+
		"If the instruction is more work than this task should do alone, extend the run's graph "+
		"rather than doing it all here: split new tasks off (each gets its own worker and runs in "+
		"parallel) and, if your outcome needs theirs, dep-add yourself on them so your task-done "+
		"waits for them. Tasks that build on yours are reopened behind you and will revise their "+
		"outcomes from your new one, and work split off after the run's answer was written is "+
		"folded into that answer automatically.\n\n"+
		"When you are done, call task-done with your full updated outcome — the whole answer as it "+
		"now stands, not only what changed, because task-done replaces the recorded outcome.", taskID, instruction)
}

// handleControlDrain returns radio messages after a cursor, from the
// operator's point of view (all messages, not just mentions of "main").
func (s *Server) handleControlDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		RunID string `json:"run_id"`
		After int64  `json:"after"`
		// WaitMs turns the read into a long poll. Without it the only way
		// to follow a run was to call again and again: the transcripts
		// show 92 drains across 5 runs, most of them returning what the
		// caller had already seen.
		WaitMs int `json:"wait_ms"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	run := s.broker.GetRun(req.RunID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}

	msgs := run.MessagesSince(req.After)
	waited := false
	if len(msgs) == 0 && req.WaitMs > 0 {
		// Cap the wait: an MCP client is holding a tool call open, and a
		// caller that asked for an hour has made a mistake it cannot see.
		wait := time.Duration(req.WaitMs) * time.Millisecond
		if wait > maxDrainWait {
			wait = maxDrainWait
		}
		ch, remove := run.RegisterWatch("main")
		select {
		case <-ch:
			msgs = run.MessagesSince(req.After)
		case <-time.After(wait):
			waited = true
		case <-r.Context().Done():
			// The client gave up; do not hold the run's watch list.
			remove()
			return
		}
		remove()
	}

	out := map[string]any{
		"run_id": req.RunID,
		// Never backwards: the tool tells its caller to pass this back,
		// and lastSeq is 0 for an empty read — so a quiet moment reset
		// the caller to the start and it re-read the whole channel.
		"cursor":   advanceCursor(req.After, msgs),
		"messages": msgs,
	}
	if waited {
		out["timeout"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// maxDrainWait bounds a long-poll drain. Long enough that following a run
// costs one call a minute rather than a call a second; short enough that a
// held-open tool call still returns on its own.
const maxDrainWait = 60 * time.Second

// handleControlStatus reports run and worker state for one run, or a
// summary of every run when run_id is omitted.
func (s *Server) handleControlStatus(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		runs := make([]map[string]any, 0)
		for _, id := range s.control.KnownRuns() {
			run := s.broker.GetRun(id)
			if run == nil {
				continue
			}
			snap := run.Snapshot()
			active := 0
			if l := s.control.Launcher(id); l != nil {
				active = len(l.ActiveWorkers())
			}
			// Per-state counts, not just a total: the picker shows how a
			// run turned out, and "4 tasks" says nothing about whether it
			// worked.
			done, failed := 0, 0
			for _, t := range snap.Tasks {
				switch t.State {
				case broker.TaskCompleted:
					done++
				case broker.TaskFailed:
					failed++
				}
			}
			runs = append(runs, map[string]any{
				"run_id":         id,
				"prompt":         snap.Prompt,
				"cwd":            run.CWD,
				"started_at":     run.CreatedAt.Unix(),
				"state":          string(snap.State),
				"tasks":          len(snap.Tasks),
				"done":           done,
				"failed":         failed,
				"messages":       len(snap.Messages),
				"active_workers": active,
				"flow":           snap.Flow,
				"parent_run":     snap.ParentRun,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
		return
	}

	run := s.broker.GetRun(runID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}
	var active []string
	if l := s.control.Launcher(runID); l != nil {
		active = l.ActiveWorkers()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot":       run.Snapshot(),
		"active_workers": active,
	})
}

// handleControlSend posts a message as the operator's "main" agent, so a
// user can steer a worker mid-flight.
func (s *Server) handleControlSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		RunID    string   `json:"run_id"`
		ThreadID string   `json:"thread_id"`
		Content  string   `json:"content"`
		Mentions []string `json:"mentions"`
		Priority string   `json:"priority"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.ThreadID == "" {
		req.ThreadID = "worklog"
	}
	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "content is required"})
		return
	}
	run := s.broker.GetRun(req.RunID)
	if run == nil {
		// A run this daemon has not hosted since it started — finished
		// before a restart. Its record is on disk, and an instruction to
		// one of its tasks reopens it; anything else has nowhere to go.
		snap, err := store.LoadRun(req.RunID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
			return
		}
		restored := broker.RestoreRun(snap)
		if len(restored.AddressedTasks(req.Mentions, req.Content)) == 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "this run has finished; address a task with @task to reopen it with the instruction",
			})
			return
		}
		s.broker.AdoptRun(restored)
		run = restored
	}
	prio := broker.PriorityNormal
	switch broker.Priority(req.Priority) {
	case broker.PriorityFYI, broker.PriorityNormal, broker.PriorityUrgent:
		prio = broker.Priority(req.Priority)
	}
	// Mentions are recorded as worker ids, whichever form the caller used,
	// so the watcher of a task named in the text wakes too.
	addressed := run.AddressedTasks(req.Mentions, req.Content)
	mentions := append([]string(nil), req.Mentions...)
	for _, t := range addressed {
		if !slices.Contains(mentions, "worker-"+t.ID) {
			mentions = append(mentions, "worker-"+t.ID)
		}
	}
	msg := run.PostMessage(req.ThreadID, "main", mentions, prio, req.Content)
	reopened := s.reopenFinished(run, addressed, req.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"seq": msg.Seq, "run_id": run.ID, "mentions": msg.Mentions, "reopened": reopened,
	})
}

// reopenFinished reopens the addressed tasks that had finished, with the
// instruction, and puts their run back in front of the dispatcher, which
// resumes each one's worker. A task still running reads the instruction on
// the radio and is left alone. See broker/followup.go.
func (s *Server) reopenFinished(run *broker.Run, tasks []*broker.Task, instruction string) []string {
	var reopened, addressed []string
	seen := map[string]bool{}
	for _, t := range tasks {
		ids := run.ReopenWithDependents(t, instruction)
		if len(ids) > 0 {
			addressed = append(addressed, ids[0])
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				reopened = append(reopened, id)
			}
		}
	}
	if len(reopened) == 0 {
		return []string{}
	}
	switch run.CurrentState() {
	case broker.RunDone, broker.RunFailed, broker.RunCanceled:
		run.SetState(broker.RunActive)
	}
	if s.control != nil {
		// A launcher is what puts a run in front of the dispatcher; a
		// settled run's was retired.
		s.control.LauncherFor(run.ID, run.Snapshot().CWD)
	}
	note := fmt.Sprintf("%s reopened %s with the operator's instruction; resuming %s.", broker.RelayPrefix,
		strings.Join(addressed, ", "), map[bool]string{true: "its worker", false: "their workers"}[len(addressed) == 1])
	var behind []string
	for _, id := range reopened {
		if !slices.Contains(addressed, id) {
			behind = append(behind, id)
		}
	}
	if len(behind) > 0 {
		note += fmt.Sprintf(" %s, which build on it, reopen behind it and will revise their outcomes when it is done.",
			strings.Join(behind, ", "))
	}
	run.PostMessage(broker.WorklogThread, "coordinator", []string{"main"}, broker.PriorityNormal, note)
	return reopened
}

// handleControlCancel stops a run's workers. The tasks are left in whatever
// state they reached; cancelling is an operator action, not a failure of
// the work itself.
func (s *Server) handleControlCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	l := s.control.Launcher(req.RunID)
	if l == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no active launcher for run"})
		return
	}
	killed := l.KillAll()
	if run := s.broker.GetRun(req.RunID); run != nil {
		run.SetState(broker.RunCanceled)
		// Retire whatever had not finished. Killing the workers stops the
		// processes, but a task left in ready or pending is a task the
		// dispatcher would pick up again the moment anything changed, and
		// one left dispatched describes a worker that no longer exists.
		for _, snap := range run.SnapshotTasks() {
			switch snap.State {
			case broker.TaskCompleted, broker.TaskFailed:
			default:
				if task := run.GetTask(snap.ID); task != nil {
					task.CancelDispatch("run canceled", run)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": req.RunID,
		"killed": killed,
	})
}

// handlePlan runs a planner agent that decomposes the request into a task DAG,
// then returns. The daemon's dispatch loop launches the workers the planner
// created — the caller (MCP) observes progress via status/drain.
//
// This is the /control path for what `narwhal plan` does in-process. It
// exists so an interactive Claude session can ask for a DAG without leaving
// the conversation: the MCP tool calls this endpoint, the daemon runs the
// planner, and the caller polls status.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		CWD             string `json:"cwd"`
		Prompt          string `json:"prompt"`
		PlannerModel    string `json:"planner_model"`
		WorkerModel     string `json:"worker_model"`
		SynthesisModel  string `json:"synthesis_model"`
		PlanTimeoutSecs int    `json:"plan_timeout_secs"`
		// Flow continues a piece of work: the new run is its next step,
		// planned with its goal, notes and what the earlier steps
		// concluded. Empty starts a new flow. See store/flow.go.
		Flow string `json:"flow"`
		// Origin is the Claude Code session asking, sent by the MCP server.
		Origin *broker.Origin `json:"origin"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "prompt is required"})
		return
	}
	var flow store.Flow
	if req.Flow != "" {
		f, err := store.LoadFlow(req.Flow)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		if f.Closed {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "flow " + f.ID + " is closed"})
			return
		}
		flow = f
		// A step runs where its flow does, whatever the caller sent.
		req.CWD = f.CWD
	}
	if req.CWD == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cwd is required"})
		return
	}
	// Reject a bad cwd here rather than letting every worker fail on it.
	// The launcher checks too, but by then the caller has a run whose
	// tasks all failed and a log full of exec errors naming the wrong
	// file — answering now says what is actually wrong.
	if err := checkDir(req.CWD); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// A run id is a timestamp, so nothing here could notice it was being
	// asked to do what it is already doing. It happened: a restart
	// orphaned a run and the same request arrived four minutes later, and
	// the two were indistinguishable in the picker.
	//
	// Reported, not refused. Re-running a request is usually deliberate,
	// and a duplicate that is already finished is not news — only one
	// still in flight, or left in flight by a restart, is worth saying.
	duplicateOf := s.broker.DuplicateOf(req.CWD, req.Prompt)

	runID := s.control.NewRunID()
	run := s.broker.CreateRun(runID, req.Prompt, req.CWD, "main")
	if req.Origin != nil {
		run.SetOrigin(*req.Origin)
	}
	mainAgent := s.registry.Register("main", runID, true)
	run.CreateStandardThreads()

	l := s.control.LauncherFor(runID, req.CWD)
	l.SetWorkerModel(req.WorkerModel)

	// The synthesis task integrates peer findings — it needs frontier
	// intelligence even when the investigation workers do not.
	synModel := req.SynthesisModel
	if synModel == "" {
		synModel = req.WorkerModel
	}

	// Build the planner instructions and launch the planner agent, mirroring
	// what narwhal plan does in-process. The planner talks to the broker
	// HTTP API to create tasks with deps.
	// Show the planner how similar requests were decomposed on this
	// repository before. Every run's graph has been persisted since the
	// beginning and nothing read it back at plan time, so each plan
	// started from the prompt alone.
	planInstructions := PlanInstructionsFor(
		runID, s.baseURL(), mainAgent.Token, req.Prompt, req.CWD)

	// Place the run in its flow: the next step of one, or the first of a
	// new one. A flow that cannot be written is reported, not fatal — the
	// run is still worth doing on its own.
	flowErr := ""
	if flow.ID != "" {
		steps := flow.Output().Runs
		parent := flow.LastRun()
		run.SetLineage(parent, flow.ID)
		// The earlier steps in full, where a worker can read them; the
		// planner is given the conclusions and this path.
		full := filepath.Join(l.SessionDir(), "flow-so-far.md")
		if err := os.MkdirAll(l.SessionDir(), 0o700); err != nil {
			full = ""
		} else if err := os.WriteFile(full, []byte(store.FlowMarkdown(flow, steps)), 0o600); err != nil {
			full = ""
		}
		planInstructions += "\n\n" + store.FlowBrief(flow, steps, full)
		if _, err := store.AddFlowStep(flow.ID, runID, req.Prompt); err != nil {
			flowErr = err.Error()
		}
	} else {
		run.SetLineage("", runID)
		if _, err := store.StartFlow(runID, req.CWD, req.Prompt); err != nil {
			flowErr = err.Error()
		}
	}
	run.SetPlanner(req.PlannerModel, planInstructions, mainAgent.Token)
	if err := s.startPlanner(run, l, plannerPrompt, ""); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "start planner: " + err.Error()})
		return
	}

	resp := map[string]any{
		"run_id":          runID,
		"broker_url":      s.baseURL(),
		"planner_model":   req.PlannerModel,
		"worker_model":    req.WorkerModel,
		"synthesis_model": synModel,
	}
	if duplicateOf != "" {
		resp["duplicate_of"] = duplicateOf
	}
	resp["flow"] = run.Snapshot().Flow
	if flowErr != "" {
		resp["flow_error"] = flowErr
	}
	writeJSON(w, http.StatusCreated, resp)
}

const plannerPrompt = "Decompose the user's request into a task DAG and create the tasks via the broker API. " +
	"When done, post PLAN_DONE to the planning thread."

// startPlanner launches the run's planner, or with resume, continues its
// Claude session with prompt as the next turn.
//
// The session id is chosen here and kept on disk so a planner that asked
// the operator something and exited can be resumed with the answers: a
// --print agent cannot wait out a person (its tool calls time out at ten
// minutes), so it asks, ends its turn, and is woken when they answer.
func (s *Server) startPlanner(run *broker.Run, l *launcher.Launcher, prompt, resume string) error {
	model, instructions, token := run.Planner()
	args := []string{"claude", "--print", "--permission-mode", "bypassPermissions",
		"--append-system-prompt", instructions}
	if model != "" {
		args = append(args, "--model", model)
	}
	idFile := filepath.Join(l.SessionDir(), "planner-session-id")
	if resume != "" {
		args = append(args, "--resume", resume)
	} else if sid, err := launcher.NewSessionUUID(); err == nil {
		args = append(args, "--session-id", sid)
		if err := os.MkdirAll(l.SessionDir(), 0o700); err == nil {
			if err := os.WriteFile(idFile, []byte(sid+"\n"), 0o600); err != nil {
				log.Printf("[plan] record planner session for %s: %v", run.ID, err)
			}
		}
	}
	args = append(args, prompt)
	cmd := exec.Command("ccproxy", args...)
	cmd.Dir = run.Snapshot().CWD
	if err := os.MkdirAll(l.SessionDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(l.SessionDir(), "planner-output.txt"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(),
		"NARWHAL_RUN_ID="+run.ID,
		"NARWHAL_BROKER_URL="+s.baseURL(),
		"NARWHAL_AGENT_TOKEN="+token,
	)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	run.SetPlannerRunning(true)
	// Waited on in a goroutine so the caller returns at once; the caller
	// polls status to learn when the DAG is ready.
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[plan] planner for %s exited: %v", run.ID, err)
		}
		run.SetPlannerRunning(false)
		logf.Close()
	}()
	return nil
}

// resumePlanner wakes a planner that asked the operator and exited, once
// every question it asked is answered, with the answers as its next turn.
func (s *Server) resumePlanner(run *broker.Run) error {
	if s.control == nil || run.PlannerRunning() {
		return nil
	}
	var answers []string
	for _, q := range run.Questions() {
		if q.TaskID != broker.PlanAsker {
			continue
		}
		if q.Open() {
			return nil
		}
		answers = append(answers, fmt.Sprintf("#%d %s\n   → %s", q.ID, q.Text, q.Answer))
	}
	if len(answers) == 0 {
		return nil
	}
	l := s.control.LauncherFor(run.ID, run.Snapshot().CWD)
	b, err := os.ReadFile(filepath.Join(l.SessionDir(), "planner-session-id"))
	sid := strings.TrimSpace(string(b))
	prompt := "The operator answered your questions:\n\n" + strings.Join(answers, "\n") +
		"\n\nPlan with these answers now: create the tasks and post PLAN_DONE."
	if err != nil || sid == "" {
		// No session to continue: start over, told the answers.
		return s.startPlanner(run, l, plannerPrompt+"\n\n"+prompt, "")
	}
	return s.startPlanner(run, l, prompt, sid)
}

// handleControlAttention is what runs need from their person, for a
// session's cockpit (broker/attention.go). session=<id> keeps the runs
// that session started; without it, every run. Runs on disk written in the
// last day fill in what a restart dropped from memory.
func (s *Server) handleControlAttention(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	items := []broker.AttentionItem{}
	for _, snap := range s.sessionSnapshots(r.URL.Query().Get("session"), now.Add(-24*time.Hour)) {
		items = append(items, broker.Attention(snap, now)...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := attentionRank(items[i].Kind), attentionRank(items[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return items[i].At.After(items[j].At)
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleControlRuns lists the runs a session started, newest first, with
// their tasks and what they produced (broker/session_runs.go): the
// cockpit's Work and Outputs tabs. session=<id> as for attention; runs on
// disk from the last week fill in what a restart dropped.
func (s *Server) handleControlRuns(w http.ResponseWriter, r *http.Request) {
	runs := []broker.RunSummary{}
	for _, snap := range s.sessionSnapshots(r.URL.Query().Get("session"), time.Now().Add(-7*24*time.Hour)) {
		runs = append(runs, broker.Summarize(snap))
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// sessionSnapshots are the snapshots of the runs a session started — every
// run when session is empty: the daemon's, then those on disk written
// since the given time that it no longer holds.
func (s *Server) sessionSnapshots(session string, since time.Time) []broker.Snapshot {
	session = strings.TrimSpace(session)
	seen := map[string]bool{}
	var snaps []broker.Snapshot
	keep := func(snap broker.Snapshot) {
		if session == "" || snap.Origin != nil && snap.Origin.SessionID == session {
			snaps = append(snaps, snap)
		}
	}
	for _, id := range s.control.KnownRuns() {
		if run := s.broker.GetRun(id); run != nil {
			seen[id] = true
			keep(run.Snapshot())
		}
	}
	for _, id := range store.RunsModifiedSince(since) {
		if seen[id] {
			continue
		}
		if snap, err := store.LoadRun(id); err == nil {
			keep(snap)
		}
	}
	return snaps
}

// attentionRank orders items by what waits on the person first.
func attentionRank(k broker.AttentionKind) int {
	switch k {
	case broker.AttentionQuestion:
		return 0
	case broker.AttentionStuck:
		return 1
	case broker.AttentionFailed:
		return 2
	}
	return 3
}

// handleControlQuestions lists a run's questions, open ones first.
func (s *Server) handleControlQuestions(w http.ResponseWriter, r *http.Request) {
	run := s.broker.GetRun(r.URL.Query().Get("run_id"))
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": run.Questions(), "open": len(run.OpenQuestions())})
}

// handleControlAnswer records the operator's answers to several questions
// at once and carries each to its task: told, unblocked, or reopened. See
// broker/questions.go.
func (s *Server) handleControlAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		RunID   string `json:"run_id"`
		Answers []struct {
			ID        int    `json:"id"`
			Answer    string `json:"answer"`
			AsAssumed bool   `json:"as_assumed"`
		} `json:"answers"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	run := s.broker.GetRun(req.RunID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}
	var errs []string
	var reopened []string
	answered := 0
	for _, a := range req.Answers {
		q, err := run.Answer(a.ID, a.Answer, a.AsAssumed)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		answered++
		reopened = append(reopened, run.ApplyAnswer(q)...)
	}
	if err := s.resumePlanner(run); err != nil {
		errs = append(errs, "resume planner: "+err.Error())
	}
	if len(reopened) > 0 {
		switch run.CurrentState() {
		case broker.RunDone, broker.RunFailed, broker.RunCanceled:
			run.SetState(broker.RunActive)
		}
		if s.control != nil {
			s.control.LauncherFor(run.ID, run.Snapshot().CWD)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"answered": answered, "reopened": reopened, "errors": errs,
		"open": len(run.OpenQuestions())})
}
