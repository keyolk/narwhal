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
			ID           string   `json:"id"`
			Name         string   `json:"name"`
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
			"cursor":   advanceCursor(req.After, msgs),
			"messages": msgs,
		})
		return
	}

	// No messages yet: register a long-poll wake channel and wait.
	ch, remove := run.RegisterWatch(agent.ID)
	defer remove()

	select {
	case <-ch:
		// A wake does not guarantee a message for THIS agent: the channel
		// wakes every watcher and MessagesMentioning then filters. An
		// empty result here would have reported cursor 0 and sent the
		// watcher back to the start of the channel.
		msgs := run.MessagesMentioning(req.After, agent.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agent.ID,
			"cursor":   advanceCursor(req.After, msgs),
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
	// The cursor has to hold when there is nothing new. lastSeq returns 0
	// for an empty read, and the tool tells its caller to pass the cursor
	// back — so a quiet moment reset the caller to the start of the
	// channel and it re-read everything on the next call. In the
	// transcripts that is 92 drains against 5 runs, each one re-reading a
	// channel it had already seen.
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agent.ID,
		"after":    req.After,
		"cursor":   advanceCursor(req.After, msgs),
		"messages": msgs,
	})
}

// advanceCursor returns the cursor to hand back: the newest message read,
// or the caller's own cursor when there was nothing new.
//
// Never going backwards is the whole contract. Both drain and watch tell
// the caller to pass the returned cursor to the next call, and lastSeq
// returns 0 for an empty read — so a quiet moment reset the caller to the
// start of the channel and it re-read everything. The transcripts show 92
// drains across 5 runs doing exactly that.
func advanceCursor(after int64, msgs []*broker.Message) int64 {
	if next := lastSeq(msgs); next > after {
		return next
	}
	return after
}

// peerMessages drops what the worker has no reason to fold in: the
// coordinator's own WAITING notice, which the gate itself posted moments
// earlier, and the worker's own messages.
//
// Without this the gate can 202 on nothing but its own announcement — it
// posts WAITING, that bumps the sequence inside the window it is about to
// inspect, and the worker is sent back to fold in a message telling it
// that it is waiting.
func peerMessages(msgs []*broker.Message, taskID string) []*broker.Message {
	out := make([]*broker.Message, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		if m.Sender == "coordinator" && strings.HasPrefix(m.Content, "WAITING|") {
			continue
		}
		if m.Sender == "worker-"+taskID {
			continue
		}
		out = append(out, m)
	}
	return out
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
			Outcome   string `json:"outcome"`
			TimeoutMs int    `json:"timeout_ms"`
			// Final says the worker has already folded in what arrived
			// during the wait. Without it a second call would be handed
			// the same messages again and the task would never complete.
			Final bool `json:"final"`
			// After is how far the worker has read the radio. The 202
			// hands back what it has NOT read; without its cursor the
			// server can only guess, and guessing is what was wrong.
			After int64 `json:"after"`
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
		// The call BLOCKS rather than refusing outright. A refusal was the
		// first design and it failed in a way worth recording: the worker
		// read it, said "I will keep the watcher up and wait", and then
		// its turn ended. `claude --print` exits when the model stops
		// producing output, so intending to wait is not waiting — the
		// process died, the coordinator recorded a dispatch with no
		// task-done, and the circuit breaker failed the task on the third
		// try. Holding the HTTP request open keeps the worker's turn alive,
		// which is the only thing that actually makes it wait.
		if pending := run.PendingDeps(taskID); len(pending) > 0 {
			// Everything the worker has already seen. Messages that land
			// during the wait are the ones it could not have folded in.
			// This used to be lastSeq(run.MessagesSince(0)) — the
			// server's global last seq at gate entry, which is not the
			// same thing at all. A peer message posted before task-done
			// but after the worker's last drain was already "seen" by
			// that measure, so the 202 excluded by construction the very
			// messages it exists to deliver. On run s1786665646933-1 the
			// finding was seq 2, the worker had drained to seq 1, and the
			// 202 handed back seq 4: the coordinator's own WAITING message
			// and nothing else. That worker recovered only because it
			// independently re-drained from its own cursor.
			//
			// The worker knows where it has read to, so it sends it. A
			// caller that omits it gets the old behaviour rather than the
			// whole channel — replaying everything would make the fold-in
			// loop non-terminating.
			seen := req.After
			if seen <= 0 {
				seen = lastSeq(run.MessagesSince(0))
			}

			run.PostMessage(broker.WorklogThread, "coordinator",
				[]string{task.ID}, broker.PriorityNormal,
				fmt.Sprintf("WAITING|%s|task-done is holding until %s finish.",
					task.ID, strings.Join(pending, ", ")))

			if remaining := s.awaitDeps(r, run, taskID, waitTimeout(req.TimeoutMs)); len(remaining) > 0 {
				// Timed out with peers still running. Answer rather than
				// hanging forever: a stuck peer must not take the
				// synthesis worker down with it, and the worker can call
				// again.
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":        "dependencies still running",
					"task_id":      task.ID,
					"pending_deps": remaining,
					"hint": "Peers are still working. Call task-done again — it " +
						"blocks until they finish.",
				})
				return
			}

			// The wait is over, but the outcome the worker submitted was
			// written before it. Waiting fixed the ordering and left the
			// content stale: on the run that exposed this, task-done held
			// 100 seconds, the peer posted its finding during the hold,
			// and the recorded outcome still read "nothing to synthesize".
			//
			// So do not complete yet. Hand back what arrived during the
			// wait and ask for one more call. The worker is alive and in
			// the middle of a tool call, which is the only moment it can
			// act on this.
			if arrived := peerMessages(run.MessagesSince(seen), task.ID); len(arrived) > 0 && !req.Final {
				writeJSON(w, http.StatusAccepted, map[string]any{
					"task_id":      task.ID,
					"state":        string(task.CurrentState()),
					"new_messages": arrived,
					"waited_for":   pending,
					"hint": "Your peers finished while this call was waiting, and these " +
						"messages arrived after you wrote your outcome. Fold them in and " +
						"call task-done again with the updated answer — the next call " +
						"completes the task.",
				})
				return
			}
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

// waitTimeout bounds how long task-done holds the request open. The
// default is generous because the thing being waited on is another agent
// investigating a codebase, which takes minutes, not seconds.
func waitTimeout(ms int) time.Duration {
	if ms <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(ms) * time.Millisecond
}

// awaitDeps blocks until every dependency of taskID has finished, the
// timeout elapses, or the client goes away. Returns whatever is still
// outstanding — empty means the wait succeeded.
//
// Polling rather than a wake channel: a task reaching a terminal state is
// not a radio event, and the two existing signals (message posted, watch
// registered) do not fire for it. A second granularity is far finer than
// the minutes these waits actually last.
func (s *Server) awaitDeps(r *http.Request, run *broker.Run, taskID string, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	for {
		pending := run.PendingDeps(taskID)
		if len(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return pending
		}
		select {
		case <-r.Context().Done():
			// The worker hung up; nothing to answer.
			return pending
		case <-time.After(time.Second):
		}
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
