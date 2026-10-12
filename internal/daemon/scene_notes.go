// scene_notes.go writes one-line summaries of a run's closed scenes.
//
// broker.Scenes tells a run as scenes by rule, and a rule can say that a
// file conflict happened and who posted, but not what was agreed — that is
// in the workers' own words. Once a scene has closed, this asks a small
// model, once, for a sentence that says it, and records it on the run
// (Run.SetSceneNote). The monitor shows the note where there is one.
//
// It runs beside the dispatcher, never in its tick: a call costs ~25s of
// Claude Code start-up. So a run's pending scenes go in one call, a run
// has at most one call in flight, and a scene that fails is not retried
// until the daemon restarts — the rule's telling is still there.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// noteTimeout bounds one summarising call.
const noteTimeout = 2 * time.Minute

// noteBatch caps the scenes in one call, so a long run's backlog is
// worked through in several rather than one prompt the model truncates.
const noteBatch = 12

// summarise turns scenes into notes by key. A variable so tests can stand
// in for the model.
var summarise = haikuNotes

// sceneNoter tracks which runs have a call in flight and which scenes
// have been tried.
type sceneNoter struct {
	mu       sync.Mutex
	inFlight map[string]bool
	tried    map[string]bool
}

// noteScenes asks for notes on the run's closed scenes that have none, in
// the background.
func (d *Dispatcher) noteScenes(runID string, run *broker.Run) {
	if !scenesNotesOn() {
		return
	}
	n := &d.notes
	n.mu.Lock()
	if n.inFlight == nil {
		n.inFlight, n.tried = map[string]bool{}, map[string]bool{}
	}
	if n.inFlight[runID] {
		n.mu.Unlock()
		return
	}
	var due []broker.Scene
	for _, sc := range broker.Scenes(run.Snapshot(), time.Now()) {
		if !sc.Closed || sc.Note != "" || sc.Key == "" || n.tried[runID+"|"+sc.Key] || !worthNoting(sc) {
			continue
		}
		due = append(due, sc)
		if len(due) == noteBatch {
			break
		}
	}
	if len(due) == 0 {
		n.mu.Unlock()
		return
	}
	n.inFlight[runID] = true
	for _, sc := range due {
		n.tried[runID+"|"+sc.Key] = true
	}
	n.mu.Unlock()

	go func() {
		defer func() {
			n.mu.Lock()
			delete(n.inFlight, runID)
			n.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), noteTimeout)
		defer cancel()
		notes, err := summarise(ctx, due)
		if err != nil {
			log.Printf("[scenes] %s: %v", runID, err)
			return
		}
		for _, sc := range due {
			rd := notes[sc.Key]
			if note := strings.TrimSpace(rd.Note); note != "" {
				run.SetSceneNote(sc.Key, note)
			}
			if sc.Kind == broker.SceneTask && len(sc.Who) == 1 && (len(rd.Decisions) > 0 || len(rd.Unverified) > 0) {
				run.SetTaskReasons(sc.Who[0], broker.TaskReasons{Decisions: rd.Decisions, Unverified: rd.Unverified})
			}
		}
	}()
}

// worthNoting reports whether a scene has something in its workers' or the
// operator's words that a rule could not say.
func worthNoting(sc broker.Scene) bool {
	switch sc.Kind {
	case broker.SceneExchange, broker.SceneOperator:
		return true
	case broker.SceneTask:
		// A finished task's outcome runs to kilobytes; its first line is
		// as often a path as a conclusion.
		return sc.Mark == "✓" || sc.Mark == "✗"
	case broker.SceneRun:
		return len([]rune(sc.Title)) > 60
	}
	return false
}

// notePrompt asks for one Korean sentence per scene, as JSON by key.
func notePrompt(scenes []broker.Scene) string {
	var b strings.Builder
	b.WriteString("아래는 멀티 에이전트 작업(run)의 장면들이다. 장면마다, 그 장면에서 무슨 일이 있었고 어떻게 " +
		"정리됐는지 한국어 한 문장(60자 안팎)으로 써라. 사람이 run의 흐름을 빠르게 따라가기 위한 요약이다.\n" +
		"- 파일명·task id·숫자 같은 고유한 사실은 남긴다.\n" +
		"- 원문에 없는 결과나 이후 일어난 일은 쓰지 않는다. 요청만 있었으면 요청했다고 쓴다.\n" +
		"- 인사·군더더기·추측은 쓰지 않는다.\n" +
		"task 장면(\"결과:\"가 있는 장면)은 결과 원문에서 두 가지를 더 뽑는다:\n" +
		"  decisions: 결과를 좌우한 판단·선택 (무엇을 왜 그렇게 정했나). 원문에 있는 것만, 최대 3개, 각 한 문장.\n" +
		"  unverified: 원문이 스스로 확인하지 않았다·실측 안 했다·미루었다·가정했다고 밝힌 것. 원문에 있는 것만, 최대 3개.\n" +
		"출력은 JSON 객체 하나뿐이다. 키는 장면 id. 값은 task 장면이면 " +
		"{\"note\": 문장, \"decisions\": [...], \"unverified\": [...]}, 그 밖의 장면이면 문장 하나. 다른 텍스트는 쓰지 않는다.\n")
	for _, sc := range scenes {
		fmt.Fprintf(&b, "\n### 장면 id: %s\n제목: %s\n", sc.Key, sc.Title)
		for _, e := range sc.Events {
			who := e.Who
			if who == "" {
				who = "run"
			}
			fmt.Fprintf(&b, "- %s: %s\n", who, cut(e.Text, 600))
		}
		if sc.Kind == broker.SceneTask && sc.Outcome != "" {
			// The outcome alone: the other lines are the rules' account
			// of the task's course, shown under the note as they are.
			fmt.Fprintf(&b, "- 결과: %s\n", cut(sc.Outcome, 1500))
		}
	}
	return b.String()
}

// haikuNotes asks a small model, through ccproxy, for the scenes' notes.
// Claude Code is started with no tools, settings or MCP servers: it only
// writes text, and each of those adds to a start-up that dominates the
// call.
func haikuNotes(ctx context.Context, scenes []broker.Scene) (map[string]sceneReading, error) {
	cmd := exec.CommandContext(ctx, "ccproxy", "claude", "--print", "--model", "haiku",
		"--restricted", "--strict-mcp-config", "--tools", "", "--no-session-persistence", "--no-chrome")
	cmd.Stdin = strings.NewReader(notePrompt(scenes))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ccproxy claude: %w: %s", err, cut(errb.String(), 200))
	}
	return parseNotes(out.String())
}

// sceneReading is what the model said about one scene: its note, and for a
// task, the decisions and unverified points read out of its outcome.
type sceneReading struct {
	Note       string   `json:"note"`
	Decisions  []string `json:"decisions"`
	Unverified []string `json:"unverified"`
}

// UnmarshalJSON takes a scene's value as a sentence or an object.
func (r *sceneReading) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		r.Note = s
		return nil
	}
	type plain sceneReading
	return json.Unmarshal(b, (*plain)(r))
}

// parseNotes reads the model's JSON, tolerating a fence or prose around it.
func parseNotes(s string) (map[string]sceneReading, error) {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("no JSON in the model's answer: %q", cut(s, 200))
	}
	var notes map[string]sceneReading
	if err := json.Unmarshal([]byte(s[i:j+1]), &notes); err != nil {
		return nil, fmt.Errorf("the model's answer: %w", err)
	}
	return notes, nil
}

// scenesNotesOn reports whether notes are written: on unless
// NARWHAL_SCENE_NOTES=off, the switch the judge has too.
func scenesNotesOn() bool { return os.Getenv("NARWHAL_SCENE_NOTES") != "off" }
