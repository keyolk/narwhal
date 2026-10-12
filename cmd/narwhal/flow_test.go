package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// flowModel is a two-step flow on disk — a plan, then a build continuing
// it — beside an unrelated run, on the home screen.
func flowModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	plan := broker.Snapshot{RunID: "p1", Prompt: "plan the lock API", CWD: "/src/repo", State: broker.RunDone,
		StartedAt: now.Add(-2 * time.Hour).Unix(), Flow: "p1",
		Tasks: []broker.TaskSnapshot{{ID: "synthesis", Name: "synthesis", State: broker.TaskCompleted,
			Outcome: "## Plan\n\n| step | what |\n|---|---|\n| 1 | middleware |\n\nbranch `lock-plan`"}}}
	build := broker.Snapshot{RunID: "b2", Prompt: "build it", CWD: "/src/repo", State: broker.RunDone,
		StartedAt: now.Add(-time.Hour).Unix(), Flow: "p1", ParentRun: "p1",
		Tasks: []broker.TaskSnapshot{{ID: "api", State: broker.TaskCompleted,
			Outcome:   "done: https://github.com/o/r/pull/7 and commit `abc1234`",
			Artifacts: &broker.Artifacts{Files: []string{"internal/api/mw.go"}}}}}
	other := broker.Snapshot{RunID: "x9", Prompt: "audit TLS", CWD: "/src/ops", State: broker.RunDone,
		StartedAt: now.Add(-30 * time.Minute).Unix(),
		Tasks:     []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted, Outcome: "fine"}}}
	for _, s := range []broker.Snapshot{plan, build, other} {
		must(t, store.SaveRun(s))
	}
	if _, err := store.StartFlow("p1", "/src/repo", "plan the lock API"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFlowStep("p1", "b2", "build it"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFlowNotes("p1", "keep the v1 wire format"); err != nil {
		t.Fatal(err)
	}

	runs := store.Discover(nil)
	m := newTUIModel(runs, 0, 0, true)
	m.applyFlows(store.ListFlows())
	m.width, m.height = 130, 40
	return m
}

// Home shows a flow of several runs as its title over its steps, and a
// lone run as a row of its own.
func TestHomeGroupsAFlowsRunsUnderItsTitle(t *testing.T) {
	got := ansi.Strip(flowModel(t).View())
	d := icons.runDone
	title, s1, s2 := strings.Index(got, "plan the lock API  2 steps"), strings.Index(got, "├ "+d+" 1 plan the lock API"), strings.Index(got, "└ "+d+" 2 build it")
	if title < 0 || s1 < title || s2 < s1 {
		t.Fatalf("flow not grouped:\n%s", got)
	}
	if !strings.Contains(got, "audit TLS") {
		t.Errorf("the lone run is missing:\n%s", got)
	}
}

// enter on a step opens the flow: its goal, notes and steps, the selected
// step's request and result, and everything it produced.
func TestTheFlowViewShowsInputAndOutput(t *testing.T) {
	m := flowModel(t)
	for i, r := range m.runs {
		if r.RunID == "b2" {
			m.runCur = i
		}
	}
	m = press(m, "enter")
	if !m.flowView || m.flowID != "p1" || m.flowSel != 1 {
		t.Fatalf("flow view: %v %q sel %d", m.flowView, m.flowID, m.flowSel)
	}
	got := ansi.Strip(m.View())
	for _, w := range []string{"Goal", "plan the lock API", "Notes", "keep the v1 wire format",
		"Steps", "1 " + icons.runDone + " plan the lock API", "2 " + icons.runDone + " build it", "Step 2", "Asked", "build it", "Result",
		"o/r#7", "abc1234", "internal/api/mw.go", "2 steps · 4 artifacts"} {
		if !strings.Contains(got, w) {
			t.Errorf("flow view lacks %q:\n%s", w, got)
		}
	}
	// The first step's result renders its markdown.
	m = press(m, "k")
	got = ansi.Strip(m.View())
	if !strings.Contains(got, "step  what") || strings.Contains(got, "|---|") {
		t.Errorf("step 1's table not rendered:\n%s", got)
	}
	// enter opens the run; esc comes back to the flow, then home.
	m = press(m, "enter")
	if m.flowView || m.live.RunID != "p1" {
		t.Fatalf("enter did not open the run: flow=%v run=%q", m.flowView, m.live.RunID)
	}
	if m = press(m, "esc"); !m.flowView {
		t.Fatal("esc from a flow's run did not go back to the flow")
	}
	if m = press(m, "esc"); !m.picker || m.flowView {
		t.Fatal("esc from the flow did not go home")
	}
}

// A finished lone run opens on its result: answer, artifacts, tasks.
func TestAFinishedRunOpensOnItsResult(t *testing.T) {
	m := flowModel(t)
	for i, r := range m.runs {
		if r.RunID == "x9" {
			m.runCur = i
		}
	}
	m = press(m, "enter")
	if m.detail != detailResult {
		t.Fatalf("detail %v, want the result", m.detail)
	}
	m.snap, _ = store.LoadRun("x9")
	got := ansi.Strip(m.View())
	for _, w := range []string{"Result", "Answer", "fine", "Artifacts", "none recorded", "Tasks"} {
		if !strings.Contains(got, w) {
			t.Errorf("result lacks %q:\n%s", w, got)
		}
	}
}

// The run header of a flow's step names the steps, this one marked; [ and
// ] move between them.
func TestARunInAFlowShowsAndWalksItsSteps(t *testing.T) {
	m := selectRun(t, flowModel(t), "p1")
	m = press(m, "enter", "enter") // the flow, then step 1
	m.snap, _ = store.LoadRun("p1")
	got := ansi.Strip(m.viewHeader())
	if !strings.Contains(got, "[1 "+icons.runDone+" plan the lock API 1/1]") || !strings.Contains(got, "2 "+icons.runDone+" build it") {
		t.Fatalf("stages:\n%s", got)
	}
	if m = press(m, "]"); m.live.RunID != "b2" {
		t.Fatalf("] went to %q", m.live.RunID)
	}
	if m = press(m, "["); m.live.RunID != "p1" {
		t.Fatalf("[ went to %q", m.live.RunID)
	}
}

// n in a run starts its flow's next step; a run from before flows is
// adopted into one first.
func TestTheNextStepContinuesTheFlow(t *testing.T) {
	var gotFlow, gotPrompt string
	saved := startRequest
	startRequest = func(cwd, prompt, flow string) (store.LiveRun, error) {
		gotFlow, gotPrompt = flow, prompt
		return store.LiveRun{RunID: "n3", BrokerURL: "http://d", Prompt: prompt, CWD: cwd, State: "active", Flow: flow,
			StartedAt: time.Now().Unix()}, nil
	}
	t.Cleanup(func() { startRequest = saved })

	m := flowModel(t)
	for i, r := range m.runs {
		if r.RunID == "x9" {
			m.runCur = i
		}
	}
	m = press(m, "enter", "esc") // x9's result, back to its run
	m = press(m, "n", "v", "e", "r", "i", "f", "y")
	if got := ansi.Strip(m.steerBar()); !strings.Contains(got, "» next step: verify") {
		t.Fatalf("compose line: %q", got)
	}
	m = pressRun(m, "enter")
	if gotFlow != "x9" || gotPrompt != "verify" {
		t.Fatalf("started %q in flow %q", gotPrompt, gotFlow)
	}
	f, err := store.LoadFlow("x9")
	if err != nil || f.Steps[0].RunID != "x9" || f.Goal != "audit TLS" {
		t.Fatalf("x9 was not adopted into a flow: %+v %v", f, err)
	}
	if m.live.RunID != "n3" {
		t.Errorf("the new step did not open: %q", m.live.RunID)
	}
}

// Notes are written in $EDITOR and kept on the flow.
func TestFlowNotesAreEditedInTheEditor(t *testing.T) {
	m := flowModel(t)
	m.openFlow("p1", "p1")
	fakeEditor(t, "drop the v1 constraint\n# ignored\n")
	cmd := m.openNotesEditor("p1")
	if cmd == nil || m.editorNotes != "p1" {
		t.Fatal("the notes editor did not open")
	}
	f, _ := store.LoadFlow("p1")
	path, err := prepareEditorFile(f.Notes, "")
	must(t, err)
	next, _ := m.Update(finishEditor(path, editorCommand(path).Run()))
	m = next.(tuiModel)
	if f, _ := store.LoadFlow("p1"); f.Notes != "drop the v1 constraint" {
		t.Errorf("notes %q", f.Notes)
	}
	if !strings.Contains(m.noticeLine(100), "notes saved") {
		t.Errorf("no notice: %q", m.noticeLine(100))
	}
}

// The result view's keys: e writes the report, digits open a PR, y copies.
func TestResultActions(t *testing.T) {
	m := flowModel(t)
	m.snap, _ = store.LoadRun("b2")
	m.live = store.LiveRun{RunID: "b2", Flow: "p1"}
	m.detail = detailResult
	_, cmd, ok := m.handleResultKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !ok || cmd == nil {
		t.Fatal("e did nothing")
	}
	msg := cmd().(noticeMsg)
	if msg.err != nil || !strings.HasSuffix(msg.text, "b2.md") {
		t.Errorf("export: %+v", msg)
	}
	if _, cmd, ok := m.handleResultKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}); !ok || cmd == nil {
		t.Error("1 did not open the first PR")
	}
	// 2 is the file, which is not on disk here: it says so.
	if _, cmd, _ := m.handleResultKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}); cmd == nil {
		t.Error("2 did nothing for the file")
	} else if msg, ok := cmd().(noticeMsg); !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "internal/api/mw.go") {
		t.Errorf("a missing file: %+v", msg)
	}
	if _, cmd, _ := m.handleResultKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}); cmd != nil {
		t.Error("3 opened an artifact that does not exist")
	}
}

// The timeline of a flow's later step starts with the step it continues,
// and enter on it opens that run.
func TestTheTimelineLinksToThePreviousStep(t *testing.T) {
	m := selectRun(t, flowModel(t), "p1")
	m = press(m, "enter", "j", "enter") // flow, step 2, its run
	m.snap, _ = store.LoadRun("b2")
	events := broker.Timeline(m.snap)
	if events[0].Kind != broker.TimelineFollows || events[0].Run != "p1" {
		t.Fatalf("first event: %+v", events[0])
	}
	m.focus, m.zoom, m.timelineSel = focusTimeline, focusTimeline, 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m = next.(tuiModel); m.live.RunID != "p1" {
		t.Errorf("enter on 'continues' went to %q", m.live.RunID)
	}
}

// selectRun puts the home cursor on a run.
func selectRun(t *testing.T, m tuiModel, id string) tuiModel {
	t.Helper()
	for i, r := range m.runs {
		if r.RunID == id {
			m.runCur = i
			return m
		}
	}
	t.Fatalf("no run %s", id)
	return m
}

// A long request reads in full: l moves into the step's detail, where j/k
// and pages scroll it to its end and no further, and the paragraphs and
// markdown it was written with survive.
func TestALongStepScrollsInTheFlowView(t *testing.T) {
	m := flowModel(t)
	long := "## Build the collector\n\n" + strings.Repeat("A paragraph of the request that runs on. ", 40) +
		"\n\n- first point\n- second point\n\nLAST LINE OF THE REQUEST"
	if _, err := store.UpdateFlow("p1", false, func(f *store.Flow) error {
		f.Steps[1].Request = long
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.applyFlows(store.ListFlows())
	m.width, m.height = 100, 24
	m.openFlow("p1", "b2")

	got := ansi.Strip(m.View())
	if strings.Contains(got, "LAST LINE OF THE REQUEST") {
		t.Fatal("setup: the request already fits")
	}
	if !strings.Contains(got, "Build the collector") || strings.Contains(got, "## Build") {
		t.Errorf("the request's markdown was not rendered:\n%s", got)
	}
	// j on the step list selects; it does not scroll.
	if m = press(m, "l"); !m.flowDetail {
		t.Fatal("l did not move into the detail")
	}
	m = press(m, "G")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "LAST LINE OF THE REQUEST") && !strings.Contains(got, "Artifacts") {
		t.Errorf("G did not reach the end:\n%s", got)
	}
	end := m.flowScroll
	m = press(m, "j", "j", "j")
	if m.flowScroll != end {
		t.Errorf("scrolled past the end: %d > %d", m.flowScroll, end)
	}
	if m = press(m, "k"); m.flowScroll != end-1 {
		t.Errorf("one k from the end is %d, want %d", m.flowScroll, end-1)
	}
	if m = press(m, "g"); m.flowScroll != 0 || m.flowSel != 1 {
		t.Errorf("g in the detail: scroll %d sel %d", m.flowScroll, m.flowSel)
	}
	// z widens; esc unwinds widened → detail → steps → home.
	m = press(m, "z")
	if got := ansi.Strip(m.View()); strings.Contains(got, "Goal") {
		t.Errorf("z did not give the detail the width:\n%s", got)
	}
	if m = press(m, "esc"); m.flowZoom || !m.flowDetail {
		t.Fatal("esc from widened")
	}
	if m = press(m, "esc"); m.flowDetail || !m.flowView {
		t.Fatal("esc from the detail")
	}
	if m = press(m, "esc"); !m.picker {
		t.Fatal("esc from the steps")
	}
}

// A file a worker recorded opens in $PAGER, numbered after the PRs.
func TestAFileArtifactOpensInThePager(t *testing.T) {
	m := flowModel(t)
	dir := t.TempDir()
	plan := dir + "/PLAN.md"
	must(t, os.WriteFile(plan, []byte("# the plan\n"), 0o644))
	seen := dir + "/seen"
	pager := dir + "/pager"
	must(t, os.WriteFile(pager, []byte("#!/bin/sh\ncp \"$1\" "+seen+"\n"), 0o755))
	t.Setenv("PAGER", pager)

	a := broker.Artifacts{PRs: []string{"https://github.com/o/r/pull/7"}, Files: []string{"PLAN.md"}}
	res := broker.RunResult{Artifacts: a}
	got := ansi.Strip(strings.Join(m.artifactLines(res, 100, true), "\n"))
	if !strings.Contains(got, "1 PR") || !strings.Contains(got, "2 file") {
		t.Fatalf("not numbered:\n%s", got)
	}
	cmd := openArtifact(a, 2, dir)
	if cmd == nil {
		t.Fatal("no command for the file")
	}
	// What tea.ExecProcess runs: the pager on the file, resolved against
	// the run's directory.
	must(t, exec.Command("sh", "-c", pager+` "$1"`, "x", plan).Run())
	if b, _ := os.ReadFile(seen); string(b) != "# the plan\n" {
		t.Errorf("pager saw %q", b)
	}
}
