// Package server exposes the Narwhal broker over a localhost HTTP API.
//
// The server is the "tusk" — the endpoint each agent's wrapper scripts hit.
// Agent identity is derived from the URL path token, not from a field in
// the request body, so the model never needs to know its own token.
//
// Endpoints (all under /api/v1):
//
//	GET  /run/<run-id>                         run snapshot
//	POST /run/<run-id>/task                    create a task
//	POST /run/<run-id>/thread                  create a thread
//	POST /agents/<token>/send                  post a message (sender = token owner)
//	POST /agents/<token>/watch                 long-poll for messages mentioning this agent
//	POST /agents/<token>/drain                 non-blocking message check
//	GET  /agents/<token>/state                 full run state for this agent
//	POST /agents/<token>/task/<task-id>/done   mark a task completed
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Server is the localhost HTTP face of the broker.
type Server struct {
	broker   *broker.Broker
	registry *broker.AgentRegistry
	listener net.Listener
	server   *http.Server

	// watchMu guards per-agent watch state so only one long-poll per agent
	// is active at a time (AgentRadio's "exactly one watcher" invariant).
	watchMu   sync.Mutex
	watchers  map[string]*watchSession
}

type watchSession struct {
	mu       sync.Mutex
	notified chan struct{}
	cursor   int64
}

// New returns a Server bound to an OS-assigned ephemeral port on localhost.
func New(b *broker.Broker, reg *broker.AgentRegistry) *Server {
	s := &Server{
		broker:   b,
		registry: reg,
		watchers: make(map[string]*watchSession),
	}
	return s
}

// Start binds the listener and begins serving. Returns the actual address.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	s.server = &http.Server{Handler: http.HandlerFunc(s.handle)}
	go func() { _ = s.server.Serve(ln) }()
	return fmt.Sprintf("http://%s", ln.Addr().String()), nil
}

// Shutdown stops the server.
func (s *Server) Shutdown() {
	if s.server != nil {
		_ = s.server.Shutdown(nil)
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	// /api/v1/...
	if len(parts) >= 2 && parts[0] == "api" && parts[1] == "v1" {
		s.handleV1(w, r, parts[2:])
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleV1(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}

	switch parts[0] {
	case "run":
		s.handleRun(w, r, parts[1:])
	case "agents":
		s.handleAgent(w, r, parts[1:])
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	runID := parts[0]
	run := s.broker.GetRun(runID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		snap := run.Snapshot()
		// Include agents in the snapshot.
		writeJSON(w, http.StatusOK, snap)
		return
	}

	if len(parts) == 2 && parts[1] == "task" && r.Method == http.MethodPost {
		var req struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Assignment string   `json:"assignment"`
			Deps       []string `json:"deps"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		t := run.AddTask(req.ID, req.Name, req.Assignment, req.Deps)
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":    t.ID,
			"state": string(t.State),
		})
		return
	}

	if len(parts) == 2 && parts[1] == "thread" && r.Method == http.MethodPost {
		var req struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Participants []string `json:"participants"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		th := run.CreateThread(req.ID, req.Name, req.Participants)
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":   th.ID,
			"name": th.Name,
		})
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	agent := s.registry.LookupByToken(token)
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown agent token"})
		return
	}

	if len(parts) == 1 {
		// Agent info
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agent.ID,
			"run_id":   agent.RunID,
		})
		return
	}

	run := s.broker.GetRun(agent.RunID)
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}

	action := parts[1]
	switch action {
	case "send":
		s.handleSend(w, r, agent, run)
	case "watch":
		s.handleWatch(w, r, agent, run)
	case "drain":
		s.handleDrain(w, r, agent, run)
	case "state":
		s.handleState(w, agent, run)
	case "task":
		s.handleTaskAction(w, r, agent, run, parts[2:])
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, agent *broker.Agent, run *broker.Run) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		ThreadID string   `json:"thread_id"`
		Mentions []string `json:"mentions"`
		Priority string   `json:"priority"`
		Content  string   `json:"content"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.ThreadID == "" || req.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "thread_id and content required"})
		return
	}
	prio := broker.PriorityNormal
	switch broker.Priority(req.Priority) {
	case broker.PriorityFYI, broker.PriorityNormal, broker.PriorityUrgent:
		prio = broker.Priority(req.Priority)
	}
	msg := run.PostMessage(req.ThreadID, agent.ID, req.Mentions, prio, req.Content)
	writeJSON(w, http.StatusOK, msg)
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request, agent *broker.Agent, run *broker.Run) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		After     int64 `json:"after"`
		TimeoutMs int   `json:"timeout_ms"`
	}
	_ = decodeBody(r, &req)
	if req.TimeoutMs == 0 {
		req.TimeoutMs = 60000
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond

	// AgentRadio invariant: exactly one watcher per agent. If a previous
	// watch session is still active, close it before starting a new one.
	s.watchMu.Lock()
	if old, ok := s.watchers[agent.ID]; ok {
		close(old.notified)
	}
	sess := &watchSession{
		notified: make(chan struct{}, 1),
		cursor:   req.After,
	}
	s.watchers[agent.ID] = sess
	s.watchMu.Unlock()

	defer func() {
		s.watchMu.Lock()
		delete(s.watchers, agent.ID)
		s.watchMu.Unlock()
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// In phase 1 the broker is in-memory with no history store, so we
		// poll the run's snapshot. A later phase will add a proper message
		// log with O(1) cursor lookup; for now the simplicity is worth it.
		time.Sleep(200 * time.Millisecond)
		// TODO: when message log is persisted, check cursor directly.
		// For now, return immediately so the watcher wrapper can drain.
		// This is a placeholder that the watch wrapper will exercise.
		break
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agent.ID,
		"cursor":   sess.cursor,
		"status":   "check-drain",
	})
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request, agent *broker.Agent, run *broker.Run) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	var req struct {
		After int64 `json:"after"`
	}
	_ = decodeBody(r, &req)

	// Phase 1: return the run snapshot. The caller (wrapper script) will
	// extract messages with seq > after and mention this agent.
	snap := run.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agent.ID,
		"after":    req.After,
		"snapshot": snap,
	})
}

func (s *Server) handleState(w http.ResponseWriter, agent *broker.Agent, run *broker.Run) {
	snap := run.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agent.ID,
		"snapshot": snap,
	})
}

func (s *Server) handleTaskAction(w http.ResponseWriter, r *http.Request, agent *broker.Agent, run *broker.Run, parts []string) {
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	taskID := parts[0]
	action := parts[1]
	task := run.GetTask(taskID)
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}

	switch action {
	case "done":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		var req struct {
			Outcome string `json:"outcome"`
		}
		_ = decodeBody(r, &req)
		task.CompleteDispatch(req.Outcome, run)
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id": task.ID,
			"state":   string(task.State),
		})
	default:
		http.NotFound(w, r)
	}
}

func decodeBody(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
