package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Both drain and watch tell the caller to pass the returned cursor to the
// next call, and both computed it as "the newest message I am returning" —
// which is 0 when there is nothing new. So a quiet moment reset the caller
// to the start of the channel, and the next call re-read everything.
//
// The transcripts show what that cost: 92 narwhal_drain calls across 5
// runs, a caller re-reading a channel it had already seen and having no way
// to tell it had stopped making progress.

// cursorFixture returns a served run with two messages posted, plus the
// agent token to call as.
func cursorFixture(t *testing.T) (string, string, *broker.Run) {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("r1", "p", t.TempDir(), "main")
	run.CreateStandardThreads()
	a := reg.Register("worker-1", "r1", false)

	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr, a.Token, run
}

type cursorReply struct {
	Cursor   int64             `json:"cursor"`
	Messages []*broker.Message `json:"messages"`
	Timeout  bool              `json:"timeout"`
}

func callCursor(t *testing.T, addr, token, route string, body map[string]any) cursorReply {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/agents/"+token+"/"+route,
		"application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out cursorReply
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDrainCursorNeverGoesBackwards(t *testing.T) {
	addr, token, run := cursorFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "one")
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "two")

	first := callCursor(t, addr, token, "drain", map[string]any{"after": 0})
	if len(first.Messages) != 2 || first.Cursor != 2 {
		t.Fatalf("first drain: cursor=%d messages=%d", first.Cursor, len(first.Messages))
	}

	// Nothing new. Following the documented contract must not rewind.
	quiet := callCursor(t, addr, token, "drain", map[string]any{"after": first.Cursor})
	if len(quiet.Messages) != 0 {
		t.Fatalf("a quiet drain returned %d messages", len(quiet.Messages))
	}
	if quiet.Cursor != first.Cursor {
		t.Fatalf("a quiet drain moved the cursor from %d to %d — the next call "+
			"re-reads the whole channel", first.Cursor, quiet.Cursor)
	}

	// And the third call, the one that used to loop, stays empty.
	again := callCursor(t, addr, token, "drain", map[string]any{"after": quiet.Cursor})
	if len(again.Messages) != 0 {
		t.Errorf("following the cursor re-read %d messages", len(again.Messages))
	}
}

func TestDrainStillSeesNewMessages(t *testing.T) {
	// The cursor holding must not become a cursor that never advances.
	addr, token, run := cursorFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "one")

	first := callCursor(t, addr, token, "drain", map[string]any{"after": 0})
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "two")

	next := callCursor(t, addr, token, "drain", map[string]any{"after": first.Cursor})
	if len(next.Messages) != 1 {
		t.Fatalf("a new message was not delivered: %d messages", len(next.Messages))
	}
	if next.Cursor <= first.Cursor {
		t.Errorf("the cursor did not advance past %d: %d", first.Cursor, next.Cursor)
	}
}

func TestWatchKeepsItsCursorWhenAWakeIsNotForIt(t *testing.T) {
	// A wake does not mean a message for this agent: the channel wakes
	// every watcher and MessagesMentioning then filters. The unfiltered
	// case is what a timeout already handled correctly; this is the one
	// that reported cursor 0 and sent the watcher back to the start.
	addr, token, run := cursorFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-1", []string{"worker-1"},
		broker.PriorityNormal, "for you")

	first := callCursor(t, addr, token, "watch",
		map[string]any{"after": 0, "timeout_ms": 500})
	if len(first.Messages) == 0 {
		t.Fatal("setup: the mentioned message was not delivered")
	}

	// Now a message for somebody else, then a short watch. Whether it
	// wakes or times out, the cursor must not rewind.
	run.PostMessage(broker.WorklogThread, "worker-2", []string{"worker-2"},
		broker.PriorityNormal, "for a peer")
	after := callCursor(t, addr, token, "watch",
		map[string]any{"after": first.Cursor, "timeout_ms": 300})

	if after.Cursor < first.Cursor {
		t.Errorf("watch rewound the cursor from %d to %d", first.Cursor, after.Cursor)
	}
}

func TestTheDrainScriptCanFollowItsOwnCursor(t *testing.T) {
	// The end-to-end version: the script a worker is actually given,
	// reading twice the way the loop in the transcripts does.
	run, scripts := protocolFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-task-1", nil,
		broker.PriorityNormal, "a finding")

	read := func(after string) cursorReply {
		out, err := exec.Command("bash", filepath.Join(scripts, "drain"), after).Output()
		if err != nil {
			t.Fatalf("drain failed: %v", err)
		}
		var reply cursorReply
		if err := json.Unmarshal(out, &reply); err != nil {
			t.Fatalf("drain output is not JSON: %v\n%s", err, out)
		}
		return reply
	}

	first := read("0")
	if len(first.Messages) == 0 {
		t.Fatal("the script read nothing")
	}
	second := read(itoa64(first.Cursor))
	if len(second.Messages) != 0 {
		t.Errorf("the script re-read %d messages by following its own cursor",
			len(second.Messages))
	}
	if second.Cursor != first.Cursor {
		t.Errorf("the script's cursor moved from %d to %d with nothing new",
			first.Cursor, second.Cursor)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
