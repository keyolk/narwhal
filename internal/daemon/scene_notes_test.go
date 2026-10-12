package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestParseNotesToleratesFencesAndProse(t *testing.T) {
	got, err := parseNotes("here you go:\n```json\n{\"a\": \"task-3이 소유\", \"b\": {\"note\": \"끝\", \"unverified\": [\"실측 안 함\"]}}\n```")
	if err != nil || got["a"].Note != "task-3이 소유" || got["b"].Note != "끝" || got["b"].Unverified[0] != "실측 안 함" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseNotes("no json"); err == nil {
		t.Error("an answer with no JSON was accepted")
	}
}

// A closed scene gets one note, recorded on the run; the dispatcher asks
// once per scene, all of a run's pending scenes in one call.
func TestClosedScenesAreSummarisedOnce(t *testing.T) {
	t.Setenv("NARWHAL_SCENE_NOTES", "on")
	var calls atomic.Int32
	saved := summarise
	summarise = func(_ context.Context, scenes []broker.Scene) (map[string]sceneReading, error) {
		calls.Add(1)
		out := map[string]sceneReading{}
		for _, sc := range scenes {
			out[sc.Key] = sceneReading{Note: "요약: " + sc.Title, Unverified: []string{"실측 안 함"}}
		}
		return out, nil
	}
	t.Cleanup(func() { summarise = saved })

	sess := NewSession()
	run := sess.Broker.CreateRun("r-notes", "p", t.TempDir(), "main")
	run.CreateStandardThreads()
	a := run.AddTask("a", "a", "x", nil)
	a.StartDispatch("d1", "worker-a")
	a.CompleteDispatch("found it at x.go:1", run)

	d := NewDispatcher(sess)
	d.noteScenes("r-notes", run)
	waitFor(t, "the note to be recorded", func() bool {
		for _, sc := range broker.Scenes(run.Snapshot(), time.Now()) {
			if sc.Kind == broker.SceneTask && strings.HasPrefix(sc.Note, "요약: a") {
				return true
			}
		}
		return false
	})
	time.Sleep(20 * time.Millisecond)
	d.noteScenes("r-notes", run)
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("summarised %d times, want once", n)
	}
	// The note, and what was read out of the outcome, survive a restart.
	s := broker.RestoreRun(run.Snapshot()).Snapshot()
	if len(s.SceneNotes) == 0 || len(s.TaskReasons["a"].Unverified) == 0 {
		t.Errorf("notes %v, reasons %v did not survive a restart", s.SceneNotes, s.TaskReasons)
	}
}
