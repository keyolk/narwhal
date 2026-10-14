// origin.go records which Claude Code session started a run.
//
// A run started through the MCP tools belongs to a conversation: the person
// is in that session, with its cockpit beside it, not in the monitor. When
// the run needs them — a question, a stuck worker — or has finished, that
// session is where to say so. The MCP server knows its session from its
// environment (CLAUDE_CODE_SESSION_ID, TMUX_PANE) and sends it with the
// request; the run keeps it, on disk too.
package broker

// Origin is the Claude Code session a run was started from.
type Origin struct {
	SessionID string `json:"session_id,omitempty"`
	TmuxPane  string `json:"tmux_pane,omitempty"`
}

// SetOrigin records the session that started the run.
func (r *Run) SetOrigin(o Origin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.origin = o
}

// originPtr is the origin for a snapshot, nil when there is none. Called
// under the run lock.
func (r *Run) originPtr() *Origin {
	if r.origin == (Origin{}) {
		return nil
	}
	o := r.origin
	return &o
}
