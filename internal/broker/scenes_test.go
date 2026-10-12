package broker

import (
	"strings"
	"testing"
	"time"
)

func sceneSnap() Snapshot {
	at := func(m int) time.Time { return time.Date(2026, 10, 13, 2, m, 0, 0, time.Local) }
	return Snapshot{
		RunID: "r", Prompt: "test 및 검증까지 수행", StartedAt: at(6).Unix(), ParentRun: "r0",
		Tasks: []TaskSnapshot{
			{ID: "task-1", Name: "go-backend-tests", State: TaskCompleted, Dispatches: 1,
				StartedAt: at(7), EndedAt: at(14), Outcome: "go test -race 통과 (105개)"},
			{ID: "task-3", Name: "nudge", State: TaskDispatched, Dispatches: 1, StartedAt: at(7)},
			{ID: "synthesis", Name: "synthesis", State: TaskDispatched, Dispatches: 3, StartedAt: at(9)},
		},
		Messages: []*Message{
			{Seq: 1, Sender: "coordinator", Priority: PriorityUrgent, CreatedAt: at(8),
				Content: "FILE_CONFLICT: these paths are already held by another task. internal/infer/infer_test.go → held by task-3 Coordinate on the radio before writing; do not overwrite."},
			{Seq: 2, Sender: "worker-task-1", Priority: PriorityUrgent, CreatedAt: at(8), Content: "[task-1->task-3] infer_test.go는 네가 갖고 있으니 부탁"},
			{Seq: 3, Sender: "worker-task-1", Priority: PriorityUrgent, CreatedAt: at(13), Content: "[task-1->task-3] 6줄 수정만 내가 할게"},
		},
		Verdicts: []Verdict{
			{At: at(8), TaskID: "synthesis", Question: QuestionNoResult, Decided: true, P: 0.8, Action: ActionRetry},
			{At: at(9), TaskID: "synthesis", Question: QuestionNoResult, Decided: true, P: 0.8, Action: ActionRetry},
			{At: at(14), TaskID: "task-1", Question: QuestionCriterion, Decided: true, P: 0.02, Action: ActionReturned},
			{At: at(14), TaskID: "task-1", Question: QuestionAnswered, Decided: true, P: 0.9, Action: ActionComplete},
			{At: at(15), TaskID: "task-3", Question: QuestionStuck, Decided: true, P: 0.1, Action: ActionComplete},
		},
	}
}

func TestScenesTellTheRunAsAPersonWould(t *testing.T) {
	scenes := Scenes(sceneSnap(), time.Date(2026, 10, 13, 2, 30, 0, 0, time.Local))
	var titles []string
	for _, sc := range scenes {
		titles = append(titles, sc.Mark+" "+sc.Title)
	}
	got := strings.Join(titles, "\n")
	for _, want := range []string{
		"◆ test 및 검증까지 수행",
		"⚠ 파일 소유 충돌: internal/infer/infer_test.go (task-3 소유)",
		"▸ synthesis 진행 중",
		"· task-1이(가) task-3에게 요청",
		"✓ task-1 go-backend-tests 완료",
		"▸ 1/3 완료",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no scene %q in:\n%s", want, got)
		}
	}
	lines := map[string]string{}
	for _, sc := range scenes {
		lines[sc.Title] = strings.Join(sc.Lines, " | ")
	}
	if l := lines["synthesis 진행 중"]; !strings.Contains(l, "결과 없이 2번 끝나 다시 시작함 (시도 3)") {
		t.Errorf("the synthesis retries are not told: %q", l)
	}
	if l := lines["task-1 go-backend-tests 완료"]; !strings.Contains(l, "기준(criterion)에 못 미쳐 한 번 돌려받음") ||
		strings.Contains(l, "answered") {
		t.Errorf("task-1's course: %q", l)
	}
	// A judgement that changed nothing is not a scene or a line.
	if strings.Contains(got, "stuck") || strings.Contains(strings.Join(func() []string {
		var all []string
		for _, sc := range scenes {
			all = append(all, sc.Lines...)
		}
		return all
	}(), " "), "멈") {
		t.Errorf("a stuck → complete verdict was told:\n%s", got)
	}
	if l := lines["1/3 완료"]; !strings.Contains(l, "진행 중: synthesis, task-3") {
		t.Errorf("now: %q", l)
	}
}

// A reopened task's later attempts are the operator's follow-up, not
// retries.
func TestAReopenedTaskIsNotToldAsRetried(t *testing.T) {
	s := sceneSnap()
	s.Tasks[0].Dispatches, s.Tasks[0].ReopenedAt = 2, 1
	for _, sc := range Scenes(s, time.Now()) {
		if sc.Kind == SceneTask && sc.Who[0] == "task-1" {
			l := strings.Join(sc.Lines, " | ")
			if strings.Contains(l, "다시 시작함") || !strings.Contains(l, "운영자 지시로 다시 열려") {
				t.Errorf("task-1: %q", l)
			}
		}
	}
}

// A scene keeps its key across snapshots, so a note written for it finds it.
func TestASceneNoteFindsItsScene(t *testing.T) {
	s := sceneSnap()
	var key string
	for _, sc := range Scenes(s, time.Now()) {
		if sc.Kind == SceneExchange {
			key = sc.Key
			break
		}
	}
	s.SceneNotes = map[string]string{key: "task-3 소유로 정리됨"}
	for _, sc := range Scenes(s, time.Now()) {
		if sc.Key == key && sc.Note != "task-3 소유로 정리됨" {
			t.Errorf("note not attached: %+v", sc)
		}
	}
}
