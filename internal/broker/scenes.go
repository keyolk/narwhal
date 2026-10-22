// scenes.go tells a run's history as scenes a person can follow, not as
// the events it is made of.
//
// The timeline (timeline.go) is a faithful log: every start, every
// relay, every verdict, each a line of the same weight. Read back, it was
// not a story. On run s1791824779359-1 the thing that happened — two
// tasks reached for the same test file, the synthesis settled who owned
// it, and the other asked for its change instead — was a FILE_CONFLICT
// line and three cut-off radio posts, among "verdict stuck → complete"
// lines that said, to anyone not reading the code, that something had
// finished.
//
// A scene is a stretch of the run with one subject: a task's whole course
// (its start, its retries, the gates it was handed back by, its end), an
// exchange between workers that came close together, an instruction and
// where it went. Each is told in a sentence or two of Korean, the judge's
// calls translated into what they meant and the ones that changed nothing
// left out. The last scene is "now": what is still going and what is left.
//
// The events stay underneath. A scene keeps the ones it was made of, and
// the monitor opens a scene into them.
//
// Where a scene is an exchange in the workers' own words, rules cannot say
// what was agreed. The daemon asks a model for a one-line summary once the
// scene has closed and records it on the run (SceneNotes); a scene with a
// note shows the note, one without shows what the rules could say.
package broker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SceneKind says what a scene is about.
type SceneKind string

const (
	SceneRun      SceneKind = "run"      // the run starting, and what it continues
	SceneTask     SceneKind = "task"     // one task's course
	SceneExchange SceneKind = "exchange" // workers coordinating, close together
	SceneOperator SceneKind = "operator" // an instruction and where it went
	SceneNow      SceneKind = "now"      // where the run stands
)

// Scene is one stretch of a run's history with one subject.
type Scene struct {
	// Key identifies the scene across snapshots, so a note written for it
	// finds it again: the kind, the subject and the first event's time.
	Key   string
	Kind  SceneKind
	Start time.Time
	End   time.Time
	// Who is the task or tasks the scene is about.
	Who []string
	// Mark is the scene's state at a glance: ✓ done, ✗ failed, ⚠ a
	// conflict or a handback, ▸ under way, » the operator.
	Mark string
	// Title is the scene in one line; Lines are what follows it.
	Title string
	Lines []string
	// Note is the model's summary of an exchange, when the daemon has
	// written one.
	Note string
	// Closed reports whether the scene can still change. Only a closed
	// scene is summarised.
	Closed bool
	// Events are what the scene was made of, oldest first.
	Events []TimelineEvent
	// Outcome is a finished task's whole outcome, for the summariser.
	Outcome string
}

// sceneGap is how long the workers' posts can be apart and still be one
// exchange. A FILE_CONFLICT and the replies that settle it land within a
// minute of each other; the next unrelated post is usually several away.
const sceneGap = 3 * time.Minute

// sceneSettle is how long an exchange must have been quiet before it is
// closed, and so summarised.
const sceneSettle = 2 * time.Minute

// Scenes returns the run's history as scenes, oldest first, ending with
// where the run stands now.
func Scenes(s Snapshot, now time.Time) []Scene {
	events := Timeline(s)
	tasks := map[string]TaskSnapshot{}
	for _, t := range s.Tasks {
		tasks[t.ID] = t
	}

	var out []Scene
	byTask := map[string]int{}
	var exchange *Scene
	closeExchange := func() {
		if exchange != nil {
			out = append(out, *exchange)
			exchange = nil
		}
	}

	for _, e := range events {
		switch e.Kind {
		case TimelineRun, TimelineFollows:
			if len(out) == 0 || out[0].Kind != SceneRun {
				out = append([]Scene{{Kind: SceneRun, Start: e.At, End: e.At, Closed: true}}, out...)
				for k := range byTask {
					byTask[k]++
				}
			}
			out[0].Events = append(out[0].Events, e)

		case TimelineStarted, TimelineCompleted, TimelineFailed, TimelineVerdict, TimelineAsked:
			if e.Who == "" {
				continue
			}
			i, ok := byTask[e.Who]
			if !ok {
				closeExchange()
				out = append(out, Scene{Kind: SceneTask, Who: []string{e.Who}, Start: e.At})
				i = len(out) - 1
				byTask[e.Who] = i
			}
			out[i].Events = append(out[i].Events, e)
			out[i].End = e.At

		case TimelineInstruction:
			closeExchange()
			out = append(out, Scene{Kind: SceneOperator, Who: []string{"operator"}, Start: e.At, End: e.At,
				Events: []TimelineEvent{e}, Closed: true})

		case TimelineRelay:
			// A relay belongs with the instruction it carries, when there
			// is one just before it.
			if n := len(out); n > 0 && out[n-1].Kind == SceneOperator && e.At.Sub(out[n-1].End) < sceneGap {
				out[n-1].Events = append(out[n-1].Events, e)
				out[n-1].End = e.At
				continue
			}
			fallthrough

		case TimelineUrgent:
			if exchange != nil && e.At.Sub(exchange.End) >= sceneGap {
				closeExchange()
			}
			if exchange == nil {
				exchange = &Scene{Kind: SceneExchange, Start: e.At}
			}
			exchange.Events = append(exchange.Events, e)
			exchange.End = e.At
			if e.Who != "" && !containsStr(exchange.Who, e.Who) {
				exchange.Who = append(exchange.Who, e.Who)
			}
		}
	}
	closeExchange()

	for i := range out {
		sc := &out[i]
		if len(sc.Events) > 0 {
			sc.Key = fmt.Sprintf("%s|%s|%d", sc.Kind, strings.Join(sc.Who, ","), sc.Events[0].At.Unix())
		}
		switch sc.Kind {
		case SceneRun:
			tellRun(sc, s)
		case SceneTask:
			tellTask(sc, tasks[sc.Who[0]])
		case SceneOperator:
			tellOperator(sc)
		case SceneExchange:
			tellExchange(sc)
			sc.Closed = now.Sub(sc.End) >= sceneSettle
		}
		if note, ok := s.SceneNotes[sc.Key]; ok {
			sc.Note = note
		}
	}
	// A task's scene is placed where its story ends, so the history reads
	// in the order things were settled; one still running sits at its
	// start.
	sort.SliceStable(out, func(i, j int) bool { return sceneAt(out[i]).Before(sceneAt(out[j])) })

	return append(out, tellNow(s, now))
}

// sceneAt is when a scene is placed and shown: a finished task where it
// finished, everything else where it began.
func sceneAt(sc Scene) time.Time {
	if sc.Kind == SceneTask && sc.Closed {
		return sc.End
	}
	return sc.Start
}

// At is when the scene is shown as having happened.
func (sc Scene) At() time.Time { return sceneAt(sc) }

func tellRun(sc *Scene, s Snapshot) {
	sc.Mark = "◆"
	sc.Title = firstLine(s.Prompt)
	if s.ParentRun != "" {
		sc.Lines = append(sc.Lines, "앞 단계("+s.ParentRun+")를 이어받음")
	}
	var work, synth int
	for _, t := range s.Tasks {
		if isSynthesisName(t.Name, t.Assignment) {
			synth++
		} else {
			work++
		}
	}
	plan := fmt.Sprintf("task %d개로 나눔", work)
	if synth > 0 {
		plan = fmt.Sprintf("task %d개와 synthesis로 나눔", work)
	}
	sc.Lines = append(sc.Lines, plan)
}

// tellTask tells one task's course: how it started, what got in its way,
// how it ended.
func tellTask(sc *Scene, t TaskSnapshot) {
	sc.Outcome = t.Outcome
	name := t.ID
	if t.Name != "" && t.Name != t.ID && !strings.HasPrefix(t.Name, t.ID) {
		name = t.ID + " " + t.Name
	}
	retries, noResult, returned := 0, 0, map[string]int{}
	// Attempts from a reopen on are the operator's follow-up, not
	// failures; see followup.go.
	failedTries := t.Dispatches
	if t.ReopenedAt > 0 {
		failedTries = t.ReopenedAt
		sc.Lines = append(sc.Lines, "운영자 지시로 다시 열려 이어서 함")
	}
	var stuck, escalated bool
	for _, e := range sc.Events {
		switch e.Kind {
		case TimelineStarted:
			var n int
			if _, err := fmt.Sscanf(e.Text, "attempt %d", &n); err == nil && n <= failedTries {
				retries = n - 1
			}
		case TimelineVerdict:
			q, action, _ := strings.Cut(e.Text, " → ")
			switch {
			case action == ActionReturned:
				returned[q]++
			case q == QuestionNoResult && action == ActionRetry:
				noResult++
			case action == ActionEscalate:
				escalated = true
			case action == ActionFlagged:
				stuck = true
			}
		case TimelineAsked:
			sc.Lines = append(sc.Lines, "판단을 물음: "+e.Text)
		}
	}
	switch t.State {
	case TaskCompleted:
		sc.Mark, sc.Closed = "✓", true
		sc.Title = name + " 완료"
		if o := firstLine(t.Outcome); o != "" {
			sc.Lines = append([]string{o}, sc.Lines...)
		}
	case TaskFailed:
		sc.Mark, sc.Closed = "✗", true
		sc.Title = name + " 실패"
		if o := firstLine(t.Outcome); o != "" {
			sc.Lines = append([]string{o}, sc.Lines...)
		}
	case TaskDispatched:
		sc.Mark = "▸"
		sc.Title = name + " 진행 중"
	default:
		// Started once and back in the queue: a failed attempt waiting for
		// its retry.
		sc.Mark = "·"
		sc.Title = name + " 재시도 대기"
		if o := firstLine(t.Outcome); o != "" {
			sc.Lines = append([]string{"직전 시도: " + o}, sc.Lines...)
		}
	}
	if n := max(noResult, retries); n > 0 && t.ReopenedAt == 0 {
		sc.Lines = append(sc.Lines, fmt.Sprintf("결과 없이 %d번 끝나 다시 시작함 (시도 %d)", n, n+1))
	}
	if escalated {
		sc.Lines = append(sc.Lines, "같은 원인으로 거듭 실패해 더 강한 모델로 올림")
	}
	for _, q := range []string{QuestionCriterion, QuestionGaveUp, QuestionAnswered, QuestionFollowed, QuestionFolded} {
		if returned[q] == 0 {
			continue
		}
		why := map[string]string{
			QuestionCriterion: "planner가 정한 기준(criterion)에 못 미쳐",
			QuestionAnswered:  "결과가 과제에 대한 답으로 읽히지 않아",
			QuestionGaveUp:    "다른 방법을 시도하지 않은 부정 결론이라",
			QuestionFolded:    "peer의 결과를 빠뜨려",
			QuestionFollowed:  "운영자의 답을 따르지 않은 결과라",
		}[q]
		sc.Lines = append(sc.Lines, why+" 한 번 돌려받음")
	}
	if stuck {
		sc.Mark = "⚠"
		sc.Lines = append(sc.Lines, "같은 동작을 반복하며 진전이 없어 보인다고 운영자에게 알림")
	}
	if len(returned) > 0 && sc.Mark == "✓" {
		sc.Mark = "✓"
	}
}

func tellOperator(sc *Scene) {
	sc.Mark = "»"
	sc.Title = "지시: " + firstLine(sc.Events[0].Text)
	for _, e := range sc.Events[1:] {
		if e.Kind == TimelineRelay {
			sc.Lines = append(sc.Lines, relayLine(e.Text))
		}
	}
}

// relayLine is a coordinator relay said as what happened.
func relayLine(text string) string {
	switch {
	case strings.HasPrefix(text, "from the operator, for "):
		who, _, _ := strings.Cut(strings.TrimPrefix(text, "from the operator, for "), ":")
		return who + "에게 전달함"
	case strings.HasPrefix(text, "reopened "):
		ids, rest, _ := strings.Cut(strings.TrimPrefix(text, "reopened "), " with the operator's instruction")
		line := ids + "을(를) 다시 열어 이어서 하게 함"
		if _, behind, ok := strings.Cut(rest, "resuming"); ok {
			if deps, _, ok := strings.Cut(behind, ", which build on it"); ok {
				deps = strings.TrimSpace(strings.TrimLeft(deps, " .its worker their workers"))
				if deps != "" {
					line += " · 이를 바탕으로 한 " + deps + "도 함께 다시 열림"
				}
			}
		}
		return line
	case strings.Contains(text, "no running worker's task covers"),
		strings.Contains(text, "no worker is running"):
		return "받아 처리할 워커가 없음"
	}
	return text
}

// tellExchange says what the workers' posts in a stretch were about, as
// far as rules can tell: a conflict over files, who asked whom.
func tellExchange(sc *Scene) {
	sc.Mark = "·"
	var conflict []string
	var asks []string
	for _, e := range sc.Events {
		switch {
		case strings.HasPrefix(e.Text, "FILE_CONFLICT"):
			conflict = append(conflict, conflictSubject(e.Text))
		case strings.Contains(e.Text, "looks stuck"):
			sc.Mark = "⚠"
			sc.Lines = append(sc.Lines, relayLine(e.Text))
		default:
			if e.Kind == TimelineRelay {
				sc.Lines = append(sc.Lines, relayLine(e.Text))
				continue
			}
			asks = append(asks, e.Who+": "+firstLine(e.Text))
		}
	}
	who := strings.Join(sc.Who, ", ")
	switch {
	case len(conflict) > 0:
		sc.Mark = "⚠"
		sc.Title = "파일 소유 충돌"
		if conflict[0] != "" {
			sc.Title += ": " + conflict[0]
		}
		if who != "" {
			sc.Lines = append(sc.Lines, who+"이(가) 조율함")
		}
	case len(asks) > 0 && len(sc.Who) == 1:
		if to := addressee(sc.Events); to != "" {
			sc.Title = sc.Who[0] + "이(가) " + to + "에게 요청"
		} else {
			sc.Title = sc.Who[0] + "의 알림"
		}
	case len(asks) > 0:
		sc.Title = who + " 사이 조율"
	default:
		sc.Title = "coordinator 알림"
	}
	sc.Lines = append(sc.Lines, asks...)
}

// tellNow is where the run stands: what is going, what is left, how it
// ended.
func tellNow(s Snapshot, now time.Time) Scene {
	sc := Scene{Kind: SceneNow, Key: "now", Start: now, End: now}
	var running, waiting, done, failed []string
	for _, t := range s.Tasks {
		switch t.State {
		case TaskDispatched:
			running = append(running, t.ID)
		case TaskReady, TaskPending:
			waiting = append(waiting, t.ID)
		case TaskCompleted:
			done = append(done, t.ID)
		case TaskFailed:
			failed = append(failed, t.ID)
		}
	}
	sort.Strings(running)
	sort.Strings(waiting)
	switch {
	case len(s.Tasks) == 0:
		sc.Mark, sc.Title = "·", "아직 계획 중"
	case len(running)+len(waiting) == 0:
		sc.Closed = true
		if len(failed) > 0 {
			sc.Mark = "✗"
			sc.Title = fmt.Sprintf("끝남 — %d개 완료, %d개 실패(%s)", len(done), len(failed), strings.Join(failed, ", "))
		} else {
			sc.Mark = "✓"
			sc.Title = fmt.Sprintf("끝남 — task %d개 모두 완료", len(done))
		}
	default:
		sc.Mark = "▸"
		sc.Title = fmt.Sprintf("%d/%d 완료", len(done), len(s.Tasks))
		if len(running) > 0 {
			sc.Lines = append(sc.Lines, "진행 중: "+strings.Join(running, ", "))
		}
		if len(waiting) > 0 {
			sc.Lines = append(sc.Lines, "대기: "+strings.Join(waiting, ", "))
		}
	}
	return sc
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SetSceneNote records a scene's summary.
func (r *Run) SetSceneNote(key, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sceneNotes == nil {
		r.sceneNotes = map[string]string{}
	}
	r.sceneNotes[key] = strings.TrimSpace(note)
}

// copySceneNotes is the notes for a snapshot. Called under the run lock.
func (r *Run) copySceneNotes() map[string]string {
	if len(r.sceneNotes) == 0 {
		return nil
	}
	out := make(map[string]string, len(r.sceneNotes))
	for k, v := range r.sceneNotes {
		out[k] = v
	}
	return out
}

// conflictSubject is a FILE_CONFLICT line's path and holder:
// "internal/infer/infer_test.go (task-3 소유)".
func conflictSubject(text string) string {
	_, rest, ok := strings.Cut(text, "task. ")
	if !ok {
		return ""
	}
	path, holder, ok := strings.Cut(firstLine(rest), " → held by ")
	if !ok {
		return strings.TrimSpace(path)
	}
	holder, _, _ = strings.Cut(holder, " ")
	return strings.TrimSpace(path) + " (" + holder + " 소유)"
}

// addressee is who a worker's posts in a scene were addressed to, from the
// "[from->to]" tag workers put on direct requests.
func addressee(events []TimelineEvent) string {
	for _, e := range events {
		if i := strings.Index(e.Text, "->"); i > 0 && strings.HasPrefix(e.Text, "[") {
			if j := strings.Index(e.Text[i:], "]"); j > 0 {
				return e.Text[i+2 : i+j]
			}
		}
	}
	return ""
}
