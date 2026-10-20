// monitor_tui.go implements the interactive monitor: a Bubble Tea TUI over
// a live run's DAG and radio traffic.
//
// The first monitor was a polling printer — clear screen, redraw, sleep. It
// showed the right data but you could not do anything with it: messages were
// truncated with no way to expand them, only the last dozen were visible,
// and there was no scrolling. Since the interesting content is exactly the
// long radio messages workers write to each other, that made the monitor
// nearly useless for the thing it exists to show.
//
// Layout: a task list on the left, radio traffic on the right, and a detail
// pane that opens over the radio pane when you select a message. Tab moves
// between panes, j/k moves within one, and Enter opens the selection —
// matching the ccproxy monitor TUI so muscle memory carries over.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/async"
	"github.com/keyolk/tuikit/input"
	"github.com/keyolk/tuikit/render"

	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/tmux"
	"github.com/keyolk/tuikit/toast"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// focusPane is which pane the keys go to.
//
// The node pane used to have no focus of its own — it followed the graph
// cursor and could not be scrolled, so a worker's activity was whatever
// three lines happened to fit. It is now a pane like the others, which is
// what lets it hold something worth reading.
type focusPane int

const (
	focusTasks focusPane = iota
	focusNode
	focusRadio
	// focusTimeline is the run's history under the graph. It is a pane
	// like the others — a number, a focus, a scroll — rather than a view
	// swapped into the radio's slot, where the same history moved across
	// the screen depending on which key was pressed last.
	focusTimeline
)

// panes is the cycle order, which is the order of the numbers in the pane
// titles: reading order, band by band — graph and node, then timeline and
// radio. See layout.go.
var panes = []focusPane{focusTasks, focusNode, focusTimeline, focusRadio}

// next and prev step through panes by position in the cycle, not by the
// constant's value: the cycle follows the screen, the constants do not.
func (f focusPane) next() focusPane { return panes[(f.slot()+1)%len(panes)] }
func (f focusPane) prev() focusPane { return panes[(f.slot()+len(panes)-1)%len(panes)] }

func (f focusPane) slot() int {
	for i, p := range panes {
		if p == f {
			return i
		}
	}
	return 0
}

// detailMode says what the detail pane is showing, or that it is closed.
type detailMode int

const (
	detailClosed detailMode = iota
	detailMessage
	// detailSession shows a worker's full Claude session output. The task
	// detail summarizes; this is where you actually watch a worker work.
	detailSession
	// detailResult is the run's result: answer, artifacts, tasks.
	detailResult
)

// tickMsg drives the poll loop.
type tickMsg time.Time

// snapshotMsg carries a fresh poll result.
//
// snap and err are not exclusive. When the broker is unreachable the poll
// falls back to the snapshot on disk, and both are set: the snapshot is
// real and worth showing, and the error is what tells the header to say the
// broker is gone. Dropping either one loses something the operator needs —
// the tasks, or the fact that they are no longer live.
type snapshotMsg struct {
	snap   broker.Snapshot
	agents []string
	err    error
	// activity is what each worker is doing, read alongside the snapshot
	// so the transcripts are never touched on the UI goroutine.
	activity map[string]agentLive
	// runID is the run the poll was issued for, stamped when the command
	// was built rather than read off snap: an error-only result has no
	// snapshot to say which run it belongs to, and a poll still in flight
	// when the user switches runs must not land on the new one.
	runID string
	// seq orders polls. See tuiModel.pollSeq.
	seq uint64
}

// runsMsg carries a refreshed list of live runs.
type runsMsg []store.LiveRun

type tuiModel struct {
	live     store.LiveRun
	interval time.Duration
	client   *http.Client

	// runs is every live run, so the user can switch without restarting
	// the monitor. An interactive session creates a run per request, so
	// several are live at once routinely.
	runs   []store.LiveRun
	runCur int
	picker bool // showing the run list instead of a run

	snap   broker.Snapshot
	agents []string
	err    error

	focus    focusPane
	taskCur  int
	radioCur int
	// followTail keeps the radio list pinned to the newest message. Any
	// manual navigation releases it, so reading an older message is not
	// yanked away by the next poll.
	followTail   bool
	detail       detailMode
	detailScroll int
	// sessionTail keeps the session view pinned to the newest output while
	// the worker is still writing. Scrolling up releases it, the same way
	// the radio list works — reading back through what a worker did should
	// not be yanked away by its next line.
	sessionTail bool
	// boxMode draws tasks as connected boxes instead of a git-style lane
	// gutter. Boxes read better as a diagram; lanes fit more on screen.
	boxMode bool

	// zoom is the pane filling the whole body, or -1 for the normal split.
	// Reading a long assignment or a busy channel means wanting one pane
	// to be the screen for a moment, and going through a separate detail
	// view to get it loses the place you were in.
	zoom focusPane
	// widthDelta is the user's adjustment to the column split. Kept as a
	// delta rather than an absolute size so the layout still adapts when
	// the terminal is resized.
	widthDelta int
	// reader is the pane the right column showed last; it stays while the
	// graph has the keys. See readerPane.
	reader focusPane
	// nodeScroll is how far the node pane has been scrolled through the
	// worker's activity, or nodeScrollTail to stay on the newest.
	nodeScroll int

	// activity is what each worker is doing, gathered on the poll; pulses
	// are radio messages in flight between boxes; flash is when each task
	// last sent or received a message. See live.go.
	activity map[string]agentLive
	pulses   []pulse
	flash    map[string]time.Time
	lastSeq  int64
	// seenChannel is false until the first snapshot of a run has been
	// absorbed, so history is not replayed as pulses.
	seenChannel bool
	// seenVerdicts is how many verdicts have been absorbed, so a worker's
	// new question can light its box.
	seenVerdicts int
	frame        int
	now          time.Time
	animated     bool // an animation tick is scheduled

	// pollSeq numbers every poll issued, polling says the newest one has
	// not answered yet, and seqFloor is the oldest poll whose result may
	// still be applied. The tick fires every second and a poll can take up
	// to the client's five-second timeout, so without these polls piled up
	// and answered out of order: an older snapshot landing after a newer
	// one moved the cursor and the radio back in time.
	pollSeq  uint64
	polling  bool
	seqFloor uint64

	// briefTab says pane 4 shows the operator's brief instead of the radio.
	// The two are tabs of one pane: both are the whole run's channel, one
	// as it was said and one as it stands. 4 on a focused pane 4 switches.
	briefTab bool
	// help shows the key reference over the dimmed screen, scrolled to
	// helpOff; palette is the action finder, with what has been typed and
	// the selected match. See overlays.go.
	help         bool
	helpOff      int
	palette      bool
	paletteQuery input.Field
	paletteSel   int

	// km is the binding table (keymap.go) and toasts the action results
	// (toasts.go); Update returns the expiry ticks of toasts added while
	// handling a message.
	km     keys.Keymap
	toasts toast.Queue
	// saver writes the UI state worth keeping (uistate.go). Only the real
	// monitor builds one; the zero Saver in tests and pinned panes writes
	// nothing.
	saver prefs.Saver[uiState]

	// The home screen's request line. See home.go. homeCWD is where a new
	// request runs, composing whether the line has the keys, starting the
	// prompt being sent while the daemon answers, and homeErr why the last
	// one did not start.
	homeCWD    string
	composing  bool
	composeBuf []rune
	starting   string
	homeErr    error
	// composeFlow is the flow the line continues, or "" for a new one.
	composeFlow string
	// editorNotes says the open editor is for a flow's notes, not a
	// request. See editor.go.
	editorNotes string

	// The flow layer. See flowview.go. flows are read from disk, runFlow
	// maps a run to the flow file listing it, and flowView with flowID,
	// flowSel and flowScroll is the flow screen.
	flows   map[string]store.Flow
	runFlow map[string]string
	// removedRuns are steps taken out of their flows; nothing lists them.
	removedRuns map[string]bool
	// removeStep is the step a d asked to remove, waiting for the second
	// d that confirms it.
	removeStep string
	flowView   bool
	flowID     string
	flowSel    int
	flowScroll int
	// flowOutSel is the output of the selected step being read in the
	// flow view; -1 is its answer, until one is chosen.
	flowOutSel int
	// The task view's cursor and what has been opened in it. taskSel is
	// an entry, -1 following the newest. See taskview.go.
	taskSel     int
	taskToggled map[string]bool
	taskOpenAll bool
	// taskTab is the open tab, taskTabAuto to pick by state, and
	// summaryAskOpen unfolds a long request on Summary.
	taskTab        int
	summaryAskOpen bool
	// questionsView shows what the run's workers asked; questionSel is the
	// question selected, answering whether an answer is being written into
	// answerBuf, and pendingAnswers what is written and not yet sent. See
	// questions.go.
	questionsView  bool
	questionSel    int
	answering      bool
	answerBuf      input.Field
	pendingAnswers map[int]pendingAnswer

	// outputsView shows the watched run's outputs; outputSel and
	// outputScroll are its list and reading positions. See outputs.go.
	outputsView  bool
	outputSel    int
	outputScroll int
	// outputsRuns are the flow steps' runs when Outputs was opened from a
	// flow, every step listed; nil for the watched run alone.
	outputsRuns []string
	// outputDetails is what gh and git said about each PR, branch and
	// commit listed, by output key.
	outputDetails map[string]outputDetail

	// pinnedTask is the one task a pane opened by w shows; leaving its
	// view quits. See tmuxpanes.go.
	pinnedTask string
	// tm is the tmux server w opens panes in; tmuxGen numbers each press
	// of w so only the latest one's outcome is shown.
	tm      tmux.Client
	tmuxGen async.Gen

	// flowDetail puts j/k on the step's detail rather than the step list,
	// and flowZoom gives the detail the full width.
	flowDetail bool
	flowZoom   bool
	snapCache  map[string]cachedSnap

	// prState is each PR's state as gh reported it. See resultview.go.
	prState map[string]string

	// timelineSel is the selected timeline event, or timelineFollow to
	// keep the newest selected as the run goes on.
	timelineSel int
	// sceneOpen is the timeline's scenes opened into their events, by key.
	sceneOpen map[string]bool

	// steering is the open command line and steerBuf what has been typed
	// into it. See steer.go; what became of a send is a toast.
	steering bool
	steerBuf input.Field
	// steerStep is the flow step the open line sends to, when it was
	// opened from the flow view; nil for the watched run.
	steerStep *steerTarget

	// clicks tells a second click on the selected row from two single
	// ones; mouseNow stands in for the clock in tests. See mouse.go.
	clicks   mouse.Clicks
	mouseNow func() time.Time

	width  int
	height int
	quit   bool
}

// newTUIModel builds the model. runs is every live run and cur indexes the
// one to open; the picker is shown when the caller did not single one out.
func newTUIModel(runs []store.LiveRun, cur int, interval time.Duration, picker bool) tuiModel {
	m := tuiModel{
		interval:    interval,
		client:      &http.Client{Timeout: 5 * time.Second},
		tm:          tmux.New(nil),
		focus:       focusRadio,
		timelineSel: timelineFollow,
		taskSel:     -1,
		flowOutSel:  -1,
		taskTab:     taskTabAuto,
		homeCWD:     launchDir(),
		snapCache:   map[string]cachedSnap{},
		prState:     map[string]string{},
		zoom:        -1,
		followTail:  true,
		nodeScroll:  nodeScrollTail,
		boxMode:     true,
		runs:        runs,
		runCur:      cur,
		picker:      picker,
		width:       100,
		height:      30,
		// Init's poll is issued from a copy of the model, so reserve its
		// number here; otherwise the first tick would see no poll in
		// flight and issue a second one under the same number.
		pollSeq: 1,
		polling: true,
	}
	if cur >= 0 && cur < len(runs) {
		m.live = runs[cur]
	}
	// Order the initial list the same way a poll would, so the first frame
	// matches every frame after it.
	m.mergeRuns(runs)
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.pollCmd(m.pollSeq), m.refreshRuns(), refreshFlows(), tick(m.interval))
}

// refreshRuns re-discovers live runs so a run started after the monitor
// opened still shows up, and a finished one drops out of the list.
func (m tuiModel) refreshRuns() tea.Cmd {
	return func() tea.Msg {
		return runsMsg(store.Discover(daemonRunLister))
	}
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// poll issues a fetch of the watched run under a new sequence number and
// marks it in flight. Callers inside a value-receiver method must take the
// command before returning m, so the copy returned carries the new number.
func (m *tuiModel) poll() tea.Cmd {
	m.pollSeq++
	m.polling = true
	return m.pollCmd(m.pollSeq)
}

// pollCmd builds the fetch for poll number seq.
func (m tuiModel) pollCmd(seq uint64) tea.Cmd {
	// A finished run has no broker to ask. Discover marks those with an
	// empty BrokerURL and the snapshot on disk is the whole record, so
	// read it instead of failing every second against a dead port.
	if m.live.BrokerURL == "" {
		runID := m.live.RunID
		return m.withActivity(seq, func() tea.Msg {
			snap, err := store.LoadRun(runID)
			if err != nil {
				return snapshotMsg{err: fmt.Errorf("read saved run: %w", err)}
			}
			return snapshotMsg{snap: snap}
		})
	}

	return m.withActivity(seq, m.fetchSnapshot())
}

// withActivity wraps a snapshot fetch so each result also carries what the
// workers are doing. Both run in the same background command.
func (m tuiModel) withActivity(seq uint64, fetch tea.Cmd) tea.Cmd {
	runID := m.live.RunID
	return func() tea.Msg {
		msg, ok := fetch().(snapshotMsg)
		if !ok {
			return msg
		}
		msg.runID = runID
		msg.seq = seq
		if msg.snap.RunID == "" {
			return msg
		}
		msg.activity = m.gatherLive(msg.snap, time.Now())
		return msg
	}
}

// fetchSnapshot reads the watched run from its broker, falling back to the
// copy on disk.
func (m tuiModel) fetchSnapshot() tea.Cmd {
	url := m.live.BrokerURL + "/api/v1/monitor/" + m.live.RunID
	runID := m.live.RunID
	client := m.client
	return func() tea.Msg {
		// An unreachable broker is not the end of the record. The daemon
		// exits, is restarted, or the run is retired out of its memory, and
		// the monitor was left with an empty snapshot and an error — no
		// tasks to select, so attaching to a worker did nothing at all,
		// which read as attach being broken. The snapshot on disk outlives
		// the process that made it, so fall back to it and keep the error
		// so the header still says the run is no longer live.
		fallback := func(cause error) tea.Msg {
			snap, err := store.LoadRun(runID)
			if err != nil {
				return snapshotMsg{err: cause}
			}
			return snapshotMsg{snap: snap, err: cause}
		}
		resp, err := client.Get(url)
		if err != nil {
			return fallback(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fallback(fmt.Errorf("broker returned %d", resp.StatusCode))
		}
		var payload struct {
			Snapshot broker.Snapshot `json:"snapshot"`
			Agents   []string        `json:"agents"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			// A daemon that dies mid-response truncates the body, which is
			// the same event as the connection failing — one tick earlier.
			return fallback(err)
		}
		return snapshotMsg{snap: payload.Snapshot, agents: payload.Agents}
	}
}

// mergeRuns takes a refreshed run list while keeping the cursor on the run
// the user is watching. Runs come and go on their own — an interactive
// session starts one per request — so a naive replace would move the
// selection out from under them.
func (m *tuiModel) mergeRuns(runs []store.LiveRun) {
	// What the cursor should follow depends on the mode. Inside a run the
	// cursor tracks the run being watched. In the picker the user is moving
	// the cursor themselves and has not opened anything yet, so following
	// m.live would drag the highlight back to the watched run on every
	// poll — the cursor would refuse to stay where it was put.
	anchor := m.live.RunID
	if m.picker && m.runCur >= 0 && m.runCur < len(m.runs) {
		anchor = m.runs[m.runCur].RunID
	}

	// Order the list here rather than trusting the source. The daemon holds
	// its runs in a map, and Go randomizes map iteration, so an unsorted
	// list reshuffles on every poll — the picker flickers and rows move out
	// from under the cursor.
	//
	// Grouped first, as the home screen shows them — in progress, then
	// abandoned, then history — so the cursor index and the rows on screen
	// are the same order. Newest first within a group.
	//
	// A flow's runs stay together, in step order, placed by the flow's
	// latest step: a flow whose last step is running is in progress
	// however long ago it started.
	// A step taken out of its flow is not listed: its record is kept, but
	// showing it again on its own would undo what the person asked for.
	//
	// So is one its flow's file leaves out (flowRuns): listed on its own it
	// was drawn as step 1 of a flow that does not have it.
	sorted := make([]store.LiveRun, 0, len(runs))
	for _, r := range runs {
		if !m.removedRuns[r.RunID] {
			sorted = append(sorted, r)
		}
	}
	m.runs = sorted
	kept := sorted[:0:0]
	for _, r := range sorted {
		id := m.flowOf(r)
		if _, ok := m.flows[id]; !ok || containsRun(m.flowRuns(id), r.RunID) {
			kept = append(kept, r)
		}
	}
	sorted = kept
	type flowKey struct {
		group  int
		latest time.Time
	}
	keys := map[string]flowKey{}
	for _, r := range sorted {
		id := m.flowOf(r)
		k, seen := keys[id]
		if t := runStartTime(r); !seen || t.After(k.latest) {
			keys[id] = flowKey{runGroup(r), t}
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		fi, fj := m.flowOf(sorted[i]), m.flowOf(sorted[j])
		ki, kj := keys[fi], keys[fj]
		if fi != fj {
			if ki.group != kj.group {
				return ki.group < kj.group
			}
			if !ki.latest.Equal(kj.latest) {
				return ki.latest.After(kj.latest)
			}
			return fi > fj
		}
		// Within a flow, step order.
		a, b := runStartTime(sorted[i]), runStartTime(sorted[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return sorted[i].RunID < sorted[j].RunID
	})
	m.runs = sorted

	// Take the refreshed entry for the run being watched. m.live was only
	// ever set when a run was opened, so a run whose broker went away — the
	// daemon exited, or retired the run out of its memory — kept its dead
	// URL forever: every poll failed, the snapshot stayed empty, and with
	// no tasks on screen there was nothing to attach to. Rediscovery
	// already knows the run is now served from disk; this is what tells the
	// model. Independent of the cursor: the watched run is watched whether
	// or not the picker is open over it.
	for _, r := range m.runs {
		if r.RunID == m.live.RunID {
			m.live = r
			break
		}
	}

	for i, r := range m.runs {
		if r.RunID == anchor {
			m.runCur = i
			return
		}
	}
	// The anchored run ended. Stay put rather than jumping elsewhere: its
	// final state is still worth reading, and the broker keeps answering
	// until the daemon drops it.
	if m.runCur >= len(m.runs) {
		m.runCur = len(m.runs) - 1
	}
	if m.runCur < 0 {
		m.runCur = 0
	}
}

// selectRun switches the view to another run, clearing per-run state so
// the new run does not inherit the previous one's cursors or message
// history.
func (m *tuiModel) selectRun(i int) tea.Cmd {
	if i < 0 || i >= len(m.runs) {
		return nil
	}
	m.runCur = i
	m.live = m.runs[i]
	m.snap = broker.Snapshot{}
	m.agents = nil
	m.err = nil
	m.taskCur, m.radioCur = 0, 0
	m.followTail = true
	m.detail = detailClosed
	m.detailScroll = 0
	m.resetLive()
	cmd := m.poll()
	// Nothing issued before the switch may land after it, even for this
	// same run: switching away and straight back would otherwise let the
	// earlier visit's late poll be absorbed as the first snapshot.
	m.seqFloor = m.pollSeq
	return cmd
}

// Update is update plus what every message shares: the expiry ticks of
// toasts added while handling it, and saving the UI state when it changed
// (uistate.go) — compared here rather than at each key, so no handler can
// forget it.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before, wasQuit, toasts := m.uiState(), m.quit, m.toasts
	next, cmd := m.update(msg)
	nm, ok := next.(tuiModel)
	if !ok {
		return next, cmd
	}
	cmd = tea.Batch(cmd, toast.Ticks(toasts, nm.toasts))
	if nm.quit && !wasQuit {
		// Quit once the state is on disk: the Final Saved quits.
		var flush tea.Cmd
		nm.saver, flush = nm.saver.FlushWith(nm.uiState())
		return nm, flush
	}
	if after := nm.uiState(); after != before {
		var save tea.Cmd
		nm.saver, save = nm.saver.Save(after)
		cmd = tea.Batch(cmd, save)
	}
	return nm, cmd
}

func (m tuiModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case toast.Expired:
		m.toasts = toast.Update(m.toasts, msg)
		return m, nil
	case prefs.SaveTick:
		var cmd tea.Cmd
		m.saver, cmd = m.saver.Update(msg)
		return m, cmd
	case prefs.Saved:
		if msg.Err != nil {
			m.notifyFrom(srcPrefs, "", fmt.Errorf("saving the layout: %w", msg.Err))
		}
		if msg.Final {
			return m, tea.Quit
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		// In the picker only the run list is on screen, so polling the
		// watched run's snapshot every second is work nobody sees.
		if m.picker || m.flowView {
			return m, tea.Batch(m.refreshRuns(), refreshFlows(), tick(m.interval))
		}
		// One poll at a time from the tick. A slow broker answers in up to
		// five seconds; issuing another every second stacked them up.
		if m.polling {
			return m, tea.Batch(m.refreshRuns(), refreshFlows(), tick(m.interval))
		}
		poll := m.poll()
		return m, tea.Batch(poll, m.refreshRuns(), refreshFlows(), tick(m.interval))

	case runsMsg:
		m.mergeRuns(msg)
		return m, nil
	case requestStartedMsg:
		return m.requestStarted(msg)
	case flowsMsg:
		m.applyFlows(msg)
		return m, nil
	case prStateMsg:
		m.prState[msg.url] = msg.state
		return m, nil
	case noticeMsg:
		m.setNotice(msg.text, msg.err)
		return m, nil
	case tmuxPanesMsg:
		if m.tmuxGen.Is(msg.gen) {
			m.setNotice(msg.text, msg.err)
		}
		return m, nil
	case answersSentMsg:
		return m.answersSent(msg)
	case outputDetailMsg:
		if m.outputDetails == nil {
			m.outputDetails = map[string]outputDetail{}
		}
		m.outputDetails[msg.key] = outputDetail{text: msg.text, err: msg.err}
		return m, nil

	case snapshotMsg:
		// A poll issued for another run — the one watched before selectRun
		// switched away — is dropped whole. Applied, it would draw the old
		// run's tasks, and as the new run's "first snapshot" it would set
		// seenChannel and lastSeq from the wrong channel: the new run's
		// history then replays as pulses, or its newer messages lose their
		// flash because the old run's seq is higher.
		if msg.seq >= m.pollSeq {
			m.polling = false
		}
		if msg.runID != m.live.RunID {
			return m, nil
		}
		// An explicit poll (after a steer, an attach) can overlap one from
		// the tick. Whichever was issued later wins; an older result
		// arriving second would roll the view back.
		if msg.seq < m.seqFloor {
			return m, nil
		}
		m.seqFloor = msg.seq
		// An error with no snapshot leaves what is on screen alone: a
		// blank view is worse than a stale one, and the header says the
		// broker is unreachable either way. An error *with* a snapshot is
		// the disk fallback — apply it and keep the error.
		m.err = msg.err
		if msg.snap.RunID == "" && msg.err != nil {
			return m, nil
		}
		// The task cursor is an index into a list sorted by id, so a task
		// added mid-run — split-request, or the planner still creating
		// tasks — can sort above the selection and shift it. Remember what
		// was selected and put the cursor back on it, the same way the run
		// picker anchors on a run id rather than a row number.
		selected, hadSelection := m.selectedTask()
		m.snap = msg.snap
		m.agents = msg.agents
		if hadSelection {
			m.restoreTaskCursor(selected.ID)
		}
		if m.followTail && len(m.snap.Messages) > 0 {
			m.radioCur = len(m.snap.Messages) - 1
		}
		m.clampCursors()
		// A poll can move the cursor to a different node — the selected
		// task was removed, or restoreTaskCursor could not find it and
		// clampCursors pulled the index into range. Both assign taskCur
		// directly rather than going through selectNode, so the node
		// pane's scroll offset stayed where the last node left it: the
		// pane opens on another worker's feed at a line number borrowed
		// from somewhere else.
		//
		// Compared by id, not by index. Every poll shifts indices when the
		// graph grows, and rewinding on those would make the pane
		// unreadable while a worker is running.
		if now, ok := m.selectedTask(); hadSelection && (!ok || now.ID != selected.ID) {
			m.nodeScroll = nodeScrollTail
		}
		return m, m.absorbLive(msg.activity, time.Now())

	case animMsg:
		m.now = time.Time(msg)
		m.frame++
		m.pulses = prunePulses(m.pulses, m.now)
		if !m.animating(m.now) {
			m.animated = false
			return m, nil
		}
		return m, animTick()

	case steerSentMsg:
		if msg.err != nil {
			m.steerNote(fmt.Sprintf("not sent (%v): %s", msg.err, msg.text), true)
			return m, nil
		}
		note := "sent: " + msg.text
		if len(msg.reopened) > 0 {
			note = "reopened " + strings.Join(msg.reopened, ", ") + " — resuming its worker with: " + msg.text
		}
		m.steerNote(note, false)
		// Show the instruction on the radio now rather than on the next
		// tick, so it is clear it went in.
		cmd := m.poll()
		return m, cmd

	case editorDoneMsg:
		return m.editorDone(msg)
	case attachDoneMsg:
		// The attached session took over the terminal; Bubble Tea has just
		// restored the TUI. Poll immediately rather than waiting out the
		// tick, since whatever happened in there may have moved the run on.
		if msg.err != nil {
			m.err = fmt.Errorf("attach: %w", msg.err)
		}
		cmd := m.poll()
		return m, cmd

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		// A key between two clicks breaks the pair: click, move away and back
		// by keyboard, click again must not count as a double click.
		m.clicks = mouse.Clicks{}
		// An error on screen long enough to be read goes with the next key.
		m.toasts = toast.OnKey(m.toasts)
		next, cmd := m.handleKey(msg)
		if nm, ok := next.(tuiModel); ok && nm.focus != focusTasks {
			// The right column keeps showing the pane last read when
			// the graph takes the keys. See layout.go.
			nm.reader = nm.focus
			return nm, cmd
		}
		return next, cmd
	}
	return m, nil
}

// nodeScrollTail parks the node pane at the newest activity and keeps it
// there as the worker writes. A worker being watched is usually one you
// want the latest of, and a fixed offset would drift away from it.
const nodeScrollTail = -1

// followZoom keeps a zoomed pane and the focus together. Moving focus
// while zoomed and leaving the old pane on screen would put the keys
// somewhere invisible, which reads as the keyboard having stopped working.
func (m *tuiModel) followZoom() {
	if m.zoom >= 0 {
		m.zoom = m.focus
	}
}

// scrollNode moves through the node pane's activity, releasing the tail on
// the way up and re-arming it at the bottom.
func (m *tuiModel) scrollNode(delta int) {
	total := m.nodeLineCount()
	avail := m.nodeActivityRows()

	if m.nodeScroll == nodeScrollTail {
		if delta > 0 {
			return // already at the newest
		}
		// Leaving the tail means taking over the window it was showing,
		// so the first step moves by one line. Resolving it to the line
		// count instead — the window's *end* — spent a step for every row
		// on screen before anything moved: eight presses scrolled two
		// lines on a 14-row pane.
		start, _ := activityWindow(nodeScrollTail, avail, total)
		m.nodeScroll = start
	}
	m.nodeScroll += delta
	if m.nodeScroll < 0 {
		m.nodeScroll = 0
	}
	// Past the last full window is the tail, which then keeps following
	// the worker instead of pinning to a line number it has outgrown.
	if m.nodeScroll >= total-avail {
		m.nodeScroll = nodeScrollTail
	}
}

// nodeActivityRows is how many activity lines the node pane shows.
//
// Scrolling and rendering have to agree on the window, or a step moves the
// offset without moving the screen. Derived the same way View derives it,
// for the same reason graphPaneWidth is shared with navigation.
func (m tuiModel) nodeActivityRows() int {
	l := m.layout()
	// The height the pane is rendered at, title row included.
	var h int
	switch {
	case l.zoom == focusNode:
		h = l.top
	case l.zoom >= 0 || m.readerPane() != focusNode:
		return 1
	default:
		h = l.top + 1
	}
	// The pane spends rows on its title, headline and fields before the
	// feed starts; the label above the feed takes one more.
	rows := h - m.nodeFieldRows() - 1
	if rows < 1 {
		rows = 1
	}
	return rows
}

// nodeFieldRows is how many rows the summary above the feed occupies.
func (m tuiModel) nodeFieldRows() int {
	t, ok := m.selectedTask()
	if !ok {
		return 2
	}
	// Title plus headline, then whichever fields have something to say.
	return 2 + len(m.inspectorFields(t, m.nodePaneWidth()))
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.focus {
	case focusTimeline:
		m.moveTimeline(delta)
	case focusTasks:
		m.taskCur += delta
	case focusRadio:
		m.stepRadio(delta)
		m.followTail = false
	}
	m.clampCursors()
}

func (m *tuiModel) jumpTo(i int) {
	switch m.focus {
	case focusTimeline:
		m.timelineSel = 0
	case focusTasks:
		m.selectNode(i)
	case focusRadio:
		m.radioCur = i
		m.followTail = false
	}
	m.clampCursors()
}

func (m *tuiModel) jumpToEnd() {
	switch m.focus {
	case focusTimeline:
		m.timelineSel = timelineFollow
	case focusTasks:
		m.selectNode(len(m.snap.Tasks) - 1)
	case focusRadio:
		m.radioCur = len(m.snap.Messages) - 1
	}
	m.clampCursors()
}

// boxNodeOrder is every task in the order its box is drawn: row by row, and
// left to right within a row. This is reading order on screen, which is what
// horizontal movement should follow — the layout order the cursor indexes is
// by layer and id, and on a wrapped row those two disagree.
//
// Outside the box view the graph is a vertical list with no horizontal axis,
// so reading order is just layout order and h/l behave like k/j.
func (m tuiModel) boxNodeOrder() []int {
	if !m.boxMode {
		order := make([]int, len(m.snap.Tasks))
		for i := range order {
			order[i] = i
		}
		return order
	}
	rows := m.boxRows(m.graphPaneWidth())
	var order []int
	seen := map[int]bool{}
	for _, r := range rows {
		for _, s := range r.spans {
			if seen[s.node] {
				continue
			}
			seen[s.node] = true
			order = append(order, s.node)
		}
	}
	return order
}

// restoreTaskCursor puts the cursor back on the task with the given id
// after a poll. Tasks arrive mid-run — a worker's split-request, or the
// planner still creating them — and the list is sorted by id, so a new
// task can land above the selection and shift every index below it.
// Anchoring on the id keeps the cursor on what the user was reading.
//
// A task that has disappeared leaves the cursor where it is; the index is
// clamped afterwards, which is the least surprising place to land.
func (m *tuiModel) restoreTaskCursor(id string) {
	if id == "" {
		return
	}
	for i, r := range m.graphRows() {
		if r.id == id {
			m.taskCur = i
			return
		}
	}
}

// selectNode moves the graph cursor and rewinds the node pane to the
// newest activity.
//
// The scroll offset belongs to the node you were reading — carrying it to
// the next one lands you in the middle of a different worker's transcript,
// at a position that means nothing there.
func (m *tuiModel) selectNode(i int) {
	if i == m.taskCur {
		return
	}
	m.taskCur = i
	m.nodeScroll = nodeScrollTail
	m.clampCursors()
}

func (m *tuiModel) clampCursors() {
	if m.taskCur < 0 {
		m.taskCur = 0
	}
	if n := len(m.snap.Tasks); n > 0 && m.taskCur >= n {
		m.taskCur = n - 1
	}
	if m.radioCur < 0 {
		m.radioCur = 0
	}
	if n := len(m.snap.Messages); n > 0 && m.radioCur >= n {
		m.radioCur = n - 1
	}
	if n := len(m.timelineItems()); m.timelineSel >= n {
		m.timelineSel = n - 1
	}
	if m.timelineSel < timelineFollow {
		m.timelineSel = timelineFollow
	}
}

// sortedTasks returns tasks in a stable order so the cursor does not jump
// between polls (the broker stores them in a map).
func (m tuiModel) sortedTasks() []broker.TaskSnapshot {
	tasks := append([]broker.TaskSnapshot(nil), m.snap.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks
}

func (m tuiModel) View() string {
	if m.quit {
		return ""
	}
	base := m.viewScreen()
	switch {
	case m.palette:
		return m.viewPaletteOverlay(base)
	case m.help:
		return m.viewHelpOverlay(base)
	}
	return base
}

// viewScreen is the screen under any overlay.
func (m tuiModel) viewScreen() string {
	if m.picker {
		return m.viewHome()
	}
	if m.questionsView {
		return m.viewQuestions()
	}
	if m.outputsView {
		return m.viewOutputs()
	}
	if m.flowView {
		return m.viewFlow()
	}
	switch m.detail {
	case detailResult:
		return m.viewResult()
	case detailMessage:
		return m.viewMessageDetail()
	case detailSession:
		return m.viewTaskView()
	}

	header := m.viewHeader()
	footer := m.viewFooter()
	bodyHeight := m.bodyHeightFor(header, footer)

	// A zoomed pane is the body. Reading a long assignment or a busy
	// channel means wanting one pane to be the screen for a moment, and
	// the sizes come from the same functions navigation uses so the cursor
	// still lands where it looks like it should.
	l := m.layout()
	switch l.zoom {
	case focusTimeline:
		return pinFooter(header+"\n"+m.viewTimeline(m.width, bodyHeight), footer, m.height)
	case focusTasks:
		return pinFooter(header+"\n"+m.viewTasks(m.width, bodyHeight), footer, m.height)
	case focusNode:
		return pinFooter(header+"\n"+m.viewInspector(m.width, bodyHeight), footer, m.height)
	case focusRadio:
		return pinFooter(header+"\n"+m.viewChannel(m.width, bodyHeight), footer, m.height)
	}
	body := m.viewBands(l)
	return pinFooter(header+"\n"+body, footer, m.height)
}

// bodyHeightFor is the rows between the header and the footer.
func (m tuiModel) bodyHeightFor(header, footer string) int {
	h := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
	if h < 1 {
		// On a terminal too short for header, body and footer together,
		// draw one row of body and let the rest be clipped by the caller.
		// A floor of 3 here overflowed a five-row terminal by two lines —
		// the panes are told a height and honour it, so a floor larger
		// than the space available is an instruction to overflow.
		h = 1
	}
	return h
}

// bodyHeight is bodyHeightFor the current header and footer, for callers
// outside View that need the same number — scrolling the timeline has to
// agree with drawing it about how many rows it has.
func (m tuiModel) bodyHeight() int {
	return m.bodyHeightFor(m.viewHeader(), m.viewFooter())
}

// inspectorContentHeight is how many rows the node pane actually has to
// show: its fixed fields plus the worker's activity, capped so a very long
// feed does not claim the whole body.
func (m tuiModel) inspectorContentHeight() int {
	t, ok := m.selectedTask()
	if !ok {
		return 0
	}
	// Title, headline, and the field rows the pane emits, plus the
	// activity heading. Counted rather than guessed: the fields only
	// appear when they have something to say.
	fixed := 3 + len(m.inspectorFields(t, m.nodePaneWidth()))
	activity := m.nodeLineCountAt(t.ID, m.nodePaneWidth())
	if activity == 0 {
		// The "(no output yet)" line, and one row of slack so the pane
		// does not sit flush against the rule below it.
		activity = 2
	}
	return fixed + activity
}

// graphPaneWidth is the width the graph pane is rendered at. Navigation
// needs it too: moving between sibling boxes means asking the layout which
// boxes share a row, and the layout depends on how wide the pane is. If the
// two disagreed, `l` would step to a box that is not on screen where the
// user sees it.
//
// Zoom resolves here for that reason. A zoomed graph is drawn at the full
// width, so navigation has to be computed at the full width as well — the
// rows a box shares change when the pane gets wider.
func (m tuiModel) graphPaneWidth() int {
	switch m.effectiveZoom() {
	case focusTasks:
		return m.width
	case focusNode, focusRadio, focusTimeline:
		return 0
	}
	return m.columnWidth()
}

// columnWidth is the inner width of the bands' left column, where the graph
// and the timeline sit.
func (m tuiModel) columnWidth() int {
	// Boxes need room for two borders plus a readable label, so the graph
	// pane gets a wider floor in box mode than the lane gutter needs.
	w := m.width/3 + m.widthDelta
	minLeft, maxLeft := 24, 44
	if m.boxMode {
		minLeft, maxLeft = 30, 52
	}
	// The user's adjustment widens the ceiling — the cap exists so the
	// graph does not eat the screen by default, not to stop someone who
	// asked for it. The right side still keeps a usable minimum.
	if max := m.width - 24; maxLeft < max {
		maxLeft = max
	}
	if w < minLeft {
		w = minLeft
	}
	if w > maxLeft {
		w = maxLeft
	}
	if w > m.width-2 {
		w = m.width - 2
	}
	// Never below one cell: a terminal narrower than the borders is drawn
	// clipped, not with negative widths.
	return max(w, 1)
}

// The palette. Colour carries meaning here rather than decorating: the
// same hue means the same thing everywhere, so a glance at any pane reads
// the same way.
//
//	green   finished, and finished well
//	cyan    in flight — the things actually moving
//	yellow  waiting on something
//	red     failed, or urgent
//	blue    who said it (agent and task names)
//	magenta structure the run is built from (threads, models, files)
//	dim     furniture: borders, labels, keys, anything you read past
//
// ANSI 0–7 rather than 256-colour or truecolour, so the terminal's own
// theme decides the exact shade. A hard-coded palette fights whatever the
// user has chosen and loses.
var (
	styTitle   = lipgloss.NewStyle().Bold(true)
	styDim     = lipgloss.NewStyle().Faint(true)
	styGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styBlue    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	styMagenta = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	stySel     = lipgloss.NewStyle().Reverse(true)

	// Bold variants for the few places where a colour has to carry across
	// a pane of text — a running count, an urgent message, a focused pane
	// title — without a second colour competing with it.
	styGreenBold  = styGreen.Bold(true)
	styYellowBold = styYellow.Bold(true)
	styCyanBold   = styCyan.Bold(true)
	styRedBold    = styRed.Bold(true)
	// styHeading is a section heading inside text a worker wrote: a third
	// level, or a "■ (1) …" line. Bold alone left it the shade of the
	// prose around it, and a long report read as one block.
	styHeading = styYellowBold
	// styThought is a worker's extended thinking: the same weight as its
	// prose, set apart so private reasoning is not mistaken for what it
	// told its peers.
	styThought = lipgloss.NewStyle().Italic(true)
)

// runLabel is the one line that tells a run apart from its neighbours.
//
// Prompts are pasted, not written: a benchmark run opens with a hundred
// characters of <uploaded_files> and an absolute path, and twenty rows of
// that are indistinguishable. Strip the boilerplate so the row starts at
// the part a person wrote.
func runLabel(r store.LiveRun) string {
	p := strings.ReplaceAll(r.Prompt, "\n", " ")
	p = strings.Join(strings.Fields(p), " ")

	// Tool-injected preambles, in the order they nest.
	if i := strings.Index(p, "</uploaded_files>"); i >= 0 {
		p = strings.TrimSpace(p[i+len("</uploaded_files>"):])
	}
	for _, lead := range []string{
		"I've uploaded a code repository in the directory",
		"I have uploaded a code repository in the directory",
	} {
		if strings.HasPrefix(p, lead) {
			// Drop the path too — it is the same for every run in a slice.
			rest := strings.TrimSpace(strings.TrimPrefix(p, lead))
			if j := strings.IndexByte(rest, ' '); j > 0 {
				rest = strings.TrimSpace(rest[j:])
			}
			p = rest
		}
	}
	if p == "" {
		return "(no prompt — " + r.RunID + ")"
	}
	return p
}

// runOutcome renders a finished run's result, or a live run's progress.
//
// A list that says only when and where makes you open every run to learn
// whether it worked — which is the question you had before opening
// anything.
func (m tuiModel) runOutcome(r store.LiveRun) string {
	if runIsWorking(r) {
		if r.Running > 0 {
			return styCyan.Render(fmt.Sprintf("running %d", r.Running))
		}
		return styCyan.Render("running")
	}
	if runIsOrphaned(r) {
		// Yellow, not red: nothing failed. It was abandoned, which is a
		// thing to go and look at rather than a result to read.
		return styYellow.Render(fmt.Sprintf("orphaned %d/%d", r.Done, r.Tasks))
	}
	if r.Tasks == 0 {
		return styDim.Render("finished")
	}
	parts := []string{styGreen.Render(fmt.Sprintf("%d/%d", r.Done, r.Tasks))}
	if r.Failed > 0 {
		parts = append(parts, styRedBold.Render(fmt.Sprintf("%d failed", r.Failed)))
	}
	if r.Messages > 0 {
		parts = append(parts, styDim.Render(fmt.Sprintf("%d msg", r.Messages)))
	}
	return strings.Join(parts, "  ")
}

// runIsWorking reports whether a run still has work in flight.
//
// Having a broker is not the same as having work. The daemon holds one for
// every run it has ever hosted, so testing BrokerURL called a run whose
// tasks had all completed "running" — the picker said running while the
// graph beside it showed four ticks.
//
// The run's own state is the answer where there is one; a batch run
// advertised through the registry file has none, and there its broker
// really does die with the process.
func runIsWorking(r store.LiveRun) bool {
	switch r.State {
	case string(broker.RunDone), string(broker.RunFailed), string(broker.RunCanceled):
		return false
	case string(broker.RunActive):
		// "active" is not proof of work. A run persisted before the daemon
		// learned to retire settled runs kept that state forever, and a
		// snapshot written mid-flight keeps it too — so believe the tasks
		// over the label when every one of them is terminal.
		if r.Tasks > 0 && r.Done+r.Failed >= r.Tasks {
			return false
		}
		// Nor is it proof of work when nothing is serving the run. An
		// active run comes back from disk once the daemon that hosted it
		// is gone, and it kept saying "running 4" — while its workers were
		// posting to a port that no longer answers.
		return r.BrokerURL != ""
	}
	return r.BrokerURL != ""
}

// runIsOrphaned reports a run left mid-flight by the death of its host.
//
// This is not the same as finished, and rendering it as "0/4" would say the
// run failed at everything. It stopped being served: the daemon exited, or
// was restarted underneath it, and its workers — detached processes with
// the old broker's URL baked into their scripts — keep working and report
// to a port that is not there.
//
// Distinguishing it matters because it is the state you must act on. A
// finished run needs nothing; an orphan has processes still running, and
// nothing will ever record what they produce.
//
// A transient lister error shows hosted runs as orphaned for one poll and
// heals on the next. That is honest — for that moment the monitor cannot
// reach them either.
func runIsOrphaned(r store.LiveRun) bool {
	if r.State != string(broker.RunActive) || r.BrokerURL != "" {
		return false
	}
	// No tasks at all is a run that never got going, not one interrupted.
	return r.Tasks > 0 && r.Done+r.Failed < r.Tasks
}

// plainOutcome is runOutcome without styling, for measuring and for the
// selected row — which reverses the whole line and must not carry escapes
// inside it.
func plainOutcome(r store.LiveRun) string {
	if runIsWorking(r) {
		if r.Running > 0 {
			return fmt.Sprintf("running %d", r.Running)
		}
		return "running"
	}
	if runIsOrphaned(r) {
		return fmt.Sprintf("orphaned %d/%d", r.Done, r.Tasks)
	}
	if r.Tasks == 0 {
		return "finished"
	}
	s := fmt.Sprintf("%d/%d", r.Done, r.Tasks)
	if r.Failed > 0 {
		s += fmt.Sprintf("  %d failed", r.Failed)
	}
	if r.Messages > 0 {
		s += fmt.Sprintf("  %d msg", r.Messages)
	}
	return s
}

// runCountLabel says how many runs there are and how many are still going.
//
// It used to read "N live runs" for a list that is now mostly history —
// which is exactly the kind of label you stop reading because it is
// always wrong.
//
// Orphans are counted apart from both. Folding them into "finished" is how
// three abandoned runs sat in the list looking like history while their
// workers were still burning through a task nobody would ever collect.
func (m tuiModel) runCountLabel() string {
	live, orphaned := 0, 0
	for _, r := range m.runs {
		switch {
		case runIsWorking(r):
			live++
		case runIsOrphaned(r):
			orphaned++
		}
	}
	done := len(m.runs) - live - orphaned

	var parts []string
	if live > 0 {
		parts = append(parts, fmt.Sprintf("%d live", live))
	}
	if orphaned > 0 {
		parts = append(parts, fmt.Sprintf("%d orphaned", orphaned))
	}
	switch {
	case len(m.runs) == 0:
		return "no runs"
	case len(parts) == 0:
		return fmt.Sprintf("%d finished runs", len(m.runs))
	case done == 0 && len(parts) == 1 && live > 0:
		return fmt.Sprintf("%d live runs", live)
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d finished", done))
	}
	return strings.Join(parts, ", ")
}

// viewPicker lists the runs: live ones first, then recent history.
//
// It shows more than ids: an interactive session names runs by timestamp,
// so the prompt is the only thing that distinguishes them at a glance.
// runStartTime is when a run began. Batch runs record StartedAt; daemon
// runs do not, but every run id ends in the millisecond timestamp it was
// minted from ("s1786472797321-1", "plan-1786543427573"), so the id is a
// usable fallback rather than showing the epoch.
func runStartTime(r store.LiveRun) time.Time {
	if r.StartedAt > 0 {
		return time.Unix(r.StartedAt, 0)
	}
	digits := strings.TrimLeft(r.RunID, "abcdefghijklmnopqrstuvwxyz-")
	if i := strings.IndexByte(digits, '-'); i > 0 {
		digits = digits[:i]
	}
	if ms, err := strconv.ParseInt(digits, 10, 64); err == nil && ms > 1e12 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

// abbreviatePath shortens a working directory to something that fits a
// column: home becomes ~, and a deep path keeps its last two segments.
func abbreviatePath(p string) string {
	if p == "" {
		return "—"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	parts := strings.Split(p, string(filepath.Separator))
	if len(parts) <= 3 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}

func (m tuiModel) viewHeader() string {
	icon, state := runGlyph(m.snap.State)
	if m.snap.State == broker.RunActive || m.snap.State == "" {
		icon = styCyan.Render(spinnerAt(m.frame))
	}
	// Where you are, and the way back: this is one request of several.
	left := styDim.Render("‹ ") + styCyanBold.Render("Narwhal") + styDim.Render("   ") +
		icon + " " + state + "   " + m.taskCounts()
	if m.err != nil {
		left += "   " + styRed.Render("daemon unreachable — showing the saved copy")
	}
	if sum := verdictSummary(m.snap.Verdicts); sum != "" {
		left += "   " + sum
	}
	// The id is what `narwhal show` takes, and with several runs the
	// position says [ and ] have somewhere to go. It is reference, not
	// news, so it sits at the far edge in the dimmest weight.
	ref := m.runID()
	if len(m.runs) > 1 {
		ref += fmt.Sprintf("  %d/%d", m.runCur+1, len(m.runs))
	}
	w := max(1, m.width)
	line := left
	if gap := w - ansi.StringWidth(left) - ansi.StringWidth(ref); gap >= 3 {
		line += strings.Repeat(" ", gap) + styDim.Render(ref)
	}
	line = render.Truncate(line, w)
	// What waits on the operator, on its own row and only when there is
	// something. See attention.go.
	if band := m.needsYouBand(w); band != "" {
		line += "\n" + band
	}
	if m.autoZoomed() {
		line += "\n" + styDim.Render(render.Truncate(fmt.Sprintf("narrow terminal (%d cols): one pane at a time · tab or 1-4 switch", m.width), w))
	}

	prompt := oneLine(m.snap.Prompt)
	if prompt == "" {
		return line
	}
	// A step of a flow says which, among the others.
	if stages := m.flowStages(w); stages != "" {
		line += "\n" + stages
	}
	// The prompt is what the run is *for*: at full contrast, wrapped to
	// two lines rather than cut at one, and indented under the title so
	// it reads as its subject rather than a second header.
	lines := wrapSpans([]span{{prompt, lipgloss.NewStyle()}}, max(10, w-3), "")
	if len(lines) > 2 {
		lines = lines[:2]
		lines[1] = render.TruncateMarker(lines[1], max(10, w-3)-1, "") + render.Ellipsis()
	}
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return line + "\n" + strings.Join(lines, "\n")
}

// taskCounts is how far the run has got: done out of all, and what is
// moving or broken. A count of zero is not news, so it is left out.
func (m tuiModel) taskCounts() string {
	counts := map[broker.TaskState]int{}
	for _, t := range m.snap.Tasks {
		counts[t.State]++
	}
	if len(m.snap.Tasks) == 0 {
		return styDim.Render("planning…")
	}
	parts := []string{progressBar(counts[broker.TaskCompleted], counts[broker.TaskFailed], len(m.snap.Tasks), 8) +
		" " + fmt.Sprintf("%d/%d done", counts[broker.TaskCompleted], len(m.snap.Tasks))}
	if n := counts[broker.TaskDispatched]; n > 0 {
		parts = append(parts, styCyan.Render(fmt.Sprintf("%d running", n)))
	}
	if n := counts[broker.TaskReady] + counts[broker.TaskPending]; n > 0 {
		parts = append(parts, styDim.Render(fmt.Sprintf("%d waiting", n)))
	}
	if n := counts[broker.TaskFailed]; n > 0 {
		parts = append(parts, styRedBold.Render(fmt.Sprintf("%d failed", n)))
	}
	return strings.Join(parts, styDim.Render(" · "))
}

// runGlyph returns the icon and colored label for a run state.
func runGlyph(s broker.RunState) (string, string) {
	switch s {
	case broker.RunDone:
		return styGreen.Render(icons.runDone), styGreen.Render(string(s))
	case broker.RunFailed:
		return styRed.Render(icons.runFailed), styRed.Render(string(s))
	case broker.RunCanceled:
		return styDim.Render(icons.runCanceled), styDim.Render(string(s))
	default:
		return styCyan.Render(icons.runActive), styCyan.Render(string(s))
	}
}

// pinFooter puts the key hints on the last line of the terminal.
//
// Every view built its output as "content, newline, hints" and stopped
// there, so the hints sat wherever the content happened to end — four rows
// down in a run picker with one run, with the rest of the screen empty
// below them. A hint line is furniture: it belongs at the bottom edge, in
// the same place every time, or the eye has to hunt for it.
//
// body is everything above the hints; height is the terminal's.
func pinFooter(body, hints string, height int) string {
	used := lipgloss.Height(body) + lipgloss.Height(hints)
	if pad := height - used; pad > 0 {
		body += strings.Repeat("\n", pad)
	}

	// Clip rather than overflow. Below about five rows there is no room
	// for a header, a line of content and the hints together, and a view
	// that writes past the last row scrolls the terminal — which is how
	// the header disappears off the top. The hints are what survive: they
	// say how to get out.
	if lines := strings.Split(body, "\n"); len(lines)+lipgloss.Height(hints) > height {
		keep := height - lipgloss.Height(hints)
		if keep < 0 {
			keep = 0
		}
		body = strings.Join(lines[:keep], "\n")
	}
	if body == "" {
		// Not even room for the hints. Keep their last line — for the main
		// view that is the keys rather than the counts, which is the half
		// you need when the window is too small to read anything else.
		hl := strings.Split(hints, "\n")
		if len(hl) > height && height > 0 {
			return strings.Join(hl[len(hl)-height:], "\n")
		}
		return hints
	}
	return body + "\n" + hints
}

func (m tuiModel) viewFooter() string {
	// The steering line, then the keys. The line is always there: a run
	// that can be steered says so and what became of the last instruction,
	// and one that cannot says why. One row however long the last
	// instruction was: a wrapped footer takes its extra line from the panes.
	// Results of actions sit above it as toasts.
	if m.composing {
		return m.footerBlock(m.width, "")
	}
	bar := m.steerBar() + "\n" + m.keyFooter(m.screenStack(), m.width)
	if n := m.noticeLine(m.width); n != "" {
		return n + "\n" + bar
	}
	return bar
}

func (m tuiModel) viewTasks(width, height int) string {
	title := badgedPaneTitle(1, "Graph", m.graphBadge(), m.focus == focusTasks, width)

	if m.boxMode {
		return m.viewTasksBoxed(title, width, height)
	}

	rows := m.graphRows()
	out := make([]string, 0, height)
	out = append(out, title)

	start := m.laneStart(height, len(rows))
	for i := start; i < len(rows) && len(out) < height; i++ {
		out = append(out, m.taskRow(rows[i], width, i == m.taskCur && m.focus == focusTasks))
	}
	if len(rows) == 0 {
		out = append(out, styDim.Render("(no tasks yet)"))
	}
	return padRows(out, width, height)
}

// laneStart is the first graph row drawn in a lane-mode pane height rows
// tall. The mouse handler calls it too, so a click lands on the row drawn.
func (m tuiModel) laneStart(height, total int) int {
	return scrollStart(m.taskCur, height-1, total)
}

// boxStart is the first rendered line drawn in a box-mode pane height rows
// tall: the window is anchored on the first line of the selected task's box.
func (m tuiModel) boxStart(rows []boxRow, height int) int {
	anchor := 0
	for i, r := range rows {
		if r.owns(m.taskCur) {
			anchor = i
			break
		}
	}
	return scrollStart(anchor, height-1, len(rows))
}

// viewTasksBoxed renders the box view. Scrolling works in rendered lines
// rather than tasks, because one task is several lines; the cursor still
// selects a task, and the window is positioned to keep its box on screen.
func (m tuiModel) viewTasksBoxed(title string, width, height int) string {
	rows := m.boxRows(width)
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	marks := overlayPulses(rows, boxCenters(rows, layoutGraph(m.sortedTasks()).nodes), m.pulses, now)
	headline := headlineRows(rows)
	out := make([]string, 0, height)
	out = append(out, title)

	if len(rows) == 0 {
		out = append(out, styDim.Render("(no tasks yet)"))
		return lipgloss.NewStyle().Width(width).Render(strings.Join(out, "\n"))
	}

	start := m.boxStart(rows, height)
	for i := start; i < len(rows) && len(out) < height; i++ {
		r := rows[i]
		selected := -1
		if m.focus == focusTasks {
			selected = m.taskCur
		}
		out = append(out, printable(m.styleBoxLine(r, render.Truncate(r.text, width), selected, marks[i], headline[i])))
	}
	return padRows(out, width, height)
}

// styleBoxLine colors one rendered line. Each box on the line gets its own
// task's state color, the selected box is reversed, and everything between
// boxes (edges, margins) stays dim. Coloring per span rather than per line
// matters because siblings share a row: styling the whole line would give
// three boxes the first one's state, and highlight all three when one is
// selected. selected is -1 when the graph pane does not have focus.
func (m tuiModel) styleBoxLine(r boxRow, line string, selected int, marks map[int]pulseMark,
	headline map[int]bool) string {
	runes := []rune(line)
	// A running box's icon is the spinner's current frame. The swap happens
	// here rather than in the layout so the layout stays a pure function of
	// the graph — it is what navigation and its tests measure against.
	// Only the headline: a box with a detail line has two body rows, and
	// the second one holds the tool, not an icon.
	for _, s := range r.spans {
		t := m.taskByIndex(s.node)
		if headline[s.node] && t.State == broker.TaskDispatched {
			if at := s.x0 + 2; at < len(runes) {
				runes[at] = []rune(spinnerAt(m.frame))[0]
			}
		}
	}
	// Routing cells are dim, except where a pulse is passing through.
	dimRun := func(seg []rune, x0 int) string {
		if len(marks) == 0 {
			return styDim.Render(string(seg))
		}
		var b strings.Builder
		for i, ch := range seg {
			mk, ok := marks[x0+i]
			switch {
			case !ok:
				b.WriteString(styDim.Render(string(ch)))
			case mk.urgent:
				b.WriteString(styRedBold.Render(string(ch)))
			case mk.head:
				b.WriteString(styCyanBold.Render(string(ch)))
			default:
				b.WriteString(styCyan.Render(string(ch)))
			}
		}
		return b.String()
	}
	if len(r.spans) == 0 {
		return dimRun(runes, 0)
	}

	clamp := func(x int) int {
		if x < 0 {
			return 0
		}
		if x > len(runes) {
			return len(runes)
		}
		return x
	}

	var b strings.Builder
	at := 0
	for _, s := range r.spans {
		x0, x1 := clamp(s.x0), clamp(s.x1)
		if x1 <= x0 {
			continue
		}
		if x0 > at {
			b.WriteString(dimRun(runes[at:x0], at))
		}
		seg := string(runes[x0:x1])
		task := m.taskByIndex(s.node)
		hot := m.isHot(task.ID)
		switch {
		case s.node == selected:
			b.WriteString(stySel.Render(seg))
		case s.part == partBody:
			_, style := taskIconStyle(task.State)
			if hot {
				style = style.Bold(true)
			}
			b.WriteString(style.Render(seg))
		case s.part == partTop || s.part == partBottom:
			// The frame takes its task's colour, faintly, so a box reads as
			// one object. Dim borders around a coloured label made every
			// box look like a grey frame someone had written inside. A box
			// that just acted or was just spoken to lights its frame.
			_, style := taskIconStyle(task.State)
			if hot {
				b.WriteString(style.Bold(true).Render(seg))
			} else {
				b.WriteString(style.Faint(true).Render(seg))
			}
		default:
			// Routing between boxes stays dim: the edges are structure, and
			// colouring them would compete with the nodes they connect.
			b.WriteString(styDim.Render(seg))
		}
		at = x1
	}
	if at < len(runes) {
		b.WriteString(dimRun(runes[at:], at))
	}
	return b.String()
}

// taskByIndex resolves a layout position to its task.
func (m tuiModel) taskByIndex(i int) broker.TaskSnapshot {
	nodes := layoutGraph(m.sortedTasks()).nodes
	if i < 0 || i >= len(nodes) {
		return broker.TaskSnapshot{}
	}
	return nodes[i].task
}

// boxRows lays out the DAG as boxes for the current snapshot.
func (m tuiModel) boxRows(width int) []boxRow {
	return layoutGraph(m.sortedTasks()).renderBoxesDetailed(width, taskIconPlain, m.liveDetail)
}

// taskIconPlain adapts taskIconStyle for the box renderer, which applies
// color itself once the line is assembled.
func taskIconPlain(s broker.TaskState) (string, string) {
	icon, _ := taskIconStyle(s)
	return icon, ""
}

// taskRow renders one graph line: the edge gutter, then the task.
// Like radioRow, the text is measured and truncated as plain text before
// any styling is applied.
func (m tuiModel) taskRow(r graphRow, width int, selected bool) string {
	icon, style := taskIconStyle(r.state)

	label := r.label
	if r.dispatches > 1 {
		label += fmt.Sprintf(" ×%d", r.dispatches)
	}

	plain := r.gutter + " " + icon + " " + label
	if selected {
		return stySel.Render(render.PadRight(render.Truncate(plain, width), width))
	}
	if render.Width(plain) > width {
		head := r.gutter + " " + icon + " "
		label = render.Truncate(label, width-render.Width(head))
	}
	return styDim.Render(r.gutter) + " " + style.Render(icon) + " " + label
}

// graphRows lays out the DAG for the current snapshot.
func (m tuiModel) graphRows() []graphRow {
	return layoutGraph(m.sortedTasks()).render()
}

// paneTitle renders a pane header as a labelled rule.
//
// With three panes on screen the eye needs to see where one ends and the
// next begins, and which one has the keys. A bare word could not do that:
// the graph, the inspector and the radio all started with an unadorned
// title and the boundary between the stacked right-hand panes was
// invisible. The rule draws the boundary; bold-underline versus dim says
// which pane is focused.
//
// The rule is one column short of the pane so a full-width line cannot
// push into its neighbour when the panes are joined horizontally.
// numberedPaneTitle is paneTitle with the key that jumps to it. The number
// is only useful if it is on screen — a shortcut you have to be told about
// is one nobody uses.
func numberedPaneTitle(n int, label string, focused bool, width int) string {
	key := styDim.Render(strconv.Itoa(n) + " ")
	if focused {
		key = styCyan.Render(strconv.Itoa(n) + " ")
	}
	return key + paneTitle(label, focused, width-2)
}

// tabbedPaneTitle is numberedPaneTitle for a pane with tabs: the active tab
// reads as a title, the others stay dim.
func tabbedPaneTitle(n int, tabs []string, active int, focused bool, width int) string {
	key := styDim.Render(strconv.Itoa(n) + " ")
	on := styTitle
	if focused {
		key = styCyan.Render(strconv.Itoa(n) + " ")
		on = styCyanBold
	}
	var b strings.Builder
	plain := 2
	for i, t := range tabs {
		if i > 0 {
			b.WriteString(styDim.Render(" │ "))
			plain += 3
		}
		if i == active {
			b.WriteString(on.Render(t))
		} else {
			b.WriteString(styDim.Render(t))
		}
		plain += render.Width(t)
	}
	if rule := width - plain - 1; rule > 0 {
		b.WriteString(" " + styDim.Render(strings.Repeat("─", rule)))
	}
	return key + b.String()
}

func paneTitle(label string, focused bool, width int) string {
	// The focused pane is where the keys go, so it gets the colour. Bold
	// and underline alone had to be compared against the other titles to
	// be read; a hue is apparent without comparison.
	styled := styDim.Render(label)
	if focused {
		// Bold + colour, without the underline. Underline made lipgloss
		// emit a full escape pair per character — the pane title alone was
		// a dozen sequences — and a coloured bold word already stands out
		// against dim neighbours.
		styled = styCyanBold.Render(label)
	}
	rule := width - render.Width(label) - 2
	if rule < 1 {
		return styled
	}
	return styled + " " + styDim.Render(strings.Repeat("─", rule))
}

// padRows renders rows into a pane of exactly height lines.
//
// A pane is told a height and is expected to occupy it. The inspector
// already padded, because the Radio rule beneath it would otherwise slide
// as the graph cursor moved; the graph and the radio did not, so on a tall
// terminal with a short channel the right-hand column ended halfway down
// and its rule floated with nothing under it. The two columns are joined
// with JoinHorizontal, which aligns to the taller one, so a short pane
// also means the two columns describe different regions of the screen.
func padRows(rows []string, width, height int) string {
	if height > 0 {
		if len(rows) > height {
			rows = rows[:height]
		}
		for len(rows) < height {
			rows = append(rows, "")
		}
	}
	// Each row cut to the width, never wrapped here. lipgloss's Width
	// wrapped an overlong row onto a row of its own, flush left — "know"
	// alone under "none — e to write what every step should" — and pushed
	// every row below it down, past the bottom. A row that should wrap is
	// wrapped where it is made, with its indent (wrapText).
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fitCells(r, width)
		if ansi.StringWidth(r) > width {
			out[i] = render.TruncateMarker(r, width-1, "") + render.Ellipsis()
		}
	}
	return strings.Join(out, "\n")
}

// viewChannel is pane 4: the radio, or the operator's brief.
func (m tuiModel) viewChannel(width, height int) string {
	if m.briefTab {
		return m.viewBrief(width, height)
	}
	return m.viewRadio(width, height)
}

// channelTitle is pane 4's title, naming both tabs with the one on screen
// marked, so the title says that 4 has another view and what it is.
func (m tuiModel) channelTitle(width int) string {
	return tabbedPaneTitle(4, []string{fmt.Sprintf("Radio (%d)", len(m.snap.Messages)), "Brief"},
		boolIndex(m.briefTab), m.focus == focusRadio, width)
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

func priorityGlyph(p broker.Priority) (string, lipgloss.Style) {
	switch p {
	case broker.PriorityUrgent:
		return icons.prioUrgent, styRed
	case broker.PriorityFYI:
		return icons.prioFYI, styDim
	default:
		return icons.prioNormal, styCyan
	}
}

// taskByID looks up a task in the current snapshot. Graph rows carry only
// the id, so the detail view resolves the full record here.
func (m tuiModel) taskByID(id string) broker.TaskSnapshot {
	for _, t := range m.snap.Tasks {
		if t.ID == id {
			return t
		}
	}
	return broker.TaskSnapshot{ID: id}
}

// selectedTask returns the task the cursor points at, and ok=false when the
// graph is empty.
func (m tuiModel) selectedTask() (broker.TaskSnapshot, bool) {
	if m.pinnedTask != "" {
		// A pane pinned to one task shows that task whatever the cursor.
		t := m.taskByID(m.pinnedTask)
		return t, t.ID != ""
	}
	rows := m.graphRows()
	if len(rows) == 0 {
		return broker.TaskSnapshot{}, false
	}
	cur := m.taskCur
	if cur >= len(rows) {
		cur = len(rows) - 1
	}
	return m.taskByID(rows[cur].id), true
}

// workerActivity reads the selected worker's session transcript.
func (m tuiModel) workerActivity(taskID string) []transcriptEntry {
	sid := m.workerSessionID(taskID)
	if sid == "" {
		return nil
	}
	return readTranscript(transcriptPath(m.live.CWD, sid))
}

// sessionLogPath returns where the launcher writes a worker's Claude output:
// ~/.narwhal/sessions/<run-id>/agents/worker-<task-id>/claude-output.txt.
func (m tuiModel) sessionLogPath(taskID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".narwhal", "sessions", m.runID(),
		"agents", "worker-"+taskID, "claude-output.txt")
}

// runID identifies the run being watched, preferring the discovered entry
// over the snapshot.
//
// These are normally the same, but a snapshot that failed to load leaves
// m.snap zeroed while m.live still knows which run it is — and the paths
// under ~/.narwhal/sessions are keyed by run id, so falling back to the
// empty one points them at a directory that does not exist.
func (m tuiModel) runID() string {
	if m.live.RunID != "" {
		return m.live.RunID
	}
	return m.snap.RunID
}

// sessionIDPath returns where the launcher records a worker's Claude
// session UUID.
func (m tuiModel) sessionIDPath(taskID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".narwhal", "sessions", m.runID(),
		"agents", "worker-"+taskID, "claude-session-id")
}

// workerSessionID reads the Claude session UUID a worker runs under, or ""
// when it is not recorded — a run started before the launcher began pinning
// session ids, or a task that has not been dispatched.
func (m tuiModel) workerSessionID(taskID string) string {
	return strings.TrimSpace(strings.Join(globalFiles.lines(m.sessionIDPath(taskID)), "\n"))
}

// attachDoneMsg reports back when an attached session exits.
type attachDoneMsg struct{ err error }

// workerCWD resolves the directory a worker's session ran in.
//
// The run normally carries it, but a run persisted before snapshots
// recorded cwd — which is every run on disk written by an older binary —
// has none, and Claude files transcripts per directory, so resuming from
// the wrong one silently finds nothing. The transcript records the
// directory it ran in, so ask it.
func (m tuiModel) workerCWD(sessionID string) string {
	if m.live.CWD != "" {
		return m.live.CWD
	}
	if m.snap.CWD != "" {
		return m.snap.CWD
	}
	return transcriptCWD(findTranscript(sessionID))
}

// attachToSession suspends the monitor and opens the worker's Claude
// session, restoring the TUI when it exits.
//
// Opening the real session is the only way to watch a running worker:
// `claude --print` buffers its output until it finishes, so the captured
// log is empty for the whole run, while the session transcript is written
// continuously. --fork-session leaves the worker's own conversation
// untouched — attaching is for watching, and sharing the session id would
// have the monitor and the worker appending to one transcript.
//
// The error names which of the two things was missing. Reporting both as
// "no recorded session" is what made a run with four perfectly good session
// ids look like it had never pinned any.
func (m tuiModel) attachToSession(taskID string) (tea.Cmd, error) {
	sid := m.workerSessionID(taskID)
	if sid == "" {
		return nil, fmt.Errorf("no recorded session for %s — the run predates session pinning, or the task has not started", taskID)
	}
	cwd := m.workerCWD(sid)
	if cwd == "" {
		return nil, fmt.Errorf("no transcript on disk for %s (session %s) — nothing to resume from", taskID, sid)
	}
	c := exec.Command("claude", "--resume", sid, "--fork-session")
	c.Dir = cwd
	// Hand over the terminal. tea.ExecProcess suspends the TUI but does not
	// wire up the streams, so without this the attached session starts with
	// no stdin — it comes up and immediately has nothing to read, which
	// looks exactly like attach being broken.
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return attachDoneMsg{err: err}
	}), nil
}

// workerOutputLines reads a worker's whole session log, newest last,
// through globalFiles so a frame only stats it. Returns nil when the file does not exist yet — the task has not been
// dispatched, or this run keeps its sessions elsewhere.
func (m tuiModel) workerOutputLines(taskID string) []string {
	return globalFiles.lines(m.sessionLogPath(taskID))
}

// scrollWindow clamps a scroll offset to a body length and returns the
// visible range plus a position indicator.
func scrollWindow(scroll, avail, total int) (int, int, string) {
	if scroll > total-1 {
		scroll = total - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + avail
	if end > total {
		end = total
	}
	pos := ""
	if total > avail {
		pos = styDim.Render(fmt.Sprintf("  [%d-%d/%d]", scroll+1, end, total))
	}
	return scroll, end, pos
}

func mentionsLabel(m []string) string {
	if len(m) == 0 {
		return styDim.Render("(broadcast)")
	}
	return styMagenta.Render(strings.Join(m, ", "))
}

func taskIconStyle(s broker.TaskState) (string, lipgloss.Style) {
	switch s {
	case broker.TaskCompleted:
		return icons.taskCompleted, styGreen
	case broker.TaskDispatched:
		return icons.taskDispatched, styCyan
	case broker.TaskReady:
		return icons.taskReady, styYellow
	case broker.TaskFailed:
		return icons.taskFailed, styRed
	case broker.TaskBlocked:
		return icons.taskBlocked, styRed
	default:
		return icons.taskPending, styDim
	}
}

// scrollStart returns the first index to render so cur stays visible.
func scrollStart(cur, visible, total int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	start := cur - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	return start
}

// shortName trims the worker- prefix so the sender column stays narrow.
// shortName is how a sender reads on the radio: a worker by its task, and
// the operator as "you", which is who is reading.
func shortName(s string) string {
	if s == "main" {
		return "you"
	}
	return strings.TrimPrefix(s, "worker-")
}

// wrapText breaks content into display lines, preserving existing newlines.
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	// By display cells, not bytes. Counting bytes cut Hangul — three
	// bytes, two cells — a third short of the edge, and could cut inside a
	// character and print the halves as garbage.
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapSpans([]span{{para, lipgloss.NewStyle()}}, width, "")...)
	}
	return out
}

func containsRun(runs []store.LiveRun, id string) bool {
	for _, r := range runs {
		if r.RunID == id {
			return true
		}
	}
	return false
}
