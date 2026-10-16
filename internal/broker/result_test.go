package broker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractArtifactsFromProse(t *testing.T) {
	text := "브랜치 `ABC-11892-kiro-acp-parity` (워크트리), 커밋 5개: `29b539a` alias, `83ac223` reaper, size `1234567` lines.\n" +
		"PR: https://github.com/acme/proxy/pull/227 and https://github.com/acme/proxy/pull/227 again."
	a := ExtractArtifacts(text)
	if strings.Join(a.PRs, ",") != "https://github.com/acme/proxy/pull/227" {
		t.Errorf("prs %v", a.PRs)
	}
	if strings.Join(a.Branches, ",") != "ABC-11892-kiro-acp-parity" {
		t.Errorf("branches %v", a.Branches)
	}
	// 1234567 has no letter: a number, not a hash.
	if strings.Join(a.Commits, ",") != "29b539a,83ac223" {
		t.Errorf("commits %v", a.Commits)
	}
	if len(a.Files) != 0 {
		t.Errorf("files guessed: %v", a.Files)
	}
}

// The answer is the synthesis task's; without one, every finished task's
// outcome under its id. Recorded artifacts and extracted ones combine, and
// the extracted are counted.
func TestARunsResult(t *testing.T) {
	r := New().CreateRun("r1", "p", "/tmp", "main")
	a := r.AddTask("a", "a", "do a", nil)
	a.StartDispatch("d", "w")
	if err := a.RecordArtifact(ArtifactBranch, "feat-a"); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordArtifact("tarball", "x"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	a.CompleteDispatch("done; commit `abc1234`", r)

	res := r.Snapshot().Result()
	if res.AnswerFrom != "" || !strings.Contains(res.Answer, "## a\n\ndone; commit") {
		t.Errorf("composed answer: %q from %q", res.Answer, res.AnswerFrom)
	}
	if strings.Join(res.Artifacts.Branches, ",") != "feat-a" || strings.Join(res.Artifacts.Commits, ",") != "abc1234" || res.Extracted != 1 {
		t.Errorf("artifacts %+v extracted %d", res.Artifacts, res.Extracted)
	}

	s := r.AddTask("synthesis", "synthesis", "integrate", []string{"a"})
	s.StartDispatch("d2", "w2")
	s.CompleteDispatch("# the answer", r)
	if res := r.Snapshot().Result(); res.Answer != "# the answer" || res.AnswerFrom != "synthesis" {
		t.Errorf("synthesis answer: %q from %q", res.Answer, res.AnswerFrom)
	}
}

// Artifacts and lineage are durable: they survive JSON and a restore.
func TestArtifactsAndLineageSurviveARestore(t *testing.T) {
	r := New().CreateRun("r2", "p", "/tmp", "main")
	r.SetLineage("r1", "r1")
	a := r.AddTask("a", "a", "do a", nil)
	_ = a.RecordArtifact(ArtifactPR, "https://github.com/o/r/pull/1")
	raw, _ := json.Marshal(r.Snapshot())
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	back := RestoreRun(s).Snapshot()
	if back.Flow != "r1" || back.ParentRun != "r1" || back.FlowID() != "r1" {
		t.Errorf("lineage %q %q", back.Flow, back.ParentRun)
	}
	if back.Tasks[0].Artifacts == nil || back.Tasks[0].Artifacts.PRs[0] != "https://github.com/o/r/pull/1" {
		t.Errorf("artifacts lost: %+v", back.Tasks[0].Artifacts)
	}
	// A run that records nothing writes nothing.
	plain := New().CreateRun("r3", "p", "/tmp", "main")
	plain.AddTask("b", "b", "x", nil)
	raw, _ = json.Marshal(plain.Snapshot())
	if strings.Contains(string(raw), "artifacts") || strings.Contains(string(raw), `"flow"`) {
		t.Errorf("empty fields serialized: %s", raw)
	}
}
