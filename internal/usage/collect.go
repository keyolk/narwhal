package usage

import (
	"os"
	"path/filepath"
	"strings"
)

// ForTask tallies one task's worker by resolving its session id and then
// its transcript. Returns an empty tally when either is missing, which is
// the ordinary state for a task that has not been dispatched.
//
// Two lookups rather than one, because the two artifacts live in
// different trees and go stale independently: narwhal writes the session
// id under ~/.narwhal, and Claude Code writes the transcript under
// ~/.claude. Measured over the sessions on this machine, 109 of 111
// recorded session ids resolve to a transcript; the two that do not are
// runs whose transcripts have since been removed, and they must read as
// unmeasured rather than as free.
func ForTask(runID, taskID string) (Tally, error) {
	sid := SessionID(runID, taskID)
	if sid == "" {
		return Tally{}, nil
	}
	path := TranscriptPath(sid)
	if path == "" {
		return Tally{}, nil
	}
	return ReadTranscript(path)
}

// SessionID reads the Claude session UUID the launcher pinned for a
// task's worker, or "" when there is none.
func SessionID(runID, taskID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".narwhal", "sessions", runID,
		"agents", "worker-"+taskID, "claude-session-id")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TranscriptPath locates a session's transcript without knowing the
// directory it ran in.
//
// Claude Code files transcripts per working directory, so the path is
// only computable from a cwd the run may not have recorded. A session id
// is a UUID, so the filename is unique across every project directory and
// a glob cannot pick the wrong one.
func TranscriptPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(
		filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}
