package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// fixtureRun is a finished-but-for-synthesis run with everything the
// monitor can show: tasks that ran, one retried; radio posts, one urgent;
// an operator instruction routed to a worker and answered; judge calls;
// and a Claude transcript per worker with its prompt, prose, tool calls
// and results. Views are checked against it the way a person would walk
// them, instead of against hand-built fragments that each cover one field.
func fixtureRun(t *testing.T) tuiModel {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	at := func(sec int) time.Time {
		return time.Date(2026, 10, 12, 10, 0, sec, 0, time.Local)
	}
	snap := broker.Snapshot{
		RunID: "r-fixture", State: broker.RunActive, CWD: cwd,
		Prompt:    "replay-safe lock API: schema, endpoints, client state, adversarial fuzzing",
		StartedAt: at(0).Unix(),
		Tasks: []broker.TaskSnapshot{
			{ID: "schema", State: broker.TaskCompleted, Dispatches: 1, Model: "sonnet",
				Assignment: "Add a LockKey field to the lock payload in internal/schema/types.go.",
				StartedAt:  at(1), EndedAt: at(40), Outcome: "LockKey added; serialization tests pass"},
			{ID: "api", State: broker.TaskDispatched, Dispatches: 1, Model: "sonnet",
				Assignment: "Validate LockKey on every lock endpoint and reject a replayed nonce.",
				StartedAt:  at(1)},
			{ID: "client", State: broker.TaskCompleted, Dispatches: 2, Model: "sonnet",
				Assignment: "Store the server's lock response in web/src/state.ts without reshaping it twice.",
				StartedAt:  at(70), EndedAt: at(110), Outcome: "response mapping in one place; state tests pass"},
			{ID: "synthesis", State: broker.TaskPending, Model: "opus",
				Deps:       []string{"api", "client", "schema"},
				Assignment: "Integrate the peers' findings into one answer."},
		},
		Messages: []*broker.Message{
			{Seq: 1, Sender: "worker-api", Content: "@worker-schema LockKey is missing from the payload — can you add it?", CreatedAt: at(10)},
			{Seq: 2, Sender: "worker-schema", Content: "@worker-api added in types.go, commit 3f2a1c", CreatedAt: at(38)},
			{Seq: 3, Sender: "worker-api", Priority: broker.PriorityUrgent, Content: "the same nonce passes twice on replay — repro attached", CreatedAt: at(50)},
			{Seq: 4, Sender: "main", Content: "make sure nothing logs the lock token", CreatedAt: at(60)},
			{Seq: 5, Sender: "coordinator", Mentions: []string{"worker-api"},
				Content: broker.RelayPrefix + " from the operator, for api: make sure nothing logs the lock token", CreatedAt: at(61)},
			{Seq: 6, Sender: "worker-api", Content: "token is redacted in the request logger now", CreatedAt: at(90)},
		},
		Verdicts: []broker.Verdict{
			{At: at(69), TaskID: "client", Question: broker.QuestionNoResult, Decided: true, P: 0.86, Action: broker.ActionRetry},
			{At: at(61), TaskID: "api", Question: broker.QuestionRoute, Decided: true, P: 0.9, Action: broker.ActionRouted, Ask: "make sure nothing logs the lock token"},
			{At: at(95), TaskID: "api", Question: broker.QuestionWorker, Asker: "worker-api", Decided: true, P: 0.81,
				Ask: "does middleware validation cover the replay path too?"},
		},
	}

	m := testModel(0, 0)
	m.live.RunID, m.live.CWD = snap.RunID, cwd
	m.runs[0].RunID = snap.RunID
	m.snap = snap
	m.width, m.height = 150, 40

	transcripts := map[string][]string{
		"api": {
			userLine(at(1), snap.Tasks[1].Assignment),
			assistantText(at(5), "text", "Reading the router first: every lock endpoint goes through it, so validation belongs there if anywhere."),
			toolUse(at(6), "Read", `{"file_path":"/repo/internal/api/router.go"}`),
			toolResult(at(7), "package api\n// 180 lines"),
			toolUse(at(8), "Grep", `{"pattern":"LockKey"}`),
			toolResult(at(9), "no matches"),
			assistantText(at(12), "thinking", "The handlers never look at LockKey. A middleware covers every route at once, including replay."),
			toolUse(at(20), "Edit", `{"file_path":"/repo/internal/api/middleware.go"}`),
			toolResult(at(21), "ok"),
			toolUse(at(22), "Bash", `{"command":"go test ./internal/api/..."}`),
			toolResult(at(30), "ok  internal/api 0.4s"),
			userLine(at(62), "[relay] from the operator, for api: make sure nothing logs the lock token"),
			assistantText(at(70), "text", "The request logger prints headers verbatim, the token included. Redacting it there."),
			toolUse(at(72), "Edit", `{"file_path":"/repo/internal/api/logging.go"}`),
			toolResult(at(73), "ok"),
		},
		"schema": {
			userLine(at(1), snap.Tasks[0].Assignment),
			assistantText(at(4), "text", "LockKey goes on the payload type; the fixtures predate it, so decoding must tolerate its absence."),
			toolUse(at(5), "Edit", `{"file_path":"/repo/internal/schema/types.go"}`),
			toolResult(at(6), "ok"),
			toolUse(at(7), "Bash", `{"command":"go test ./internal/schema/..."}`),
			toolResult(at(12), "ok"),
		},
		"client": {
			userLine(at(70), snap.Tasks[2].Assignment),
			assistantText(at(75), "text", "state.ts maps the response in three places. Folding them into one mapper."),
			toolUse(at(80), "Edit", `{"file_path":"/repo/web/src/state.ts"}`),
			toolResult(at(81), "ok"),
		},
	}
	for id, lines := range transcripts {
		sid := "sid-" + id
		dir := filepath.Join(home, ".narwhal", "sessions", snap.RunID, "agents", "worker-"+id)
		must(t, os.MkdirAll(dir, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, "claude-session-id"), []byte(sid), 0o644))
		path := transcriptPath(cwd, sid)
		must(t, os.MkdirAll(filepath.Dir(path), 0o755))
		must(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
		t.Cleanup(func() { globalTranscripts.forget(path) })
	}
	m.radioCur = len(snap.Messages) - 1
	m.activity = m.gatherLive(snap, at(100))
	return m
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func jsonLine(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func userLine(at time.Time, text string) string {
	return jsonLine(map[string]any{"type": "user", "timestamp": stamp(at),
		"message": map[string]any{"content": text}})
}

func assistantText(at time.Time, kind, text string) string {
	return jsonLine(map[string]any{"type": "assistant", "timestamp": stamp(at),
		"message": map[string]any{"content": []map[string]any{{"type": kind, kind: text}}}})
}

func toolUse(at time.Time, name, input string) string {
	return jsonLine(map[string]any{"type": "assistant", "timestamp": stamp(at),
		"message": map[string]any{"content": []map[string]any{
			{"type": "tool_use", "name": name, "input": json.RawMessage(input)}}}})
}

func toolResult(at time.Time, text string) string {
	return jsonLine(map[string]any{"type": "user", "timestamp": stamp(at),
		"message": map[string]any{"content": []map[string]any{{"type": "tool_result", "content": text}}}})
}
