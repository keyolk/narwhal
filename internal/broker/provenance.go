// provenance.go says how a run's result came to be what it is, as the
// points an operator could correct.
//
// The timeline (scenes.go) tells the run in the order things happened. That
// answers "what went on", not "why is the answer this, and where would I
// push back". For that the result comes first, then each task that fed it
// with what it rests on: what it concluded, the calls it made, what was
// checked, what it asked and was or was not told, and what it left
// unverified. Each of those is a line the operator can pick and correct —
// a steer to that task, reopening it and what builds on it.
//
// Most of it is in the record already: checks and criteria and their
// results, handbacks, questions and answers, file conflicts. What a task
// decided and what it did not verify is in its outcome's prose; the daemon
// asks a model for those once the task finishes, alongside its scene note
// (daemon/scene_notes.go), and records them on the run (TaskReasons).
package broker

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ReasonKind says what a line of a task's provenance is.
type ReasonKind string

const (
	ReasonConclusion ReasonKind = "결론"
	ReasonDecision   ReasonKind = "판단"
	ReasonChecked    ReasonKind = "검증"
	ReasonAsked      ReasonKind = "질문"
	ReasonUnverified ReasonKind = "미검증"
)

// Reason is one line of what a task's outcome rests on.
type Reason struct {
	Kind ReasonKind
	Text string
	// Flag marks a line worth the operator's attention: an assumption
	// nobody confirmed, something left unverified, a check not met.
	Flag bool
}

// TaskReasons is what the daemon read out of a finished task's outcome.
type TaskReasons struct {
	Decisions  []string `json:"decisions,omitempty"`
	Unverified []string `json:"unverified,omitempty"`
}

// Provenance is one task's part in the result.
type Provenance struct {
	Task    TaskSnapshot
	Reasons []Reason
}

// Provenances returns, for each task that has finished or is working, what
// its outcome rests on: the answer task first, then the others in id order.
func Provenances(s Snapshot) []Provenance {
	var out []Provenance
	for _, t := range s.Tasks {
		if t.State == TaskPending || t.State == TaskReady && t.Dispatches == 0 {
			continue
		}
		out = append(out, Provenance{Task: t, Reasons: reasonsFor(s, t)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := isSynthesisName(out[i].Task.Name, out[i].Task.Assignment), isSynthesisName(out[j].Task.Name, out[j].Task.Assignment)
		if si != sj {
			return si
		}
		return out[i].Task.ID < out[j].Task.ID
	})
	return out
}

func reasonsFor(s Snapshot, t TaskSnapshot) []Reason {
	var out []Reason
	add := func(k ReasonKind, flag bool, f string, a ...any) {
		out = append(out, Reason{Kind: k, Text: strings.TrimSpace(fmt.Sprintf(f, a...)), Flag: flag})
	}
	switch t.State {
	case TaskCompleted, TaskFailed:
		// The summary the daemon wrote for the task's scene, else the
		// outcome's first sentence: the outcome itself runs to a page.
		concl := taskNote(s, t.ID)
		if concl == "" {
			concl = clipRunes(proseSentence(t.Outcome), 120)
		}
		if concl != "" {
			add(ReasonConclusion, t.State == TaskFailed, "%s", concl)
		}
	case TaskDispatched:
		add(ReasonConclusion, false, "진행 중")
	}

	tr := s.TaskReasons[t.ID]
	for _, d := range tr.Decisions {
		add(ReasonDecision, false, "%s", d)
	}
	// The file conflicts this task was party to, as they were settled.
	for _, sc := range Scenes(s, timeNowFor(s)) {
		if sc.Kind == SceneExchange && sc.Mark == "⚠" && containsStr(sc.Who, t.ID) && sc.Note != "" {
			add(ReasonDecision, false, "%s", sc.Note)
		}
	}

	if c := strings.TrimSpace(t.Criterion); c != "" {
		met, judged := criterionVerdict(s.Verdicts, t.ID)
		switch {
		case !judged:
			add(ReasonChecked, false, "기준 \"%s\" — 아직 판정 전", clipRunes(c, 80))
		case met:
			add(ReasonChecked, false, "기준 \"%s\" 충족", clipRunes(c, 80))
		default:
			add(ReasonChecked, true, "기준 \"%s\" 미달로 한 번 돌려받음", clipRunes(c, 80))
		}
	}
	if c := strings.TrimSpace(t.Check); c != "" {
		if r := strings.TrimSpace(t.CheckResult); r != "" {
			add(ReasonChecked, false, "check \"%s\" → %s", clipRunes(firstSentence(c), 80), clipRunes(firstSentence(r), 100))
		} else if t.State == TaskCompleted {
			add(ReasonChecked, true, "check \"%s\" — 결과 보고 없음", clipRunes(firstSentence(c), 80))
		}
	}

	for _, q := range s.Questions {
		if q.TaskID != t.ID {
			continue
		}
		switch {
		case q.Open():
			add(ReasonAsked, true, "\"%s\" — 답 없음, %s로 가정", q.Text, orNone(q.Assumed))
		case q.Differs():
			add(ReasonAsked, false, "\"%s\" → %s (가정 %s를 바꿈)", q.Text, q.Answer, orNone(q.Assumed))
		default:
			add(ReasonAsked, false, "\"%s\" → %s", q.Text, q.Answer)
		}
	}

	for _, u := range tr.Unverified {
		add(ReasonUnverified, true, "%s", u)
	}
	return out
}

// criterionVerdict is whether the task's last criterion judgement was met.
func criterionVerdict(vs []Verdict, taskID string) (met, judged bool) {
	for _, v := range vs {
		if v.TaskID == taskID && v.Question == QuestionCriterion && v.Decided {
			judged, met = true, v.Action != ActionReturned
		}
	}
	return met, judged
}

// timeNowFor is the "now" scenes are told against when only provenance is
// wanted: well after the snapshot's own latest moment, so every exchange
// in it reads as settled.
func timeNowFor(s Snapshot) time.Time {
	var at time.Time
	for _, e := range Timeline(s) {
		if e.At.After(at) {
			at = e.At
		}
	}
	return at.Add(time.Hour)
}

// SetTaskReasons records what was read out of a task's outcome.
func (r *Run) SetTaskReasons(taskID string, tr TaskReasons) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.taskReasons == nil {
		r.taskReasons = map[string]TaskReasons{}
	}
	r.taskReasons[taskID] = tr
}

// copyTaskReasons is the reasons for a snapshot. Called under the run lock.
func (r *Run) copyTaskReasons() map[string]TaskReasons {
	if len(r.taskReasons) == 0 {
		return nil
	}
	out := make(map[string]TaskReasons, len(r.taskReasons))
	for k, v := range r.taskReasons {
		out[k] = TaskReasons{Decisions: append([]string(nil), v.Decisions...),
			Unverified: append([]string(nil), v.Unverified...)}
	}
	return out
}

// taskNote is the note written for a task's scene, or "".
func taskNote(s Snapshot, taskID string) string {
	for k, v := range s.SceneNotes {
		if strings.HasPrefix(k, string(SceneTask)+"|"+taskID+"|") {
			return v
		}
	}
	return ""
}

// proseSentence is the first sentence of the outcome's prose: headings,
// rules, fences and table rows skipped, emphasis marks dropped. An outcome
// that opened "# dayx 가이드 (…)" made its conclusion line a heading with
// its marker and nothing it concluded.
func proseSentence(s string) string {
	inFence := false
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			inFence = !inFence
			continue
		case inFence, t == "", strings.HasPrefix(t, "#"), strings.HasPrefix(t, "|"),
			strings.Trim(t, "-=*_ ") == "":
			continue
		}
		t = strings.TrimLeft(t, "-*>• ")
		t = strings.NewReplacer("**", "", "__", "", "`", "").Replace(t)
		return firstSentence(t)
	}
	return firstSentence(s)
}

// firstSentence is the text's first sentence, or its first line. A period
// ends a sentence only where prose would put one — after a word, before a
// space and a capital or Hangul — so "./..." and "1.5" do not.
func firstSentence(s string) string {
	line := firstLine(strings.TrimSpace(s))
	r := []rune(line)
	for i := 1; i+2 < len(r); i++ {
		if (r[i] == '.' || r[i] == '。') && r[i+1] == ' ' && r[i-1] != '.' && r[i-1] != '/' &&
			(unicode.IsUpper(r[i+2]) || unicode.Is(unicode.Hangul, r[i+2])) {
			return string(r[:i+1])
		}
	}
	return line
}

func clipRunes(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
