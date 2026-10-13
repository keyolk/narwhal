package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		"Steps", "1 " + icons.runDone + " plan the lock API", "2 " + icons.runDone + " build it", "Step 2", "asked  you        build it",
		"file   api", "mw.go", "PR     api", "o/r#7", "commit", "abc1234", "answer", "2 steps · 4 artifacts"} {
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
	// The watched step by its request; the others by number and progress.
	if !strings.Contains(got, "[1 "+icons.runDone+" plan the lock API 1/1]") || !strings.Contains(got, "2 "+icons.runDone+" 1/1") ||
		strings.Contains(got, "build it") {
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
	m.focus, m.zoom = focusTimeline, focusTimeline
	for i, it := range m.timelineItems() {
		if it.scene != nil && it.scene.Kind == broker.SceneRun {
			m.timelineSel = i
		}
	}
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

// A long request reads in full: l moves into the step's outputs, the
// request leads them, and J/K and pages scroll it to its end and no
// further, with its paragraphs and markdown.
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

	if m = press(m, "l"); !m.flowDetail {
		t.Fatal("l did not move into the detail")
	}
	m = press(m, "g")
	for m.stepOutputs()[m.stepOutputSel(m.stepOutputs())].kind != outRequest {
		m = press(m, "k")
	}
	got := ansi.Strip(m.View())
	if strings.Contains(got, "LAST LINE OF THE REQUEST") {
		t.Fatal("setup: the request already fits")
	}
	if !strings.Contains(got, "Build the collector") || strings.Contains(got, "## Build") {
		t.Errorf("the request's markdown was not rendered:\n%s", got)
	}
	m = press(m, "G")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "LAST LINE OF THE REQUEST") {
		t.Errorf("G did not reach the end:\n%s", got)
	}
	end := m.flowScroll
	m = press(m, "J", "J", "J")
	if m.flowScroll != end {
		t.Errorf("scrolled past the end: %d > %d", m.flowScroll, end)
	}
	if m = press(m, "K"); m.flowScroll >= end {
		t.Errorf("K from the end did not scroll back: %d", m.flowScroll)
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

// i on a flow step opens a line to that step's run; unaddressed, the
// instruction resumes the task whose outcome is the step's answer.
func TestResumingAFlowStepFromTheFlowView(t *testing.T) {
	m := flowModel(t)
	var got steerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"seq":3,"reopened":["synthesis"]}`))
	}))
	defer srv.Close()
	saved := daemonURL
	daemonURL = func() (string, error) { return srv.URL, nil }
	t.Cleanup(func() { daemonURL = saved })

	for i, r := range m.runs {
		if r.RunID == "p1" {
			m.runCur = i
		}
	}
	m = press(m, "enter")
	if !m.flowView || m.flowSel != 0 {
		t.Fatalf("flow view %v, step %d", m.flowView, m.flowSel)
	}
	m = press(m, "i")
	if !m.steering {
		t.Fatal("i in the flow view did not open the line")
	}
	if foot := ansi.Strip(m.View()); !strings.Contains(foot, "step 1 → synthesis »") {
		t.Errorf("the line does not say where it goes:\n%s", foot)
	}
	m = typeKeys(m, "add a rollback section")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("the instruction was not sent")
	}
	next, _ = m.Update(cmd())
	m = next.(tuiModel)
	if got.RunID != "p1" || len(got.Mentions) != 1 || got.Mentions[0] != "worker-synthesis" {
		t.Errorf("sent %+v, want step 1's run with worker-synthesis", got)
	}
	if !m.flowView || m.steerStep != nil {
		t.Errorf("after sending: flowView=%v steerStep=%v", m.flowView, m.steerStep)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "reopened synthesis") {
		t.Errorf("the flow view does not say the step was resumed:\n%s", view)
	}
}

// n still plans a new step; i is not it.
func TestNStillStartsANextStepInTheFlowView(t *testing.T) {
	m := flowModel(t)
	m = press(m, "enter", "n")
	if !m.composing || m.steering {
		t.Errorf("n: composing=%v steering=%v", m.composing, m.steering)
	}
}

// In the flow view a step's outputs are a list: l moves into it, j/k
// choose, and the chosen one is read below the list, each kind its own way.
func TestAFlowStepsOutputsAreChosenFromAList(t *testing.T) {
	m := flowModel(t)
	m.openFlow("p1", "b2")
	// Before choosing, the step's answer is shown.
	outs := m.stepOutputs()
	if got := outs[m.stepOutputSel(outs)].kind; got != outAnswer {
		t.Fatalf("shown first: %s, want the answer", outputLabels[got])
	}
	if !strings.Contains(ansi.Strip(m.View()), "done: https://github.com/o/r/pull/7") {
		t.Errorf("the answer is not shown:\n%s", ansi.Strip(m.View()))
	}
	m = press(m, "l", "g")
	for outs[m.stepOutputSel(outs)].kind != outPR {
		m = press(m, "k")
	}
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "▸ PR") || !strings.Contains(got, "PR · https://github.com/o/r/pull/7") {
		t.Errorf("k did not show the PR:\n%s", got)
	}
	// j on the list does not change the step.
	if m.flowSel != 1 {
		t.Errorf("choosing an output moved the step to %d", m.flowSel)
	}
	// h goes back to the steps; j then moves the step and shows its answer.
	m = press(m, "h", "k")
	if m.flowSel != 0 || m.flowOutSel != -1 {
		t.Errorf("after h k: step %d, output %d", m.flowSel, m.flowOutSel)
	}
}

// d asks, a second d removes: the step leaves the flow and is listed
// nowhere, while its run stays on disk. Any other key keeps it.
func TestDRemovesAStepOnASecondPress(t *testing.T) {
	m := flowModel(t)
	m.openFlow("p1", "b2")
	press := func(m tuiModel, k string) tuiModel {
		next, cmd := m.handleFlowKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		nm := next.(tuiModel)
		if cmd != nil {
			if msg, ok := cmd().(flowsMsg); ok {
				nm.applyFlows(msg)
				nm.mergeRuns(nm.runs)
			}
		}
		return nm
	}
	m = press(m, "d")
	if f, _ := store.LoadFlow("p1"); len(f.Steps) != 2 {
		t.Fatal("one d removed the step")
	}
	m = press(m, "j") // another key keeps it
	m = press(m, "d")
	m = press(m, "d")
	f, err := store.LoadFlow("p1")
	if err != nil || len(f.Steps) != 1 || f.Steps[0].RunID != "p1" {
		t.Fatalf("after d d: %+v %v", f, err)
	}
	if _, err := store.LoadRun("b2"); err != nil {
		t.Errorf("the run's record went with it: %v", err)
	}
	for _, r := range m.runs {
		if r.RunID == "b2" {
			t.Error("the removed step is still listed")
		}
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "removed step 2: build it") ||
		strings.Count(got, "build it") != 1 {
		t.Errorf("the flow view still shows the removed step:\n%s", got)
	}
}

func TestFlowCommandRemovesAStep(t *testing.T) {
	flowModel(t)
	var out strings.Builder
	if err := runFlowCmd(&out, []string{"rm", "p1", "2"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	runFlowCmd(&out, []string{"show", "p1"})
	if strings.Contains(out.String(), "b2  build it") || !strings.Contains(out.String(), "removed: [b2]") {
		t.Errorf("show:\n%s", out.String())
	}
}

// d d on home takes the selected run off the list — a step out of its
// flow, a lone run off on its own — and other keys keep it.
func TestHomeRemovesARunOnASecondD(t *testing.T) {
	m := flowModel(t)
	press := func(m tuiModel, k string) tuiModel {
		next, cmd := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		nm := next.(tuiModel)
		if cmd != nil {
			if msg, ok := cmd().(flowsMsg); ok {
				nm.applyFlows(msg)
				nm.mergeRuns(nm.runs)
			}
		}
		return nm
	}
	sel := func(m tuiModel, id string) tuiModel {
		for i, r := range m.runs {
			if r.RunID == id {
				m.runCur = i
			}
		}
		return m
	}
	m = press(sel(m, "x9"), "d")
	if !strings.Contains(ansi.Strip(m.View()), "d again to remove") {
		t.Errorf("no confirmation on home:\n%s", ansi.Strip(m.View()))
	}
	m = press(m, "k")
	m = press(sel(m, "x9"), "d")
	m = press(m, "d")
	m = press(sel(m, "b2"), "d")
	m = press(m, "d")
	for _, r := range m.runs {
		if r.RunID == "x9" || r.RunID == "b2" {
			t.Errorf("%s is still listed", r.RunID)
		}
	}
	got := ansi.Strip(m.View())
	if strings.Contains(got, "audit TLS") || strings.Contains(got, "plan the lock API  2 steps") ||
		!strings.Contains(got, "plan the lock API") {
		t.Errorf("home after removing:\n%s", got)
	}
	// Survives a fresh start: the removal is on disk.
	m2 := newTUIModel(store.Discover(nil), 0, 0, true)
	m2.applyFlows(store.ListFlowFiles())
	m2.mergeRuns(m2.runs)
	for _, r := range m2.runs {
		if r.RunID == "x9" || r.RunID == "b2" {
			t.Errorf("%s came back after a restart", r.RunID)
		}
	}
}

// A step dropped from the flow file without a Removed record — taken out
// by an older narwhal, or by hand — still claims the flow by its own
// record. It must not reappear at the bottom of the steps.
func TestAStepDroppedFromTheFileStaysOut(t *testing.T) {
	m := flowModel(t)
	if _, err := store.UpdateFlow("p1", false, func(f *store.Flow) error {
		f.Steps = f.Steps[1:] // drop p1, the earlier step, and record nothing
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.applyFlows(store.ListFlowFiles())
	for _, r := range m.flowRuns("p1") {
		if r.RunID == "p1" {
			t.Error("the dropped step is listed again")
		}
	}
	// Nor on home, where it was drawn as step 1.
	m.mergeRuns(m.runs)
	for _, r := range m.runs {
		if r.RunID == "p1" {
			t.Error("the dropped step is on home")
		}
	}
}
