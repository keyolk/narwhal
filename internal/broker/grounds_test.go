package broker

import (
	"strings"
	"testing"
)

// The question that prompted this: the planner used a label from the
// previous step's findings, which the operator had not read.
func TestAQuestionCarriesWhatItsLabelsMean(t *testing.T) {
	flow := "## Step 3\n\ndayx 검증 완료. hook SessionStart p99 15.5ms·UserPromptSubmit p99 55.6ms. " +
		"새 갭 N1(PR 나이=dayx 관측 기준), N2(최악 7분), N3(UserPromptSubmit p99>50ms). " +
		"설치 권장: settings.json hook은 SessionStart만 권장, UserPromptSubmit은 N3 해결 전 보류.\n"
	r := New().CreateRun("r", "local 환경에 설치 및 설정", t.TempDir(), "main")
	q := r.AskSpec(PlanAsker, QuestionSpec{
		Text:    "hook 범위: step 3 권고대로 SessionStart만 설치하고 UserPromptSubmit(N3: p99 55.6ms > 50ms)은 보류할까요?",
		Assumed: "SessionStart만", Blocking: true, Sources: []string{flow}})
	joined := strings.Join(q.Grounds, "\n")
	if !strings.Contains(joined, "N3: ") || !strings.Contains(joined, "N3(UserPromptSubmit p99>50ms)") {
		t.Errorf("grounds do not define N3:\n%s", joined)
	}
	if !strings.Contains(joined, "step 3: ") {
		t.Errorf("grounds do not say what step 3 is:\n%s", joined)
	}
}

// A worker's question with no label falls back to its assignment.
func TestAWorkersQuestionFallsBackToItsAssignment(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.AddTask("api", "api", "Validate the lock key in a middleware before the handler runs.", nil)
	q := r.Ask("api", "Reject or warn on an expired key?", "reject", false)
	if len(q.Grounds) != 1 || !strings.Contains(q.Grounds[0], "Validate the lock key") {
		t.Errorf("grounds = %v", q.Grounds)
	}
}

func TestLabels(t *testing.T) {
	got := strings.Join(Labels("N3 and G2, then step 3 and N3 again; not p99 or UTF8"), ",")
	if got != "N3,G2,step 3" {
		t.Errorf("labels = %s", got)
	}
}
