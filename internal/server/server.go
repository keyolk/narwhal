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
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Server is the localhost HTTP face of the broker.
type Server struct {
	broker   *broker.Broker
	registry *broker.AgentRegistry
	listener net.Listener
	server   *http.Server
	addr     string // the base URL once Start binds

	// control is set only by the daemon. It backs the /control routes an
	// interactive client uses to spawn and steer workers; the batch CLI
	// leaves it nil because it drives the launcher in-process.
	control Controller
}

// New returns a Server bound to an OS-assigned ephemeral port on localhost.
func New(b *broker.Broker, reg *broker.AgentRegistry) *Server {
	return &Server{
		broker:   b,
		registry: reg,
	}
}

// Start binds the listener and begins serving. Returns the actual address.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	s.addr = fmt.Sprintf("http://%s", ln.Addr().String())
	s.server = &http.Server{Handler: http.HandlerFunc(s.handle)}
	go func() { _ = s.server.Serve(ln) }()
	return s.addr, nil
}

// baseURL returns the address the server is listening on. Empty before Start.
func (s *Server) baseURL() string { return s.addr }

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
	case "monitor":
		s.handleMonitor(w, r, parts[1:])
	case "control":
		s.handleControl(w, r, parts[1:])
	default:
		http.NotFound(w, r)
	}
}

// handleMonitor serves read-only run state for the live monitor. It needs
// no agent token: the monitor observes, it never sends. Binding is
// localhost-only, so this is not an external exposure.
func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET required"})
		return
	}
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	run := s.broker.GetRun(parts[0])
	if run == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
		return
	}
	snap := run.Snapshot()
	// Include which agents are registered so the monitor can show the roster.
	agents := make([]string, 0)
	for _, a := range s.registry.Agents() {
		if a.RunID == run.ID {
			agents = append(agents, a.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snap,
		"agents":   agents,
	})
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
			Model      string   `json:"model"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		t := run.AddTask(req.ID, req.Name, req.Assignment, req.Deps)
		t.SetModel(req.Model)
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

	// First check: are there already messages waiting?
	msgs := run.MessagesMentioning(req.After, agent.ID)
	if len(msgs) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agent.ID,
			"cursor":   lastSeq(msgs),
			"messages": msgs,
		})
		return
	}

	// No messages yet: register a long-poll wake channel and wait.
	ch, remove := run.RegisterWatch(agent.ID)
	defer remove()

	select {
	case <-ch:
		msgs := run.MessagesMentioning(req.After, agent.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agent.ID,
			"cursor":   lastSeq(msgs),
			"messages": msgs,
		})
	case <-time.After(timeout):
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agent.ID,
			"cursor":   req.After,
			"messages": []*broker.Message{},
			"timeout":  true,
		})
	case <-r.Context().Done():
		return
	}
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

	// Drain returns ALL messages after the cursor, not just mentioned ones.
	// A synthesis task needs every peer finding; mention filtering here is
	// what caused the "drain skipped seq N" symptom — a message whose
	// mentions slot held a priority ("urgent") was filtered out as addressing
	// a nonexistent agent. Mention-based filtering belongs on the watch
	// (notification) path, not on the manual read path.
	msgs := run.MessagesSince(req.After)
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agent.ID,
		"after":    req.After,
		"cursor":   lastSeq(msgs),
		"messages": msgs,
	})
}

// lastSeq returns the highest Seq among msgs, or 0 if empty.
func lastSeq(msgs []*broker.Message) int64 {
	var max int64
	for _, m := range msgs {
		if m.Seq > max {
			max = m.Seq
		}
	}
	return max
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

		// Gate completion, not dispatch. A task with deps is launched
		// immediately — synthesis drains the radio as peers post, which is
		// why it does not queue behind them — but declaring itself done
		// while a peer is still writing means the final answer was
		// assembled from a partial picture. Observed on real runs: the
		// synthesis worker stopped four messages before its peer posted
		// its final summary, and nothing noticed.
		//
		// The refusal names who is outstanding so the worker can go back
		// to waiting instead of guessing what went wrong.
		if pending := run.PendingDeps(taskID); len(pending) > 0 {
			run.PostMessage(broker.WorklogThread, "coordinator",
				[]string{task.ID}, broker.PriorityUrgent,
				fmt.Sprintf("NOT_DONE|%s|task-done refused: %s still running. "+
					"Keep the watcher up and drain until every one has finished, "+
					"then call task-done again.",
					task.ID, strings.Join(pending, ", ")))
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":        "dependencies still running",
				"task_id":      task.ID,
				"pending_deps": pending,
				"hint": "Your task depends on peers that have not finished. Keep " +
					"draining the radio and call task-done again once they have.",
			})
			return
		}

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
