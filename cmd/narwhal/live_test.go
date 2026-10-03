package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// liveModel is three siblings feeding a synthesis, two of them running.
func liveModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 140, 34
	m.focus = focusRadio
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "alpha-worker", State: broker.TaskDispatched, Dispatches: 1},
		{ID: "beta-worker", State: broker.TaskDispatched, Dispatches: 1},
		{ID: "gamma-worker", State: broker.TaskCompleted, Dispatches: 1},
		{ID: "synthesis", State: broker.TaskPending,
			Deps: []string{"alpha-worker", "beta-worker", "gamma-worker"}},
	}
	return m
}

func graphText(m tuiModel) string {
	return ansi.Strip(m.viewTasks(m.graphPaneWidth(), 30))
}

// The point of the change: a running box says what its worker is doing.
func TestRunningBoxShowsItsCurrentTool(t *testing.T) {
	m := liveModel(t)
	m.activity = map[string]agentLive{
		"alpha-worker": {tool: "Edit  internal/api.go", last: time.Now()},
	}
	got := graphText(m)
	if !strings.Contains(got, "Edit") {
		t.Errorf("running box does not show its tool:\n%s", got)
	}
	if !strings.Contains(got, "starting…") {
		t.Errorf("a running box with no transcript yet should say so:\n%s", got)
	}
}

// A finished box keeps one line. Its last tool is history, and a second row
// on every box would halve what fits on the pane.
func TestFinishedBoxStaysOneLine(t *testing.T) {
	m := liveModel(t)
	m.activity = map[string]agentLive{"gamma-worker": {tool: "Read  x.go"}}
	if got := m.liveDetail("gamma-worker"); got != "" {
		t.Errorf("completed task has a detail line: %q", got)
	}
}

// The detail line must never widen a box: on a narrow pane that reflowed
// running siblings onto two rows and broke vertical navigation.
func TestDetailDoesNotWidenTheBox(t *testing.T) {
	m := liveModel(t)
	before := m.boxRows(m.graphPaneWidth())
	m.activity = map[string]agentLive{
		"alpha-worker": {tool: "Bash  go test ./internal/very/long/package/path/..."},
	}
	after := m.boxRows(m.graphPaneWidth())
	span := func(rows []boxRow, node int) (int, int) {
		for _, r := range rows {
			if r.owns(node) {
				s := r.spanOf(node)
				return s.x0, s.x1
			}
		}
		return -1, -1
	}
	for node := range m.snap.Tasks {
		b0, b1 := span(before, node)
		a0, a1 := span(after, node)
		if b1-b0 != a1-a0 {
			t.Errorf("box %d changed width %d → %d", node, b1-b0, a1-a0)
		}
	}
}

func TestFitDetailKeepsTheToolWhenTheArgumentWillNotFit(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"Edit  a.go", 20, "Edit  a.go"},
		{"Edit  internal/handlers/metrics.go", 12, "Edit intern…"},
		{"Bash  go test ./...", 6, "Bash"},
	}
	for _, c := range cases {
		got := fitDetail(c.in, c.w)
		if displayWidth(got) > c.w {
			t.Errorf("fitDetail(%q, %d) = %q, wider than %d", c.in, c.w, got, c.w)
		}
		if c.want != "" && !strings.HasPrefix(got, strings.TrimSuffix(c.want, "…")[:4]) {
			t.Errorf("fitDetail(%q, %d) = %q, want it to keep the tool", c.in, c.w, got)
		}
	}
}

// Only the headline row spins. The detail row holds the tool, and a spinner
// over its first letter turned "Bash" into "⠙ash".
func TestSpinnerOnlyOnTheHeadline(t *testing.T) {
	m := liveModel(t)
	m.activity = map[string]agentLive{"alpha-worker": {tool: "Bash  make"}}
	got := graphText(m)
	if !strings.Contains(got, "Bash") {
		t.Errorf("the spinner overwrote the tool name:\n%s", got)
	}
	if !strings.Contains(got, spinnerAt(m.frame)+" alpha-worker") {
		t.Errorf("the running headline does not spin:\n%s", got)
	}
}

func TestSpinnerAdvancesWithTheFrame(t *testing.T) {
	m := liveModel(t)
	a := graphText(m)
	m.frame++
	b := graphText(m)
	if a == b {
		t.Error("advancing the frame changed nothing on a graph with running tasks")
	}
}

// Opening the monitor on a run with history must not replay it as pulses.
func TestFirstSnapshotDoesNotReplayHistory(t *testing.T) {
	m := liveModel(t)
	m.snap.Messages = []*broker.Message{
		{Seq: 1, Sender: "worker-alpha-worker", Content: "@beta-worker heads up"},
	}
	m.absorbLive(nil, time.Now())
	if len(m.pulses) != 0 {
		t.Errorf("history fired %d pulses", len(m.pulses))
	}
}

func TestNewMessageBecomesAPulseToEveryoneItNames(t *testing.T) {
	m := liveModel(t)
	now := time.Now()
	m.absorbLive(nil, now)
	m.snap.Messages = append(m.snap.Messages, &broker.Message{
		Seq: 1, Sender: "worker-alpha-worker",
		Content: "@worker-beta-worker and @gamma-worker: schema changed",
	})
	m.absorbLive(nil, now)
	if len(m.pulses) != 2 {
		t.Fatalf("pulses = %+v, want one to each named peer", m.pulses)
	}
	if !m.isHot("alpha-worker") {
		t.Error("the sender did not light up")
	}
}

// Most messages name nobody. They flash their sender rather than vanish.
func TestBroadcastFlashesTheSender(t *testing.T) {
	m := liveModel(t)
	now := time.Now()
	m.absorbLive(nil, now)
	m.snap.Messages = append(m.snap.Messages, &broker.Message{
		Seq: 1, Sender: "worker-gamma-worker", Content: "finished the audit",
	})
	m.absorbLive(nil, now)
	if len(m.pulses) != 0 {
		t.Errorf("a broadcast became a pulse: %+v", m.pulses)
	}
	if !m.isHot("gamma-worker") {
		t.Error("a broadcast did not flash its sender")
	}
}

// The operator is not a box. Its message lights whoever it names.
func TestOperatorMessageFlashesItsAddressees(t *testing.T) {
	m := liveModel(t)
	now := time.Now()
	m.absorbLive(nil, now)
	m.snap.Messages = append(m.snap.Messages, &broker.Message{
		Seq: 1, Sender: "main", Content: "@worker-beta-worker approved",
	})
	m.absorbLive(nil, now)
	if !m.isHot("beta-worker") || len(m.pulses) != 0 {
		t.Errorf("operator message: hot=%v pulses=%+v", m.isHot("beta-worker"), m.pulses)
	}
}

func TestMentionInTheBodyCountsAndUnknownNamesDoNot(t *testing.T) {
	tasks := map[string]bool{"a": true, "b": true}
	msg := &broker.Message{Content: "@worker-b look; also @someone-else and @a", Mentions: []string{"b"}}
	got := recipients(msg, "a", tasks)
	if len(got) != 1 || got[0] != "b" {
		t.Errorf("recipients = %v, want [b] (deduplicated, no sender, no strangers)", got)
	}
}

func TestPulseTravelsAndNeverDrawsOverABox(t *testing.T) {
	m := liveModel(t)
	now := time.Now()
	m.pulses = []pulse{{from: "alpha-worker", to: "synthesis", born: now}}

	var seen []string
	for _, dt := range []time.Duration{100, 600, 1100} {
		m.now = now.Add(dt * time.Millisecond)
		rows := m.boxRows(m.graphPaneWidth())
		centers := boxCenters(rows, layoutGraph(m.sortedTasks()).nodes)
		marks := overlayPulses(rows, centers, m.pulses, m.now)
		var heads []string
		for y, cols := range marks {
			for x, mk := range cols {
				if !mk.head {
					continue
				}
				for _, s := range rows[y].spans {
					if x >= s.x0 && x < s.x1 {
						t.Fatalf("pulse drawn inside a box at (%d,%d)", x, y)
					}
				}
				heads = append(heads, string(rune('0'+y%10))+":"+string(rune('0'+x%10)))
			}
		}
		if len(heads) != 1 {
			t.Fatalf("t+%v: %d pulse heads, want 1", dt, len(heads))
		}
		seen = append(seen, heads[0])
	}
	if seen[0] == seen[1] && seen[1] == seen[2] {
		t.Errorf("the pulse did not move: %v", seen)
	}
}

func TestArrivedPulsesArePruned(t *testing.T) {
	now := time.Now()
	ps := []pulse{{born: now.Add(-2 * pulseDuration)}, {born: now}}
	if got := prunePulses(ps, now); len(got) != 1 {
		t.Errorf("prunePulses kept %d, want 1", len(got))
	}
}

// An idle run must not keep a 10Hz timer running.
func TestAnimationStopsWhenNothingMoves(t *testing.T) {
	m := liveModel(t)
	for i := range m.snap.Tasks {
		m.snap.Tasks[i].State = broker.TaskCompleted
	}
	m.animated = true
	next, cmd := m.Update(animMsg(time.Now()))
	if cmd != nil {
		t.Error("an idle run scheduled another animation frame")
	}
	if next.(tuiModel).animated {
		t.Error("animated stayed set with nothing moving")
	}
}

func TestAnimationRunsWhileWorkersRun(t *testing.T) {
	m := liveModel(t)
	m.animated = true
	_, cmd := m.Update(animMsg(time.Now()))
	if cmd == nil {
		t.Error("running workers did not keep the animation going")
	}
	var _ tea.Cmd = cmd
}

func TestSwitchingRunsForgetsMotion(t *testing.T) {
	m := liveModel(t)
	m.pulses = []pulse{{from: "a", to: "b", born: time.Now()}}
	m.lastSeq, m.seenChannel = 9, true
	m.resetLive()
	if m.pulses != nil || m.lastSeq != 0 || m.seenChannel {
		t.Errorf("state survived a run switch: %+v %d %v", m.pulses, m.lastSeq, m.seenChannel)
	}
}

func TestSummarizeLiveFindsTheLatestToolAndBucketsActivity(t *testing.T) {
	now := time.Now()
	entries := []transcriptEntry{
		{at: now.Add(-30 * time.Second), kind: "tool", text: "Read  old.go"},
		{at: now.Add(-3 * time.Second), kind: "tool", text: "Edit  new.go"},
		{at: now.Add(-1 * time.Second), kind: "result", text: "ok"},
	}
	a := summarizeLive(entries, now)
	if a.tool != "Edit  new.go" {
		t.Errorf("tool = %q, want the latest", a.tool)
	}
	if !a.last.Equal(now.Add(-1 * time.Second)) {
		t.Errorf("last = %v", a.last)
	}
	total := 0
	for _, c := range a.spark {
		total += c
	}
	if total != 2 {
		t.Errorf("spark counts %d entries in the window, want 2", total)
	}
}

// The keys to open a worker belong on the node you are looking at.
func TestNodeHeadlineOffersTheSessionKeys(t *testing.T) {
	m := liveModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	m.live.RunID = "r1"
	dir := home + "/.narwhal/sessions/r1/agents/worker-alpha-worker"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/claude-session-id", []byte("sid-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.activity = map[string]agentLive{"alpha-worker": {spark: []int{0, 1, 3, 1}}}

	got := ansi.Strip(m.inspectorHeadline(m.taskByID("alpha-worker"), 100))
	if !strings.Contains(got, "s session · a attach") {
		t.Errorf("headline does not offer the session keys: %q", got)
	}
	if !strings.ContainsAny(got, "▁▃█") {
		t.Errorf("headline has no activity sparkline: %q", got)
	}
	if narrow := ansi.Strip(m.inspectorHeadline(m.taskByID("alpha-worker"), 30)); strings.Contains(narrow, "attach") {
		t.Errorf("hint overflowed a narrow pane: %q", narrow)
	}
}

func TestSparklineScalesToItsOwnPeak(t *testing.T) {
	if got := sparkline([]int{0, 1, 2, 4}); got != " ▂▄█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline([]int{0, 0}); got != "  " {
		t.Errorf("an idle sparkline = %q, want blanks", got)
	}
}
