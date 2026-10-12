// stuck.go watches running workers for being stuck and asks the judge.
//
// A worker that loops — the same failing test, the same search with no
// match — spends its run and the operator finds out when it times out, if
// it does. The monitor shows the activity, but only to someone looking at
// that task. The dispatcher already ticks over every running task; once a
// worker has run long, it reads the worker's last actions from its
// transcript and asks the judge whether they are going anywhere. A
// confident no is posted to the operator (broker.JudgeStuck). It never
// changes the task: steering, waiting or cancelling is the operator's call.
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// StuckAfter is how long a dispatch runs before it is first judged, and
// how long after each judgement the next one waits. Short tasks never
// reach it; long ones are asked a handful of times.
var StuckAfter = 10 * time.Minute

// stuckRecent is how many of a worker's last actions are judged.
const stuckRecent = 20

// stuckWatch remembers when each running task was last judged and which
// judgements are in flight, so the tick neither repeats nor waits on one.
type stuckWatch struct {
	mu       sync.Mutex
	last     map[string]time.Time
	inFlight map[string]bool
}

// checkStuck judges the run's long-running workers, in the background.
func (d *Dispatcher) checkStuck(runID string, run *broker.Run) {
	l := d.sess.Launcher(runID)
	if l == nil {
		return
	}
	d.stuck.mu.Lock()
	if d.stuck.last == nil {
		d.stuck.last, d.stuck.inFlight = map[string]time.Time{}, map[string]bool{}
	}
	d.stuck.mu.Unlock()
	now := time.Now()
	for _, s := range run.SnapshotTasks() {
		if s.State != broker.TaskDispatched || s.StartedAt.IsZero() || now.Sub(s.StartedAt) < StuckAfter {
			continue
		}
		key := runKey(runID, s.ID)
		d.stuck.mu.Lock()
		due := !d.stuck.inFlight[key] && now.Sub(d.stuck.last[key]) >= StuckAfter
		if due {
			d.stuck.inFlight[key], d.stuck.last[key] = true, now
		}
		d.stuck.mu.Unlock()
		if !due {
			continue
		}
		task := run.GetTask(s.ID)
		sid := l.RecordedSessionID("worker-" + s.ID)
		go func() {
			defer func() {
				d.stuck.mu.Lock()
				delete(d.stuck.inFlight, key)
				d.stuck.mu.Unlock()
			}()
			if task == nil || sid == "" {
				return
			}
			recent, err := recentActions(launcher.TranscriptPath(l.CWD(), sid), stuckRecent)
			if err != nil || len(recent) == 0 {
				return
			}
			run.JudgeStuck(task, recent)
		}()
	}
}

// recentActions reads a Claude transcript's last n tool calls, each with
// the start of what came back, oldest first.
func recentActions(path string, n int) ([]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var acts []string
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		var parts []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     map[string]any  `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(e.Message.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			switch p.Type {
			case "tool_use":
				at[p.ID] = len(acts)
				acts = append(acts, fmt.Sprintf("%s: %s", p.Name, cut(actionArg(p.Input), 140)))
			case "tool_result":
				if i, ok := at[p.ToolUseID]; ok {
					acts[i] += "  →  " + cut(oneLine(resultText(p.Content)), 100)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(acts) > n {
		acts = acts[len(acts)-n:]
	}
	return acts, nil
}

// actionArg is the part of a tool call that says what it did.
func actionArg(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "pattern", "path", "url"} {
		if v, ok := in[k].(string); ok && v != "" {
			return v
		}
	}
	b, _ := json.Marshal(in)
	return string(b)
}

// resultText is a tool result's text, whether a string or blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, x := range blocks {
			b.WriteString(x.Text)
		}
		return b.String()
	}
	return string(raw)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func cut(s string, n int) string {
	r := []rune(oneLine(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
