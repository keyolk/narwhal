package broker

import (
	"strings"
	"testing"
	"time"
)

func TestAttentionNamesWhatARunNeeds(t *testing.T) {
	now := time.Now()
	r := New().CreateRun("r1", "local 환경에 설치 및 설정", t.TempDir(), "main")
	r.SetOrigin(Origin{SessionID: "s-1", TmuxPane: "%9"})
	a := r.AddTask("research", "research", "x", nil)
	cp := r.AddTask("checkpoint", "checkpoint", "This is the checkpoint task.", []string{"research"})
	a.StartDispatch("d", "w")
	cp.StartDispatch("d", "w")
	q := r.Ask("research", "Which format?", "json", false)
	r.Resolve("checkpoint", Resolution{ID: q.ID, Raise: true, Options: []string{"json", "yaml"}, Recommended: 1})
	r.Ask("research", "Still in review?", "x", false) // a candidate: not the person's yet

	items := Attention(r.Snapshot(), now)
	if len(items) != 1 || items[0].Kind != AttentionQuestion || items[0].Question.ID != q.ID {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	if it.Origin == nil || it.Origin.SessionID != "s-1" || !strings.Contains(it.Title, "checkpoint") ||
		len(it.Question.Options) != 2 {
		t.Errorf("question item = %+v", it)
	}

	// A finished run says so, with its answer, for a day.
	b := New().CreateRun("r2", "p", t.TempDir(), "main")
	syn := b.AddTask("synthesis", "synthesis", "combine", nil)
	syn.StartDispatch("d", "w")
	syn.CompleteDispatch("# Guide\n\ndayx는 로컬 서비스다. 더.", b)
	b.SetState(RunDone)
	got := Attention(b.Snapshot(), now)
	if len(got) != 1 || got[0].Kind != AttentionDone || got[0].Summary != "dayx는 로컬 서비스다." {
		t.Fatalf("done = %+v", got)
	}
	if late := Attention(b.Snapshot(), now.Add(25*time.Hour)); len(late) != 0 {
		t.Errorf("a day-old run is still an item: %+v", late)
	}
}
