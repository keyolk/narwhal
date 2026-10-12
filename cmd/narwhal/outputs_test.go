package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// withOutputs gives the fixture what the plan run on disk had: a plan file
// a task recorded, and a report posted to the results thread instead of
// the outcome.
func withOutputs(t *testing.T) tuiModel {
	t.Helper()
	m := fixtureRun(t)
	must(t, os.WriteFile(filepath.Join(m.snap.CWD, "PLAN.md"), []byte("# Lock plan\n\n- step one\n- step two\n"), 0o644))
	m.snap.Tasks[0].Artifacts = &broker.Artifacts{Files: []string{"PLAN.md"}}
	m.snap.Messages = append(m.snap.Messages, &broker.Message{Seq: 7, Sender: "worker-client",
		ThreadID: broker.ResultsThread, Content: "## Client findings\n\nthree mappers folded into one"})
	return m
}

func TestRunOutputsListsFilesReportsThenAnswers(t *testing.T) {
	m := withOutputs(t)
	outs := runOutputs(m.snap, 0)
	var got []string
	for _, o := range outs {
		got = append(got, outputLabels[o.kind]+":"+o.task)
	}
	want := []string{"file:schema", "report:client", "answer:client", "answer:schema"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	if outs[0].path != filepath.Join(m.snap.CWD, "PLAN.md") {
		t.Errorf("file path = %q, want it resolved against the run's directory", outs[0].path)
	}
	if !strings.HasPrefix(outs[1].title, "Client findings") {
		t.Errorf("report title = %q", outs[1].title)
	}
}

func TestOutputsViewShowsTheSelectedOutputRendered(t *testing.T) {
	m := press(withOutputs(t), "o")
	if !m.outputsView {
		t.Fatal("o on the run view did not open Outputs")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Outputs", "PLAN.md", "Lock plan", "step two", "Client findings"} {
		if !strings.Contains(view, want) {
			t.Errorf("Outputs view lacks %q:\n%s", want, view)
		}
	}
	m = press(m, "j")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "three mappers folded into one") {
		t.Errorf("j did not show the report:\n%s", view)
	}
	m = press(m, "esc")
	if m.outputsView {
		t.Error("esc did not leave Outputs")
	}
}

func TestOutputsFromATaskStartsAtItsOutput(t *testing.T) {
	m := withOutputs(t)
	m.openOutputs("client")
	outs := runOutputs(m.snap, 0)
	if outs[m.outputSel].task != "client" {
		t.Fatalf("selected %s's output, want client's", outs[m.outputSel].task)
	}
}

func TestOutputsViewDrawsAtAnySize(t *testing.T) {
	m := withOutputs(t)
	m.outputsView = true
	for w := 0; w <= 12; w++ {
		for h := 0; h <= 12; h++ {
			m.width, m.height = w, h
			_ = m.View()
		}
	}
}

func TestPageOutputReportsAMissingFile(t *testing.T) {
	cmd := openOutput(output{kind: outFile, path: filepath.Join(t.TempDir(), "gone.md")})
	msg, ok := cmd().(noticeMsg)
	if !ok || msg.err == nil {
		t.Fatalf("paging a missing file gave %#v, want an error notice", msg)
	}
}

func TestOutputsReadEachKindItsOwnWay(t *testing.T) {
	m := withOutputs(t)
	cwd := m.snap.CWD
	must(t, os.WriteFile(filepath.Join(cwd, "main.go"), []byte("// # not a heading\npackage main\n\tfunc f() {}\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(cwd, "blob.bin"), []byte{0x7f, 'E', 0, 1, 2}, 0o644))
	m.snap.Tasks[0].Artifacts.Files = append(m.snap.Tasks[0].Artifacts.Files, "main.go", "blob.bin")

	pick := func(title string) output {
		for _, o := range runOutputs(m.snap, 0) {
			if o.title == title {
				return o
			}
		}
		t.Fatalf("no output %q", title)
		return output{}
	}
	code := ansi.Strip(strings.Join(m.outputBody(pick("main.go"), 80), "\n"))
	if !strings.Contains(code, "   1 // # not a heading") || !strings.Contains(code, "   3     func f() {}") {
		t.Errorf("code is not shown as written with line numbers:\n%s", code)
	}
	plan := ansi.Strip(strings.Join(m.outputBody(pick("PLAN.md"), 80), "\n"))
	if strings.Contains(plan, "# Lock plan") || !strings.Contains(plan, "Lock plan") {
		t.Errorf("markdown is not rendered:\n%s", plan)
	}
	bin := ansi.Strip(strings.Join(m.outputBody(pick("blob.bin"), 80), "\n"))
	if !strings.Contains(bin, "binary file") {
		t.Errorf("a binary file is not described as one:\n%s", bin)
	}
}

func TestACommitIsShownAsGitShowsIt(t *testing.T) {
	m := withOutputs(t)
	cwd := m.snap.CWD
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", cwd, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "PLAN.md")
	git("commit", "-qm", "Write the lock plan")
	hash := git("rev-parse", "--short", "HEAD")
	m.snap.Tasks[0].Artifacts.Commits = []string{hash}

	m = press(m, "o")
	var commit output
	for _, o := range m.outputs() {
		if o.kind == outCommit {
			commit = o
		}
	}
	if d := m.outputDetails[commit.key()]; !d.pending {
		t.Fatalf("opening Outputs did not ask git about the commit: %+v", d)
	}
	next, _ := m.Update(detailCmd(commit)())
	m = next.(tuiModel)
	body := ansi.Strip(strings.Join(m.outputBody(commit, 100), "\n"))
	if !strings.Contains(body, "Write the lock plan") || !strings.Contains(body, "PLAN.md") {
		t.Errorf("the commit is not shown with its message and files:\n%s", body)
	}
}

func TestFlowOutputsListEveryStep(t *testing.T) {
	m := withOutputs(t)
	first := m.snap
	first.RunID = "r-step1"
	first.Tasks = []broker.TaskSnapshot{{ID: "plan", State: broker.TaskCompleted, Outcome: "the plan"}}
	first.Messages = nil
	m.snapCache = map[string]cachedSnap{first.RunID: {first, time.Now()}}

	m.openFlowOutputs([]string{"r-step1", m.snap.RunID}, m.snap.RunID)
	outs := m.outputs()
	if outs[0].run != "r-step1" || outs[m.outputSel].run != m.snap.RunID {
		t.Fatalf("flow outputs start %s, selected %s; want step 1 listed first and step 2 selected",
			outs[0].run, outs[m.outputSel].run)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Step 1", "Step 2", "2 steps"} {
		if !strings.Contains(view, want) {
			t.Errorf("flow Outputs lacks %q:\n%s", want, view)
		}
	}
}

func TestEnterOnAnAnswerGoesToItsTask(t *testing.T) {
	m := withOutputs(t)
	m.openOutputs("client")
	for runOutputs(m.snap, 0)[m.outputSel].kind != outAnswer {
		m.outputSel++
	}
	m = press(m, "enter")
	if m.outputsView || m.detail != detailSession {
		t.Fatalf("enter on an answer: outputsView=%v detail=%v, want the task view", m.outputsView, m.detail)
	}
	if task, _ := m.selectedTask(); task.ID != "client" {
		t.Errorf("opened %s, want client", task.ID)
	}
}
