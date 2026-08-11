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
	"sort"
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
)

// tickMsg drives the poll loop.
type tickMsg time.Time

// snapshotMsg carries a fresh poll result.
type snapshotMsg struct {
	snap   broker.Snapshot
	agents []string
	err    error
}

type tuiModel struct {
	live     store.LiveRun
	interval time.Duration
	client   *http.Client

	snap   broker.Snapshot
	agents []string
	err    error

	focus     focusPane
	taskCur   int
	radioCur  int
	// followTail keeps the radio list pinned to the newest message. Any
	// manual navigation releases it, so reading an older message is not
	// yanked away by the next poll.
	followTail bool
	detail     detailMode
	detailScroll int

	width  int
	height int
	quit   bool
}

func newTUIModel(live store.LiveRun, interval time.Duration) tuiModel {
	return tuiModel{
		live:       live,
		interval:   interval,
		client:     &http.Client{Timeout: 5 * time.Second},
		focus:      focusRadio,
		followTail: true,
		width:      100,
		height:     30,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.poll(), tick(m.interval))
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m tuiModel) poll() tea.Cmd {
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

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.poll(), tick(m.interval))

	case snapshotMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.snap = msg.snap
		m.agents = msg.agents
		if m.followTail && len(m.snap.Messages) > 0 {
			m.radioCur = len(m.snap.Messages) - 1
		}
		m.clampCursors()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	case "f":
		// Re-arm tail following after manual navigation.
		m.followTail = true
		m.jumpToEnd()
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
	case "k", "up":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "ctrl+d", "pgdown":
		m.detailScroll += 10
	case "ctrl+u", "pgup":
		m.detailScroll -= 10
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
	case "g", "home":
		m.detailScroll = 0
	case "n":
		// Walk to the next item without leaving the detail view.
		switch m.detail {
		case detailMessage:
			if m.radioCur < len(m.snap.Messages)-1 {
				m.radioCur++
				m.detailScroll = 0
				m.followTail = false
			}
		case detailTask:
			if m.taskCur < len(m.snap.Tasks)-1 {
				m.taskCur++
				m.detailScroll = 0
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
		case detailTask:
			if m.taskCur > 0 {
				m.taskCur--
				m.detailScroll = 0
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
	switch m.detail {
	case detailMessage:
		return m.viewMessageDetail()
	case detailTask:
		return m.viewTaskDetail()
	}

	header := m.viewHeader()
	footer := m.viewFooter()
	bodyHeight := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	leftWidth := m.width / 3
	if leftWidth < 24 {
		leftWidth = 24
	}
	if leftWidth > 44 {
		leftWidth = 44
	}
	rightWidth := m.width - leftWidth - 1
	if rightWidth < 20 {
		rightWidth = 20
	}

	left := m.viewTasks(leftWidth, bodyHeight)
	right := m.viewRadio(rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)

	return header + "\n" + body + "\n" + footer
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

func (m tuiModel) viewHeader() string {
	icon, state := runGlyph(m.snap.State)
	line := fmt.Sprintf("%s  %s  %s %s",
		styTitle.Render("Narwhal"), m.snap.RunID, icon, state)
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
	keys := styDim.Render("tab pane · j/k move · enter detail · f follow · q quit")
	return stats + tail + "\n" + keys
}


func (m tuiModel) viewTasks(width, height int) string {
	title := "Graph"
	if m.focus == focusTasks {
		title = styPanel.Render(title)
	} else {
		title = styDim.Render(title)
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

func (m tuiModel) viewRadio(width, height int) string {
	title := fmt.Sprintf("Radio (%d)", len(m.snap.Messages))
	if m.focus == focusRadio {
		title = styPanel.Render(title)
	} else {
		title = styDim.Render(title)
	}

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
	prefix := fmt.Sprintf("%3d %s %s ", msg.Seq, prioCh, sender)

	// The mention/split markers are short and fixed; keeping them unstyled
	// in the row body avoids re-styling a fragment truncation cut in half.
	var body strings.Builder
	if len(msg.Mentions) > 0 {
		body.WriteString("→" + strings.Join(msg.Mentions, ",") + " ")
	}
	if _, _, _, _, ok := broker.ParseSplitRequest(msg.Content); ok {
		body.WriteString("[split] ")
	}
	body.WriteString(strings.ReplaceAll(msg.Content, "\n", " "))

	rest := truncate(body.String(), width-displayWidth(prefix))

	if selected {
		// A reversed row reads better without competing colors inside it.
		return stySel.Render(padRight(prefix+rest, width))
	}
	return fmt.Sprintf("%3d %s %s %s",
		msg.Seq, prioStyle.Render(prioCh), styBlue.Render(sender), rest)
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

// viewTaskDetail shows the selected task's state, its place in the graph,
// and its full assignment — which the graph pane can only show truncated.
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
	footer := styDim.Render("j/k scroll · n/p task · esc back · q close")

	avail := m.height - 5
	if avail < 3 {
		avail = 3
	}
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))

	return head + pos + "\n" + styDim.Render(strings.Join(meta, "  ")) + "\n\n" +
		strings.Join(body[scroll:end], "\n") + "\n" + footer
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

	return head + pos + "\n" + meta + "\n\n" +
		strings.Join(body[scroll:end], "\n") + "\n" + footer
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
