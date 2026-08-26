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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
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
type spawnWorkerSpec struct {
	Name       string   `json:"name"`
	Assignment string   `json:"assignment"`
	Deps       []string `json:"deps"`
	Model      string   `json:"model"`
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

	run := s.broker.GetRun(req.RunID)
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

		note := "queued; the dispatcher will launch it shortly"
		if task.CurrentState() != broker.TaskReady {
			note = "waiting on deps; will launch when they complete"
		}
		launched = append(launched, map[string]any{
			"task_id": taskID,
			"state":   string(task.CurrentState()),
			"note":    note,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":      req.RunID,
		"workers":     launched,
		"session_dir": l.SessionDir(),
	})
}

// DispatchTask registers an agent for a ready task, sets up its workspace,
// and launches the worker. Shared by the spawn endpoint and the daemon's
// dispatch loop so both paths create workers identically.
func DispatchTask(reg *broker.AgentRegistry, l *launcher.Launcher, run *broker.Run, task *broker.Task) error {
	agentID := "worker-" + task.ID
	agent := reg.Register(agentID, run.ID, false)
	cfg := launcher.WorkerConfig{
		AgentID:    agentID,
		TaskID:     task.ID,
		Assignment: task.Assignment,
	}
	agentDir, err := l.SetupAgent(agent, cfg)
	if err != nil {
		return fmt.Errorf("setup agent: %w", err)
	}
	task.StartDispatch(fmt.Sprintf("%s-d%d", task.ID, task.DispatchCount()+1), agentID)
	if err := l.Launch(agentDir, cfg); err != nil {
		task.FailDispatch(err.Error(), run)
		return fmt.Errorf("launch worker: %w", err)
	}
	return nil
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
	run := s.broker.GetRun(req.RunID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}
	if req.ThreadID == "" {
		req.ThreadID = "worklog"
	}
	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "content is required"})
		return
	}
	prio := broker.PriorityNormal
	switch broker.Priority(req.Priority) {
	case broker.PriorityFYI, broker.PriorityNormal, broker.PriorityUrgent:
		prio = broker.Priority(req.Priority)
	}
	msg := run.PostMessage(req.ThreadID, "main", req.Mentions, prio, req.Content)
	writeJSON(w, http.StatusOK, msg)
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
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "prompt is required"})
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
	planTimeout := time.Duration(req.PlanTimeoutSecs) * time.Second
	if planTimeout == 0 {
		planTimeout = 5 * time.Minute
	}

	planArgs := []string{"claude", "--print",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", planInstructions,
	}
	if req.PlannerModel != "" {
		planArgs = append(planArgs, "--model", req.PlannerModel)
	}
	planArgs = append(planArgs, "Decompose the user's request into a task DAG and create the tasks via the broker API. When done, post PLAN_DONE to the planning thread.")
	planCmd := exec.Command("ccproxy", planArgs...)
	planCmd.Dir = req.CWD
	planLog, _ := os.Create(filepath.Join(l.SessionDir(), "planner-output.txt"))
	planCmd.Stdout = planLog
	planCmd.Stderr = planLog
	planCmd.Env = append(os.Environ(),
		"NARWHAL_RUN_ID="+runID,
		"NARWHAL_BROKER_URL="+s.baseURL(),
		"NARWHAL_AGENT_TOKEN="+mainAgent.Token,
	)
	if err := planCmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "start planner: " + err.Error()})
		return
	}

	// Wait for the planner in a goroutine so the HTTP response can return
	// the run id immediately. The caller polls status to learn when the
	// DAG is ready and workers start dispatching.
	go func() {
		_ = planCmd.Wait()
		planLog.Close()
	}()

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
	writeJSON(w, http.StatusCreated, resp)
}
