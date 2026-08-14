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
	"github.com/mattn/go-runewidth"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

type focusPane int

const (
	focusTasks focusPane = iota
	focusRadio
)

// detailMode says what the detail pane is showing, or that it is closed.
type detailMode int

const (
	detailClosed detailMode = iota
	detailMessage
	detailTask
	// detailSession shows a worker's full Claude session output. The task
	// detail summarizes; this is where you actually watch a worker work.
	detailSession
)

// tickMsg drives the poll loop.
type tickMsg time.Time

// snapshotMsg carries a fresh poll result.
type snapshotMsg struct {
	snap   broker.Snapshot
	agents []string
	err    error
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

	width  int
	height int
	quit   bool
}

// newTUIModel builds the model. runs is every live run and cur indexes the
// one to open; the picker is shown when the caller did not single one out.
func newTUIModel(runs []store.LiveRun, cur int, interval time.Duration, picker bool) tuiModel {
	m := tuiModel{
		interval:   interval,
		client:     &http.Client{Timeout: 5 * time.Second},
		focus:      focusRadio,
		followTail: true,
		boxMode:    true,
		runs:       runs,
		runCur:     cur,
		picker:     picker,
		width:      100,
		height:     30,
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
	return tea.Batch(m.poll(), m.refreshRuns(), tick(m.interval))
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

func (m tuiModel) poll() tea.Cmd {
	// A finished run has no broker to ask. Discover marks those with an
	// empty BrokerURL and the snapshot on disk is the whole record, so
	// read it instead of failing every second against a dead port.
	if m.live.BrokerURL == "" {
		runID := m.live.RunID
		return func() tea.Msg {
			snap, err := store.LoadRun(runID)
			if err != nil {
				return snapshotMsg{err: fmt.Errorf("read saved run: %w", err)}
			}
			return snapshotMsg{snap: snap}
		}
	}

	url := m.live.BrokerURL + "/api/v1/monitor/" + m.live.RunID
	client := m.client
	return func() tea.Msg {
		resp, err := client.Get(url)
		if err != nil {
			return snapshotMsg{err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return snapshotMsg{err: fmt.Errorf("broker returned %d", resp.StatusCode)}
		}
		var payload struct {
			Snapshot broker.Snapshot `json:"snapshot"`
			Agents   []string        `json:"agents"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return snapshotMsg{err: err}
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
	sorted := append([]store.LiveRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].StartedAt, sorted[j].StartedAt
		if a != b {
			return a > b
		}
		return sorted[i].RunID > sorted[j].RunID
	})
	m.runs = sorted

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
	return m.poll()
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		// In the picker only the run list is on screen, so polling the
		// watched run's snapshot every second is work nobody sees.
		if m.picker {
			return m, tea.Batch(m.refreshRuns(), tick(m.interval))
		}
		return m, tea.Batch(m.poll(), m.refreshRuns(), tick(m.interval))

	case runsMsg:
		m.mergeRuns(msg)
		return m, nil

	case snapshotMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
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
		return m, nil

	case attachDoneMsg:
		// The attached session took over the terminal; Bubble Tea has just
		// restored the TUI. Poll immediately rather than waiting out the
		// tick, since whatever happened in there may have moved the run on.
		if msg.err != nil {
			m.err = fmt.Errorf("attach: %w", msg.err)
		}
		return m, m.poll()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handlePickerKey drives the run list. It is a separate mode rather than a
// third pane because switching runs replaces everything on screen.
func (m tuiModel) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "j", "down":
		if m.runCur < len(m.runs)-1 {
			m.runCur++
		}
	case "k", "up":
		if m.runCur > 0 {
			m.runCur--
		}
	case "g", "home":
		m.runCur = 0
	case "G", "end":
		m.runCur = len(m.runs) - 1
	case "enter", "l", "right":
		cmd := m.selectRun(m.runCur)
		m.picker = false
		return m, cmd
	case "esc":
		// esc backs out. From the top level there is nowhere further to go,
		// so it quits rather than sitting inert.
		m.quit = true
		return m, tea.Quit
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.picker {
		return m.handlePickerKey(msg)
	}
	if m.detail != detailClosed {
		return m.handleDetailKey(msg)
	}
	switch msg.String() {
	case "q", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "tab":
		if m.focus == focusTasks {
			m.focus = focusRadio
		} else {
			m.focus = focusTasks
		}
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.jumpTo(0)
	case "G", "end":
		m.jumpToEnd()
	case "ctrl+d", "pgdown":
		m.moveCursor(10)
	case "ctrl+u", "pgup":
		m.moveCursor(-10)
	case "enter":
		// Detail shows whichever pane you opened it from.
		switch m.focus {
		case focusRadio:
			if len(m.snap.Messages) > 0 {
				m.detail = detailMessage
				m.detailScroll = 0
			}
		case focusTasks:
			if len(m.snap.Tasks) > 0 {
				m.detail = detailTask
				m.detailScroll = 0
			}
		}
	case "s":
		// Open the selected task's Claude session output. Reachable from
		// either pane: when the radio has focus, the task cursor still
		// points at something, and watching a worker is the common reason
		// to reach for the keyboard at all.
		if len(m.snap.Tasks) > 0 {
			m.detail = detailSession
			m.detailScroll = 0
			m.sessionTail = true
		}
	case "a":
		// Attach to the worker's live Claude session. s shows what the
		// worker has written; a puts you inside the session itself, which
		// for a running worker is the only place anything is visible.
		if t, ok := m.selectedTask(); ok {
			if cmd := m.attachToSession(t.ID); cmd != nil {
				return m, cmd
			}
			m.err = fmt.Errorf("no recorded session for %s — the run predates session pinning, or the task has not started", t.ID)
		}
	case "f":
		// Re-arm tail following after manual navigation.
		m.followTail = true
		m.jumpToEnd()
	case "b":
		// Boxes read as a diagram; lanes fit more tasks on screen. Which
		// one is better depends on the graph, so it is a toggle.
		m.boxMode = !m.boxMode
	case "h", "left":
		// Inside the graph, h/l navigate the diagram: the box left or right
		// of the selected one. The graph is two-dimensional, so a direction
		// key has a direction to mean here — making h back out to the run
		// list would leave the diagram the one place where an arrow key
		// exits instead of moving. From the radio, which is a flat list with
		// no horizontal axis, h/l step between panes.
		if m.focus == focusTasks {
			m.moveSibling(-1)
		} else {
			m.focus = focusTasks
		}
	case "l", "right":
		if m.focus == focusTasks {
			m.moveSibling(1)
		} else {
			m.focus = focusRadio
		}
	case "r", "esc":
		// Back to the run list. esc pairs with enter: enter digs into a run,
		// esc backs out of it. Allowed even with a single run — backing out
		// to a one-line list is less surprising than a key that silently
		// does nothing.
		m.picker = true
	case "]", "shift+right":
		if m.runCur+1 < len(m.runs) {
			// selectRun mutates m, so run it before m is copied into the
			// return value — otherwise the switch is silently discarded.
			cmd := m.selectRun(m.runCur + 1)
			return m, cmd
		}
	case "[", "shift+left":
		if m.runCur > 0 {
			cmd := m.selectRun(m.runCur - 1)
			return m, cmd
		}
	}
	return m, nil
}

func (m tuiModel) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.detail = detailClosed
	case "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "j", "down":
		m.detailScroll++
		m.sessionTail = false
	case "k", "up":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
		m.sessionTail = false
	case "ctrl+d", "pgdown":
		m.detailScroll += 10
		m.sessionTail = false
	case "ctrl+u", "pgup":
		m.detailScroll -= 10
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
		m.sessionTail = false
	case "g", "home":
		m.detailScroll = 0
		m.sessionTail = false
	case "f":
		// Re-arm session following, matching what f does on the radio list.
		if m.detail == detailSession {
			m.sessionTail = true
		}
	case "s":
		// Toggle between a task's summary and its session output without
		// closing the detail view.
		switch m.detail {
		case detailTask:
			m.detail = detailSession
			m.detailScroll = 0
			m.sessionTail = true
		case detailSession:
			m.detail = detailTask
			m.detailScroll = 0
		}
	case "a":
		// Attach from the detail view too: this is where you find out the
		// captured log is empty, and where wanting the live session next
		// is most likely.
		if t, ok := m.selectedTask(); ok {
			if cmd := m.attachToSession(t.ID); cmd != nil {
				return m, cmd
			}
			m.err = fmt.Errorf("no recorded session for %s — the run predates session pinning, or the task has not started", t.ID)
		}
	case "n":
		// Walk to the next item without leaving the detail view.
		switch m.detail {
		case detailMessage:
			if m.radioCur < len(m.snap.Messages)-1 {
				m.radioCur++
				m.detailScroll = 0
				m.followTail = false
			}
		case detailTask, detailSession:
			if m.taskCur < len(m.snap.Tasks)-1 {
				m.taskCur++
				m.detailScroll = 0
				m.sessionTail = true
			}
		}
	case "p":
		switch m.detail {
		case detailMessage:
			if m.radioCur > 0 {
				m.radioCur--
				m.detailScroll = 0
				m.followTail = false
			}
		case detailTask, detailSession:
			if m.taskCur > 0 {
				m.taskCur--
				m.detailScroll = 0
				m.sessionTail = true
			}
		}
	}
	return m, nil
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.focus {
	case focusTasks:
		m.taskCur += delta
	case focusRadio:
		m.radioCur += delta
		m.followTail = false
	}
	m.clampCursors()
}

func (m *tuiModel) jumpTo(i int) {
	switch m.focus {
	case focusTasks:
		m.taskCur = i
	case focusRadio:
		m.radioCur = i
		m.followTail = false
	}
	m.clampCursors()
}

func (m *tuiModel) jumpToEnd() {
	switch m.focus {
	case focusTasks:
		m.taskCur = len(m.snap.Tasks) - 1
	case focusRadio:
		m.radioCur = len(m.snap.Messages) - 1
	}
	m.clampCursors()
}

// moveSibling walks the graph horizontally: to the box left or right of the
// selected one in the order the boxes are drawn. In the box view the graph
// is two-dimensional, so h/l are a navigation axis in their own right rather
// than a synonym for "back out" — leaving the graph on h would make the
// diagram the one place where a direction key means "exit".
func (m *tuiModel) moveSibling(delta int) {
	if m.focus != focusTasks {
		return
	}
	order := m.boxNodeOrder()
	if len(order) < 2 {
		return
	}
	at := -1
	for i, n := range order {
		if n == m.taskCur {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	next := at + delta
	if next < 0 || next >= len(order) {
		return
	}
	m.taskCur = order[next]
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
	if m.picker {
		return m.viewPicker()
	}
	switch m.detail {
	case detailMessage:
		return m.viewMessageDetail()
	case detailTask:
		return m.viewTaskDetail()
	case detailSession:
		return m.viewSessionDetail()
	}

	header := m.viewHeader()
	footer := m.viewFooter()
	bodyHeight := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	leftWidth := m.graphPaneWidth()
	rightWidth := m.width - leftWidth - 1
	if rightWidth < 20 {
		rightWidth = 20
	}

	// The right side is split: the top follows the graph cursor, the bottom
	// is the radio. Moving the cursor in the graph used to change nothing on
	// the right, so reading a node meant opening a detail view and backing
	// out of it — for what is usually a one-glance question. The radio stays
	// the whole channel rather than being filtered to the selected node:
	// a channel is the thing everyone is talking on, and messages nobody
	// @-mentions (PLAN_DONE, broadcasts) belong to no node at all.
	inspectHeight := m.inspectorHeight(bodyHeight)
	radioHeight := bodyHeight - inspectHeight
	if radioHeight < 3 {
		radioHeight, inspectHeight = bodyHeight, 0
	}

	left := m.viewTasks(leftWidth, bodyHeight)
	var right string
	if inspectHeight > 0 {
		right = lipgloss.JoinVertical(lipgloss.Left,
			m.viewInspector(rightWidth, inspectHeight),
			m.viewRadio(rightWidth, radioHeight))
	} else {
		right = m.viewRadio(rightWidth, radioHeight)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)

	return header + "\n" + body + "\n" + footer
}

// inspectorHeight splits the right pane between the node inspector and the
// radio. The inspector is a fixed summary — a handful of fields plus a few
// activity lines — so it takes what it needs and the radio, which grows
// without bound, takes the rest. On a short terminal it is dropped entirely
// rather than squeezing the radio to nothing.
func (m tuiModel) inspectorHeight(bodyHeight int) int {
	if len(m.snap.Tasks) == 0 || bodyHeight < 14 {
		return 0
	}
	const want = 9
	if bodyHeight-want < 5 {
		return 0
	}
	return want
}

// graphPaneWidth is the width the graph pane is rendered at. Navigation
// needs it too: moving between sibling boxes means asking the layout which
// boxes share a row, and the layout depends on how wide the pane is. If the
// two disagreed, `l` would step to a box that is not on screen where the
// user sees it.
func (m tuiModel) graphPaneWidth() int {
	// Boxes need room for two borders plus a readable label, so the graph
	// pane gets a wider floor in box mode than the lane gutter needs.
	w := m.width / 3
	minLeft, maxLeft := 24, 44
	if m.boxMode {
		minLeft, maxLeft = 30, 52
	}
	if w < minLeft {
		w = minLeft
	}
	if w > maxLeft {
		w = maxLeft
	}
	return w
}

var (
	styTitle  = lipgloss.NewStyle().Bold(true)
	styDim    = lipgloss.NewStyle().Faint(true)
	styGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styBlue   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	stySel    = lipgloss.NewStyle().Reverse(true)
	styPanel  = lipgloss.NewStyle().Bold(true).Underline(true)
)

// runCountLabel says how many runs there are and how many are still going.
//
// It used to read "N live runs" for a list that is now mostly history —
// which is exactly the kind of label you stop reading because it is
// always wrong.
func (m tuiModel) runCountLabel() string {
	live := 0
	for _, r := range m.runs {
		if r.BrokerURL != "" {
			live++
		}
	}
	switch {
	case len(m.runs) == 0:
		return "no runs"
	case live == 0:
		return fmt.Sprintf("%d finished runs", len(m.runs))
	case live == len(m.runs):
		return fmt.Sprintf("%d live runs", live)
	default:
		return fmt.Sprintf("%d live, %d finished", live, len(m.runs)-live)
	}
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

func (m tuiModel) viewPicker() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n\n",
		styTitle.Render("Narwhal"),
		styDim.Render(m.runCountLabel()))

	if len(m.runs) == 0 {
		b.WriteString(styDim.Render("(no runs yet)\n"))
		return b.String()
	}

	visible := m.height - 5
	if visible < 3 {
		visible = 3
	}
	start := scrollStart(m.runCur, visible, len(m.runs))

	for i := start; i < len(m.runs) && i < start+visible; i++ {
		r := m.runs[i]

		// The run id is a timestamp — useless for telling runs apart. What
		// identifies a run to the operator is when it started, where it is
		// working, and what it was asked to do.
		when := runStartTime(r).Format("01-02 15:04")
		where := abbreviatePath(r.CWD)
		// A finished run has no broker to name. Saying so is the point:
		// the picker mixes live and finished runs, and "which of these is
		// still working" is the first thing you need from the list.
		origin := "daemon"
		switch {
		case r.BrokerURL == "":
			origin = "finished"
		case r.PID > 0:
			origin = fmt.Sprintf("pid %d", r.PID)
		}

		// Truncate in plain text, then style. truncate() counts bytes and
		// rune widths, so handing it a styled string miscounts the escape
		// sequences as content — and cutting one mid-escape drops the reset,
		// which bleeds the style into every row below.
		// Truncate in plain text, then style. truncate() counts bytes and
		// rune widths, so handing it a styled string miscounts the escape
		// sequences as content — and cutting one mid-escape drops the reset,
		// which bleeds the style into every row below.
		marker, rowStyle := "  ", styDim
		if i == m.runCur {
			marker, rowStyle = "▸ ", stySel
		}
		head := fmt.Sprintf("%s%-11s  %-28s  %s", marker, when, where, origin)
		if i == m.runCur {
			head = padRight(head, m.width-1)
		}
		b.WriteString(rowStyle.Render(truncate(head, m.width)) + "\n")

		prompt := strings.ReplaceAll(r.Prompt, "\n", " ")
		if prompt == "" {
			prompt = "(no prompt — " + r.RunID + ")"
		}
		b.WriteString(styDim.Render(truncate("    "+prompt, m.width)) + "\n")
	}

	return pinFooter(strings.TrimRight(b.String(), "\n"),
		styDim.Render("j/k move · enter open · esc quit"), m.height)
}

func (m tuiModel) viewHeader() string {
	icon, state := runGlyph(m.snap.State)
	line := fmt.Sprintf("%s  %s  %s %s",
		styTitle.Render("Narwhal"), m.snap.RunID, icon, state)
	// With several runs live, say which one this is — otherwise the view
	// gives no hint that the others exist.
	if len(m.runs) > 1 {
		line += styDim.Render(fmt.Sprintf("  [%d/%d]", m.runCur+1, len(m.runs)))
	}
	if m.err != nil {
		line += "  " + styRed.Render("(broker unreachable)")
	}
	prompt := m.snap.Prompt
	if w := m.width - 2; w > 10 && len(prompt) > w {
		prompt = prompt[:w-3] + "..."
	}
	if prompt == "" {
		return line
	}
	return line + "\n" + styDim.Render(prompt)
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
	return body + "\n" + hints
}

func (m tuiModel) viewFooter() string {
	counts := map[broker.TaskState]int{}
	for _, t := range m.snap.Tasks {
		counts[t.State]++
	}
	stats := fmt.Sprintf("%s %d  %s %d  %s %d  %s %d",
		styGreen.Render("done"), counts[broker.TaskCompleted],
		styCyan.Render("running"), counts[broker.TaskDispatched],
		styYellow.Render("ready"), counts[broker.TaskReady],
		styRed.Render("failed"), counts[broker.TaskFailed])

	tail := ""
	if m.followTail {
		tail = styDim.Render("  [following]")
	}
	keys := "tab pane · hjkl move · enter detail · s session · a attach · esc runs · q quit"
	if len(m.runs) > 1 {
		keys = "tab pane · hjkl move · enter detail · s session · a attach · [ ] run · q quit"
	}
	return stats + tail + "\n" + styDim.Render(keys)
}

func (m tuiModel) viewTasks(width, height int) string {
	title := paneTitle("Graph", m.focus == focusTasks, width)

	if m.boxMode {
		return m.viewTasksBoxed(title, width, height)
	}

	rows := m.graphRows()
	out := make([]string, 0, height)
	out = append(out, title)

	visible := height - 1
	start := scrollStart(m.taskCur, visible, len(rows))
	for i := start; i < len(rows) && len(out) < height; i++ {
		out = append(out, m.taskRow(rows[i], width, i == m.taskCur && m.focus == focusTasks))
	}
	if len(rows) == 0 {
		out = append(out, styDim.Render("(no tasks yet)"))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(out, "\n"))
}

// viewTasksBoxed renders the box view. Scrolling works in rendered lines
// rather than tasks, because one task is several lines; the cursor still
// selects a task, and the window is positioned to keep its box on screen.
func (m tuiModel) viewTasksBoxed(title string, width, height int) string {
	rows := m.boxRows(width)
	out := make([]string, 0, height)
	out = append(out, title)

	if len(rows) == 0 {
		out = append(out, styDim.Render("(no tasks yet)"))
		return lipgloss.NewStyle().Width(width).Render(strings.Join(out, "\n"))
	}

	// Find the first line of the selected task's box so the window can be
	// anchored on it.
	anchor := 0
	for i, r := range rows {
		if r.owns(m.taskCur) {
			anchor = i
			break
		}
	}

	visible := height - 1
	start := scrollStart(anchor, visible, len(rows))
	for i := start; i < len(rows) && len(out) < height; i++ {
		r := rows[i]
		selected := -1
		if m.focus == focusTasks {
			selected = m.taskCur
		}
		out = append(out, m.styleBoxLine(r, truncate(r.text, width), selected))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(out, "\n"))
}

// styleBoxLine colors one rendered line. Each box on the line gets its own
// task's state color, the selected box is reversed, and everything between
// boxes (edges, margins) stays dim. Coloring per span rather than per line
// matters because siblings share a row: styling the whole line would give
// three boxes the first one's state, and highlight all three when one is
// selected. selected is -1 when the graph pane does not have focus.
func (m tuiModel) styleBoxLine(r boxRow, line string, selected int) string {
	if len(r.spans) == 0 {
		return styDim.Render(line)
	}

	runes := []rune(line)
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
			b.WriteString(styDim.Render(string(runes[at:x0])))
		}
		seg := string(runes[x0:x1])
		switch {
		case s.node == selected:
			b.WriteString(stySel.Render(seg))
		case s.part == partBody:
			_, style := taskIconStyle(m.taskByIndex(s.node).State)
			b.WriteString(style.Render(seg))
		default:
			// Borders stay dim so the graph does not fight the content.
			b.WriteString(styDim.Render(seg))
		}
		at = x1
	}
	if at < len(runes) {
		b.WriteString(styDim.Render(string(runes[at:])))
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
	return layoutGraph(m.sortedTasks()).renderBoxes(width, taskIconPlain)
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
		return stySel.Render(padRight(truncate(plain, width), width))
	}
	if displayWidth(plain) > width {
		head := r.gutter + " " + icon + " "
		label = truncate(label, width-displayWidth(head))
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
func paneTitle(label string, focused bool, width int) string {
	styled := styDim.Render(label)
	if focused {
		styled = styPanel.Render(label)
	}
	rule := width - displayWidth(label) - 2
	if rule < 1 {
		return styled
	}
	return styled + " " + styDim.Render(strings.Repeat("─", rule))
}

func (m tuiModel) viewRadio(width, height int) string {
	title := paneTitle(fmt.Sprintf("Radio (%d)", len(m.snap.Messages)),
		m.focus == focusRadio, width)

	rows := make([]string, 0, height)
	rows = append(rows, title)

	msgs := m.snap.Messages
	visible := height - 1
	start := scrollStart(m.radioCur, visible, len(msgs))
	for i := start; i < len(msgs) && len(rows) < height; i++ {
		selected := i == m.radioCur && m.focus == focusRadio
		rows = append(rows, m.radioRow(msgs[i], width, selected))
	}
	if len(msgs) == 0 {
		rows = append(rows, styDim.Render("(no messages yet)"))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(rows, "\n"))
}

// radioRow renders one message line, fitted to width.
//
// The line is assembled and truncated as plain text first, then styled.
// Doing it the other way round means measuring and cutting a string full of
// ANSI escapes, which is what made every frame cost ~34ms.
func (m tuiModel) radioRow(msg *broker.Message, width int, selected bool) string {
	prioCh, prioStyle := priorityGlyph(msg.Priority)
	sender := shortName(msg.Sender)
	// The time is what turns a list into a channel: it says whether two
	// findings landed together or an hour apart, which the sequence number
	// alone cannot.
	stamp := msg.CreatedAt.Format("15:04:05")
	prefix := fmt.Sprintf("%s %s %s ", stamp, prioCh, sender)

	// The mention marker is short and fixed; keeping it unstyled in the row
	// body avoids re-styling a fragment truncation cut in half.
	var body strings.Builder
	if len(msg.Mentions) > 0 {
		body.WriteString("→" + strings.Join(msg.Mentions, ",") + " ")
	}
	// Protocol messages are a wire format, not prose. Rendered raw they
	// read as noise — FILE_CLAIM|api|internal/api/router.go tells you a
	// worker claimed a file only after you split it on pipes yourself.
	summary, isProtocol := radioSummary(msg.Content)
	if isProtocol {
		// These summaries lead with the task they concern, which is
		// usually the sender: "api  ⋮ api claims router.go" says it twice.
		summary = strings.TrimPrefix(summary, sender+" ")
		body.WriteString("⋮ ")
	}
	body.WriteString(summary)

	rest := truncate(body.String(), width-displayWidth(prefix))

	if selected {
		// A reversed row reads better without competing colors inside it.
		return stySel.Render(padRight(prefix+rest, width))
	}
	if isProtocol {
		// Coordination traffic is context for the prose around it, so it
		// recedes rather than competing with a worker's actual finding.
		rest = styDim.Render(rest)
	}
	return fmt.Sprintf("%s %s %s %s",
		styDim.Render(stamp), prioStyle.Render(prioCh), styBlue.Render(sender), rest)
}

func priorityGlyph(p broker.Priority) (string, lipgloss.Style) {
	switch p {
	case broker.PriorityUrgent:
		return icons.prioUrgent, styRed
	case broker.PriorityFYI:
		return icons.prioFYI, styDim
	default:
		return icons.prioNormal, styDim
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

// viewSessionDetail shows what a worker is doing, as a live activity feed
// read from its Claude session transcript.
//
// The transcript rather than the captured stdout log, because the log is
// empty for the entire time you would want to watch: a `--print` worker
// buffers its output until it exits. The transcript is appended as the
// session happens, so a running worker's tool calls and reasoning show up
// within a second. Once the worker finishes, its final answer is appended
// to the feed — that is what the log holds, and it is the last thing said
// rather than a separate view.
func (m tuiModel) viewSessionDetail() string {
	t, ok := m.selectedTask()
	if !ok {
		return "no tasks"
	}

	icon, style := taskIconStyle(t.State)
	head := fmt.Sprintf("%s  %s %s  %s",
		styTitle.Render("Session"), style.Render(icon), t.ID, style.Render(string(t.State)))

	meta := []string{"agent=worker-" + t.ID}
	if t.Model != "" {
		meta = append(meta, "model="+t.Model)
	}
	if t.Dispatches > 0 {
		meta = append(meta, fmt.Sprintf("dispatches=%d", t.Dispatches))
	}

	width := m.width - 2
	var body []string

	entries := m.workerActivity(t.ID)
	if len(entries) > 0 {
		meta = append(meta, fmt.Sprintf("%d events", len(entries)))
		body = renderTranscript(entries, width)
	}

	// The final answer only exists once the worker has exited. Append it
	// rather than showing it instead: it is the end of the same story.
	if final := m.workerOutputLines(t.ID); len(final) > 0 {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, styDim.Render("── final answer ──"))
		for _, l := range final {
			body = append(body, wrapText(l, width)...)
		}
	}

	if len(body) == 0 {
		// Say which is true rather than leaving a blank pane.
		var reason string
		switch {
		case t.Dispatches == 0:
			reason = "not dispatched yet — nothing to show"
		case t.State == broker.TaskDispatched:
			reason = "the worker has started but has not written its first event yet"
		default:
			reason = "no transcript found. This run predates session pinning, so " +
				"the worker's session cannot be located; only its final output " +
				"would be available, at " + m.sessionLogPath(t.ID)
		}
		body = wrapText(reason, width)
	}

	avail := m.height - 5
	if avail < 3 {
		avail = 3
	}
	// Following pins the view to the end as the worker works.
	if m.sessionTail && len(body) > avail {
		m.detailScroll = len(body) - avail
	}
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))

	follow := "  [following]"
	if !m.sessionTail {
		follow = ""
	}
	footer := styDim.Render("j/k scroll · n/p task · f follow · a attach · esc back · q close")

	return pinFooter(head+pos+styDim.Render(follow)+"\n"+
		styDim.Render(strings.Join(meta, "  "))+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}

// workerActivity reads the selected worker's session transcript.
func (m tuiModel) workerActivity(taskID string) []transcriptEntry {
	sid := m.workerSessionID(taskID)
	if sid == "" {
		return nil
	}
	return readTranscript(transcriptPath(m.live.CWD, sid))
}

// viewTaskDetail shows the selected task's state, its place in the graph,
// and its full assignment — which the graph pane can only show truncated.
// If the task has a running or completed worker, the tail of its
// claude-output.txt is appended so the operator can see what the worker is
// actually doing without leaving the monitor.
func (m tuiModel) viewTaskDetail() string {
	rows := m.graphRows()
	if len(rows) == 0 {
		return "no tasks"
	}
	cur := m.taskCur
	if cur >= len(rows) {
		cur = len(rows) - 1
	}
	t := m.taskByID(rows[cur].id)

	icon, style := taskIconStyle(t.State)
	head := fmt.Sprintf("%s  %s %s  %s",
		styTitle.Render("Task"), style.Render(icon), t.ID, style.Render(string(t.State)))

	var meta []string
	if t.Name != "" && t.Name != t.ID {
		meta = append(meta, "name="+t.Name)
	}
	meta = append(meta, fmt.Sprintf("dispatches=%d", t.Dispatches))
	if len(t.Deps) > 0 {
		meta = append(meta, "waits on "+strings.Join(t.Deps, ", "))
	}
	// Downstream tasks are not stored on the task, so derive them.
	var blocks []string
	for _, other := range m.snap.Tasks {
		for _, d := range other.Deps {
			if d == t.ID {
				blocks = append(blocks, other.ID)
				break
			}
		}
	}
	sort.Strings(blocks)
	if len(blocks) > 0 {
		meta = append(meta, "blocks "+strings.Join(blocks, ", "))
	}

	body := wrapText(t.Assignment, m.width-2)

	// Append the last few things the worker did. This is a preview — `s`
	// opens the full activity feed. The tail comes from the transcript, so
	// it says something while the worker is still running; the captured
	// log only exists after it exits.
	if tail := m.workerActivityTail(t.ID, m.width-2); len(tail) > 0 {
		body = append(body, "",
			styDim.Render("── recent activity (press s for the full session) ──"))
		body = append(body, tail...)
	}

	footer := styDim.Render("j/k scroll · n/p task · s session · esc back · q close")

	avail := m.height - 5
	if avail < 3 {
		avail = 3
	}
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))

	return pinFooter(head+pos+"\n"+styDim.Render(strings.Join(meta, "  "))+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}

// sessionLogPath returns where the launcher writes a worker's Claude output:
// ~/.narwhal/sessions/<run-id>/agents/worker-<task-id>/claude-output.txt.
func (m tuiModel) sessionLogPath(taskID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".narwhal", "sessions", m.snap.RunID,
		"agents", "worker-"+taskID, "claude-output.txt")
}

// sessionIDPath returns where the launcher records a worker's Claude
// session UUID.
func (m tuiModel) sessionIDPath(taskID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".narwhal", "sessions", m.snap.RunID,
		"agents", "worker-"+taskID, "claude-session-id")
}

// workerSessionID reads the Claude session UUID a worker runs under, or ""
// when it is not recorded — a run started before the launcher began pinning
// session ids, or a task that has not been dispatched.
func (m tuiModel) workerSessionID(taskID string) string {
	path := m.sessionIDPath(taskID)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// attachDoneMsg reports back when an attached session exits.
type attachDoneMsg struct{ err error }

// attachToSession suspends the monitor and opens the worker's Claude
// session, restoring the TUI when it exits.
//
// Opening the real session is the only way to watch a running worker:
// `claude --print` buffers its output until it finishes, so the captured
// log is empty for the whole run, while the session transcript is written
// continuously. --fork-session leaves the worker's own conversation
// untouched — attaching is for watching, and sharing the session id would
// have the monitor and the worker appending to one transcript.
func (m tuiModel) attachToSession(taskID string) tea.Cmd {
	sid := m.workerSessionID(taskID)
	if sid == "" {
		return nil
	}
	c := exec.Command("claude", "--resume", sid, "--fork-session")
	// The transcript is filed under the directory the worker ran in, so
	// resuming from anywhere else does not find it.
	c.Dir = m.live.CWD
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return attachDoneMsg{err: err}
	})
}

// workerOutputLines reads a worker's whole session log, newest last.
// Returns nil when the file does not exist yet — the task has not been
// dispatched, or this run keeps its sessions elsewhere.
func (m tuiModel) workerOutputLines(taskID string) []string {
	path := m.sessionLogPath(taskID)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// workerActivityTail renders the last few things a worker did, for the
// preview inside the task detail. It falls back to the final output for
// runs old enough to have no transcript.
func (m tuiModel) workerActivityTail(taskID string, width int) []string {
	const tail = 12
	if entries := m.workerActivity(taskID); len(entries) > 0 {
		if len(entries) > tail {
			entries = entries[len(entries)-tail:]
		}
		return renderTranscript(entries, width)
	}
	lines := m.workerOutputLines(taskID)
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines
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

func (m tuiModel) viewMessageDetail() string {
	if len(m.snap.Messages) == 0 {
		return "no messages"
	}
	msg := m.snap.Messages[m.radioCur]

	head := fmt.Sprintf("%s  seq %d  %s → %s",
		styTitle.Render("Message"), msg.Seq,
		styBlue.Render(msg.Sender), mentionsLabel(msg.Mentions))
	meta := styDim.Render(fmt.Sprintf("thread=%s  priority=%s  %s",
		msg.ThreadID, msg.Priority, msg.CreatedAt.Format("15:04:05")))

	body := wrapText(msg.Content, m.width-2)
	footer := styDim.Render("j/k scroll · n/p message · esc back · q close")

	avail := m.height - 4
	if avail < 3 {
		avail = 3
	}
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))

	return pinFooter(head+pos+"\n"+meta+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}

func mentionsLabel(m []string) string {
	if len(m) == 0 {
		return styDim.Render("(broadcast)")
	}
	return strings.Join(m, ", ")
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
func shortName(s string) string {
	return strings.TrimPrefix(s, "worker-")
}

// truncate cuts s to w display columns.
//
// This is on the hot path: every visible row is truncated on every frame,
// and a frame happens on every keystroke. The obvious implementation —
// drop one rune at a time and re-measure — costs ~0.9ms on a long styled
// line because each measurement re-parses ANSI escapes, which put whole-View
// latency at 34ms and made navigation feel sluggish.
//
// Instead: fast-path pure-ASCII strings (the common case) with a byte slice,
// and otherwise accumulate rune widths in a single pass.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if isASCII(s) {
		if len(s) <= w {
			return s
		}
		return s[:w]
	}
	total := 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if total+rw > w {
			return s[:i]
		}
		total += rw
	}
	return s
}

// isASCII reports whether s is plain ASCII with no escape sequences, in
// which case byte length equals display width.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] == 0x1b {
			return false
		}
	}
	return true
}

// displayWidth is the column count of s, using the same fast path.
func displayWidth(s string) int {
	if isASCII(s) {
		return len(s)
	}
	return runewidth.StringWidth(s)
}

func padRight(s string, w int) string {
	gap := w - displayWidth(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// wrapText breaks content into display lines, preserving existing newlines.
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		for len(para) > width {
			cut := strings.LastIndex(para[:width], " ")
			if cut <= 0 {
				cut = width
			}
			out = append(out, para[:cut])
			para = strings.TrimLeft(para[cut:], " ")
		}
		out = append(out, para)
	}
	return out
}
