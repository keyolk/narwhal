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

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

type focusPane int

const (
	focusTasks focusPane = iota
	focusRadio
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
	detail     bool
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
	if m.detail {
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
		if m.focus == focusRadio && len(m.snap.Messages) > 0 {
			m.detail = true
			m.detailScroll = 0
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
		m.detail = false
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
		// Next message without leaving the detail view.
		if m.radioCur < len(m.snap.Messages)-1 {
			m.radioCur++
			m.detailScroll = 0
			m.followTail = false
		}
	case "p":
		if m.radioCur > 0 {
			m.radioCur--
			m.detailScroll = 0
			m.followTail = false
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
	if m.detail {
		return m.viewDetail()
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
	state := string(m.snap.State)
	switch m.snap.State {
	case broker.RunDone:
		state = styGreen.Render(state)
	case broker.RunFailed, broker.RunCanceled:
		state = styRed.Render(state)
	default:
		state = styCyan.Render(state)
	}
	line := fmt.Sprintf("%s  %s  %s", styTitle.Render("Narwhal"), m.snap.RunID, state)
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
	title := "Tasks"
	if m.focus == focusTasks {
		title = styPanel.Render(title)
	} else {
		title = styDim.Render(title)
	}

	tasks := m.sortedTasks()
	rows := make([]string, 0, height)
	rows = append(rows, title)

	visible := height - 1
	start := scrollStart(m.taskCur, visible, len(tasks))
	for i := start; i < len(tasks) && len(rows) < height; i++ {
		t := tasks[i]
		icon, style := taskIconStyle(t.State)
		name := t.ID
		if t.Dispatches > 1 {
			name += fmt.Sprintf(" (%d)", t.Dispatches)
		}
		line := fmt.Sprintf("%s %s", style.Render(icon), name)
		if len(t.Deps) > 0 {
			line += styDim.Render(" ←" + strings.Join(t.Deps, ","))
		}
		line = truncate(line, width)
		if i == m.taskCur && m.focus == focusTasks {
			line = stySel.Render(padRight(line, width))
		}
		rows = append(rows, line)
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(rows, "\n"))
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
		line := truncate(m.radioLine(msgs[i]), width)
		if i == m.radioCur && m.focus == focusRadio {
			line = stySel.Render(padRight(line, width))
		}
		rows = append(rows, line)
	}
	if len(msgs) == 0 {
		rows = append(rows, styDim.Render("(no messages yet)"))
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(rows, "\n"))
}

func (m tuiModel) radioLine(msg *broker.Message) string {
	prio := styDim.Render("·")
	switch msg.Priority {
	case broker.PriorityUrgent:
		prio = styRed.Render("!")
	case broker.PriorityFYI:
		prio = styDim.Render("i")
	}
	tag := ""
	if _, _, _, _, ok := broker.ParseSplitRequest(msg.Content); ok {
		tag = styYellow.Render("[split] ")
	}
	to := ""
	if len(msg.Mentions) > 0 {
		to = styDim.Render("→" + strings.Join(msg.Mentions, ",") + " ")
	}
	content := strings.ReplaceAll(msg.Content, "\n", " ")
	return fmt.Sprintf("%s%3d %s %s %s%s%s",
		"", msg.Seq, prio, styBlue.Render(shortName(msg.Sender)), to, tag, content)
}

func (m tuiModel) viewDetail() string {
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
	if m.detailScroll > len(body)-1 {
		m.detailScroll = len(body) - 1
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	end := m.detailScroll + avail
	if end > len(body) {
		end = len(body)
	}
	pos := ""
	if len(body) > avail {
		pos = styDim.Render(fmt.Sprintf("  [%d-%d/%d]", m.detailScroll+1, end, len(body)))
	}

	return head + pos + "\n" + meta + "\n\n" +
		strings.Join(body[m.detailScroll:end], "\n") + "\n" + footer
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
		return "✓", styGreen
	case broker.TaskDispatched:
		return "▶", styCyan
	case broker.TaskReady:
		return "○", styYellow
	case broker.TaskFailed:
		return "✗", styRed
	case broker.TaskBlocked:
		return "⊘", styRed
	default:
		return "·", styDim
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

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Trim by runes until it fits; ANSI-aware width keeps styled text honest.
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w {
		r = r[:len(r)-1]
	}
	return string(r)
}

func padRight(s string, w int) string {
	gap := w - lipgloss.Width(s)
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
