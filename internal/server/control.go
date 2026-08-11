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
	// ActiveRuns lists runs that still have a launcher.
	ActiveRuns() []string
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

// spawnWorkerSpec describes one worker the caller wants launched.
type spawnWorkerSpec struct {
	Name       string   `json:"name"`
	Assignment string   `json:"assignment"`
	Deps       []string `json:"deps"`
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
		run.CreateThread("planning", "planning", []string{"main"})
		run.CreateThread("worklog", "worklog", []string{"main"})
		run.CreateThread("results", "results", []string{"main"})
	}

	l := s.control.LauncherFor(req.RunID, req.CWD)

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

		// A task with unmet deps is not launched now; the caller can spawn
		// it later, or a future coordinator tick will pick it up.
		if task.CurrentState() != broker.TaskReady {
			launched = append(launched, map[string]any{
				"task_id": taskID,
				"state":   string(task.CurrentState()),
				"note":    "waiting on deps; not launched yet",
			})
			continue
		}

		agentID := "worker-" + taskID
		agent := s.registry.Register(agentID, req.RunID, false)
		cfg := launcher.WorkerConfig{
			AgentID:    agentID,
			TaskID:     taskID,
			Assignment: spec.Assignment,
		}
		agentDir, err := l.SetupAgent(agent, cfg)
		if err != nil {
			launched = append(launched, map[string]any{
				"task_id": taskID, "state": "error", "error": err.Error(),
			})
			continue
		}
		task.StartDispatch(taskID+"-d1", agentID)
		if err := l.Launch(agentDir, cfg); err != nil {
			task.FailDispatch(err.Error(), run)
			launched = append(launched, map[string]any{
				"task_id": taskID, "state": "error", "error": err.Error(),
			})
			continue
		}
		launched = append(launched, map[string]any{
			"task_id":  taskID,
			"agent_id": agentID,
			"state":    "dispatched",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":      req.RunID,
		"workers":     launched,
		"session_dir": l.SessionDir(),
	})
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
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":   req.RunID,
		"cursor":   lastSeq(msgs),
		"messages": msgs,
	})
}

// handleControlStatus reports run and worker state for one run, or a
// summary of every run when run_id is omitted.
func (s *Server) handleControlStatus(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		runs := make([]map[string]any, 0)
		for _, id := range s.control.ActiveRuns() {
			run := s.broker.GetRun(id)
			if run == nil {
				continue
			}
			snap := run.Snapshot()
			active := 0
			if l := s.control.Launcher(id); l != nil {
				active = len(l.ActiveWorkers())
			}
			runs = append(runs, map[string]any{
				"run_id":         id,
				"state":          string(snap.State),
				"tasks":          len(snap.Tasks),
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
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": req.RunID,
		"killed": killed,
	})
}
