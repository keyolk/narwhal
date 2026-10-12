// taskview.go draws a task as a list of entries that can each be opened.
//
// The task view was one block of lines scrolled as a whole, and the long
// parts were cut to fit it: a tool call to its one-line summary, a tool
// result to two lines and a count, the check and its result to a line
// each. What a worker actually ran, or what it got back, was in the
// transcript and nowhere on screen.
//
// Now the cursor is on an entry, not a line. j/k step between entries,
// enter opens or closes the one under the cursor in place — a call's full
// input, a result in full, a long paragraph folded back — E does that for
// all of them, and o hands the entry to $PAGER for anything too long to
// read in a pane.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/narwhal/internal/broker"
)

// Entry kinds that belong to the task view rather than the transcript.
const (
	kindCheck = "check" // the end condition, its result in detail
	kindFinal = "final" // the final answer, or why it failed
)

// expandedCap bounds an opened entry in the pane; o reads the rest.
const expandedCap = 400

// taskFeed is everything the task view lists, in order: the check, the
// transcript with the operator's instructions and the judge's calls placed
// at their times, and the final answer.
func (m tuiModel) taskFeed(t broker.TaskSnapshot) []transcriptEntry {
	var out []transcriptEntry
	if c := strings.TrimSpace(t.Check); c != "" {
		out = append(out, transcriptEntry{kind: kindCheck, text: c, detail: strings.TrimSpace(t.CheckResult)})
	}
	var feed []transcriptEntry
	if sid := m.workerSessionID(t.ID); sid != "" {
		feed = mergeFeed(globalTranscripts.read(transcriptPath(m.live.CWD, sid)), m.taskInputs(t.ID))
	}
	if len(feed) == 0 && strings.TrimSpace(t.Assignment) != "" {
		feed = []transcriptEntry{{kind: "text", user: true, text: t.Assignment}}
	}
	out = append(out, feed...)
	// Two sources say the final answer, and they outlive each other:
	// claude-output.txt is this machine's capture, the snapshot outcome
	// what the worker declared at task-done and what survives a restart.
	final := strings.Join(m.workerOutputLines(t.ID), "\n")
	if strings.TrimSpace(final) == "" {
		final = t.Outcome
	}
	if strings.TrimSpace(final) != "" {
		e := transcriptEntry{at: t.EndedAt, kind: kindFinal, text: final}
		e.user = t.State == broker.TaskFailed // reused: failed
		out = append(out, e)
	}
	return out
}

// entryKey identifies an entry across polls, for what has been opened.
func entryKey(e transcriptEntry) string {
	head := e.text
	if len(head) > 48 {
		head = head[:48]
	}
	return fmt.Sprintf("%d|%s|%d|%s", e.at.UnixNano(), e.kind, len(e.text), head)
}

// foldsByDefault reports whether an entry starts closed: the long,
// mechanical ones. Prose, instructions and the answer start open.
func foldsByDefault(e transcriptEntry) bool {
	switch e.kind {
	case "tool", "result", kindCheck:
		return true
	}
	return false
}

// entryOpen reports whether an entry is drawn opened.
func (m tuiModel) entryOpen(e transcriptEntry) bool {
	open := !foldsByDefault(e) || m.taskOpenAll
	if m.taskToggled[entryKey(e)] {
		open = !open
	}
	return open
}

// entryRenderCache memoizes rendered entries: the view is drawn on every
// animation frame, and a long transcript is hundreds of entries.
var entryRenderCache = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

// renderTaskEntry draws one entry, opened or folded.
func renderTaskEntry(e transcriptEntry, width int, open bool, st *feedState) []string {
	key := fmt.Sprintf("%s|%d|%v|%v", entryKey(e), width, open, st.sawPrompt)
	entryRenderCache.Lock()
	if c, ok := entryRenderCache.m[key]; ok {
		entryRenderCache.Unlock()
		if e.kind == "text" && e.user {
			st.sawPrompt = true
		}
		return c
	}
	entryRenderCache.Unlock()

	out := renderTaskEntryUncached(e, width, open, st)

	entryRenderCache.Lock()
	if len(entryRenderCache.m) > 20000 {
		entryRenderCache.m = map[string][]string{}
	}
	entryRenderCache.m[key] = out
	entryRenderCache.Unlock()
	return out
}

func renderTaskEntryUncached(e transcriptEntry, width int, open bool, st *feedState) []string {
	stamp := ""
	if !e.at.IsZero() {
		stamp = clock(e.at) + " "
	}
	pad := strings.Repeat(" ", len(stamp)) + "  "
	inner := max(10, width-len(pad))
	capped := func(lines []string) []string {
		if len(lines) > expandedCap {
			n := len(lines) - expandedCap
			lines = append(lines[:expandedCap:expandedCap], styDim.Render(fmt.Sprintf("… %d more lines — o to read all", n)))
		}
		return lines
	}
	more := func(n int) string {
		return pad + styDim.Render(fmt.Sprintf("… %d more lines — enter to open", n))
	}
	switch e.kind {
	case "tool":
		head := styDim.Render(stamp) + styCyan.Render("→ ") + styCyan.Render(truncate(e.text, width-len(stamp)-2))
		if !open || strings.TrimSpace(e.detail) == "" {
			return []string{head}
		}
		out := []string{head}
		for _, l := range capped(wrapText(e.detail, inner)) {
			out = append(out, pad+styDim.Render("│ ")+l)
		}
		return out
	case "result":
		lines := strings.Split(strings.TrimRight(e.text, "\n"), "\n")
		if !open {
			var out []string
			shown := 0
			for _, l := range lines {
				if strings.TrimSpace(l) == "" {
					continue
				}
				if shown == 2 {
					break
				}
				out = append(out, pad+styDim.Render(truncate(oneLine(l), width-len(pad))))
				shown++
			}
			if rest := len(lines) - shown; rest > 0 && shown == 2 {
				out = append(out, more(rest))
			}
			return out
		}
		var out []string
		for _, l := range capped(wrapText(strings.Join(lines, "\n"), inner)) {
			out = append(out, pad+styDim.Render(l))
		}
		return out
	case kindCheck:
		head := styDim.Render(pad[:len(pad)-2]) + styMagenta.Render("◎ ") + styMagenta.Render("check")
		body := renderMarkdown(e.text, inner, lipgloss.NewStyle())
		res := []string{pad + styDim.Render("not answered yet")}
		if e.detail != "" {
			res = renderMarkdown(e.detail, inner-2, lipgloss.NewStyle())
		}
		if !open {
			out := []string{head + "  " + truncate(flattenMarkdown(e.text), width-12)}
			if e.detail != "" {
				out = append(out, pad+styDim.Render("↳ ")+truncate(flattenMarkdown(e.detail), inner-2))
			}
			if len(body) > 1 || len(res) > 1 {
				out = append(out, more(len(body)+len(res)-2))
			}
			return out
		}
		out := []string{head}
		for _, l := range body {
			out = append(out, pad+l)
		}
		for i, l := range res {
			lead := "  "
			if i == 0 {
				lead = styDim.Render("↳ ")
			}
			out = append(out, pad+lead+l)
		}
		return out
	case kindFinal:
		label, style := "── final answer ──", styGreen
		if e.user {
			label, style = "── why it failed ──", styRedBold
		}
		body := renderMarkdown(e.text, width, lipgloss.NewStyle())
		out := []string{"", style.Render(label)}
		if !open && len(body) > 6 {
			return append(append(out, body[:6]...), styDim.Render(fmt.Sprintf("… %d more lines — enter to open", len(body)-6)))
		}
		return append(out, capped(body)...)
	}
	// Prose: the worker's text and thinking, the task, the operator's
	// words. Open by default; folded, three lines.
	out := renderEntry(e, width, st)
	if !open && len(out) > 3 {
		return append(out[:3:3], more(len(out)-3))
	}
	return capped(out)
}

// taskLayout is the rendered feed with where each entry starts.
type taskLayout struct {
	entries []transcriptEntry
	lines   []string
	starts  []int // first line of entry i; len(entries)+1 with the end
}

func (m tuiModel) layoutTask(t broker.TaskSnapshot, width int) taskLayout {
	l := taskLayout{entries: m.taskFeed(t)}
	var st feedState
	for _, e := range l.entries {
		l.starts = append(l.starts, len(l.lines))
		l.lines = append(l.lines, renderTaskEntry(e, width-1, m.entryOpen(e), &st)...)
	}
	l.starts = append(l.starts, len(l.lines))
	return l
}

// selectedEntry resolves taskSel against the feed: following, the last.
func (m tuiModel) selectedEntry(n int) int {
	if n == 0 {
		return -1
	}
	if m.taskSel < 0 || m.taskSel >= n {
		return n - 1
	}
	return m.taskSel
}

// taskViewRows is the rows the feed has, for keeping the cursor on screen.
func (m tuiModel) taskViewRows(t broker.TaskSnapshot) int {
	return max(1, m.height-6)
}

// moveEntry steps the cursor and scrolls the entry into view.
func (m *tuiModel) moveEntry(delta int) {
	t, ok := m.selectedTask()
	if !ok {
		return
	}
	l := m.layoutTask(t, m.width-2)
	n := len(l.entries)
	if n == 0 {
		return
	}
	i := min(max(m.selectedEntry(n)+delta, 0), n-1)
	m.taskSel, m.sessionTail = i, false
	m.revealEntry(l, i, m.taskViewRows(t))
}

// revealEntry scrolls so entry i is on screen: its start, or as much of a
// tall one as fits, with two lines of what came before when there is room.
func (m *tuiModel) revealEntry(l taskLayout, i, rows int) {
	start, end := l.starts[i], l.starts[i+1]
	switch {
	case start < m.detailScroll:
		m.detailScroll = max(0, start-2)
	case end > m.detailScroll+rows:
		m.detailScroll = max(0, min(start-2, end-rows))
	}
}

// toggleEntry opens or folds the entry under the cursor.
func (m *tuiModel) toggleEntry() {
	t, ok := m.selectedTask()
	if !ok {
		return
	}
	l := m.layoutTask(t, m.width-2)
	i := m.selectedEntry(len(l.entries))
	if i < 0 {
		return
	}
	k := entryKey(l.entries[i])
	if m.taskToggled == nil {
		m.taskToggled = map[string]bool{}
	}
	if m.taskToggled[k] {
		delete(m.taskToggled, k)
	} else {
		m.taskToggled[k] = true
	}
	m.taskSel, m.sessionTail = i, false
	m.revealEntry(m.layoutTask(t, m.width-2), i, m.taskViewRows(t))
}

// scrollTask pages the feed by lines and puts the cursor on the entry at
// the top of the new window, so the two never disagree.
func (m *tuiModel) scrollTask(delta int) {
	t, ok := m.selectedTask()
	if !ok {
		return
	}
	l := m.layoutTask(t, m.width-2)
	rows := m.taskViewRows(t)
	m.detailScroll = min(max(0, m.detailScroll+delta), max(0, len(l.lines)-rows))
	m.sessionTail = false
	for i := range l.entries {
		if l.starts[i+1] > m.detailScroll {
			m.taskSel = i
			return
		}
	}
}

// entryAt is the first entry at or after at, for opening at a moment.
func (m tuiModel) entryAt(t broker.TaskSnapshot, at time.Time) int {
	feed := m.taskFeed(t)
	for i, e := range feed {
		if !e.at.IsZero() && !e.at.Before(at) {
			return i
		}
	}
	return len(feed) - 1
}

// pageEntry hands the entry under the cursor to $PAGER, whole.
func (m tuiModel) pageEntry() tea.Cmd {
	t, ok := m.selectedTask()
	if !ok {
		return nil
	}
	feed := m.taskFeed(t)
	i := m.selectedEntry(len(feed))
	if i < 0 {
		return nil
	}
	e := feed[i]
	text := e.text
	if e.detail != "" {
		sep := "\n\n--- input ---\n\n"
		if e.kind == kindCheck {
			sep = "\n\n--- result ---\n\n"
		}
		text += sep + e.detail
	}
	f, err := os.CreateTemp("", "narwhal-entry-*.md")
	if err != nil {
		return func() tea.Msg { return noticeMsg{err: err} }
	}
	path := f.Name()
	_, werr := f.WriteString(text + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return func() tea.Msg { return noticeMsg{err: werr} }
	}
	return pagerCmd(path, true)
}

// pagerCmd shows a file in $PAGER (less -R if unset), removing it after
// when temp is set.
func pagerCmd(path string, temp bool) tea.Cmd {
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	c := exec.Command("sh", "-c", pager+` "$1"`, "narwhal-pager", path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if temp {
			os.Remove(path)
		}
		if err != nil {
			return noticeMsg{err: fmt.Errorf("pager: %w", err)}
		}
		return nil
	})
}

// handleTaskKey drives the task view's entry keys; the rest fall through
// to the detail handler.
func (m tuiModel) handleTaskKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "i":
		// An instruction for this task: read on the radio while it runs,
		// or reopening it when it has finished. See steer.go.
		if t, ok := m.selectedTask(); ok {
			m.startSteer(t.ID)
		}
		return m, nil, true
	case "O":
		// This run's outputs, this task's first. See outputs.go.
		if t, ok := m.selectedTask(); ok {
			return m, m.openOutputs(t.ID), true
		}
		return m, nil, true
	case "tab", "l", "right":
		m.switchTaskTab(1)
		return m, nil, true
	case "shift+tab", "h", "left":
		m.switchTaskTab(-1)
		return m, nil, true
	}
	t, ok := m.selectedTask()
	if !ok {
		return m, nil, false
	}
	if m.currentTaskTab(t) != taskTabActivity {
		// Summary and Messages are read, not navigated: lines scroll, and
		// enter on Summary opens or folds the request.
		switch msg.String() {
		case "j", "down":
			m.detailScroll++
		case "k", "up":
			m.detailScroll = max(0, m.detailScroll-1)
		case "ctrl+d", "pgdown", " ":
			m.detailScroll += m.height / 2
		case "ctrl+u", "pgup":
			m.detailScroll = max(0, m.detailScroll-m.height/2)
		case "g", "home":
			m.detailScroll = 0
		case "G", "end":
			m.detailScroll = 1 << 20
		case "o":
			return m, m.openOutputs(t.ID), true
		case "enter":
			m.summaryAskOpen = !m.summaryAskOpen
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			res := broker.Snapshot{Tasks: []broker.TaskSnapshot{t}}.Result()
			return m, openArtifact(res.Artifacts, int(msg.String()[0]-'0'), m.snap.CWD), true
		default:
			return m, nil, false
		}
		return m, nil, true
	}
	switch msg.String() {
	case "j", "down":
		m.moveEntry(1)
	case "k", "up":
		m.moveEntry(-1)
	case "enter", " ":
		m.toggleEntry()
	case "E":
		// Open or fold everything, and start over from what is shown.
		m.taskOpenAll, m.taskToggled = !m.taskOpenAll, nil
		m.moveEntry(0)
	case "o":
		return m, m.pageEntry(), true
	case "ctrl+d", "pgdown":
		m.scrollTask(m.height / 2)
	case "ctrl+u", "pgup":
		m.scrollTask(-m.height / 2)
	case "g", "home":
		m.taskSel, m.detailScroll, m.sessionTail = 0, 0, false
	case "G", "end", "f":
		// To the newest, and stay there as it grows.
		m.taskSel, m.sessionTail = -1, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

// resetTaskView forgets the cursor and what was opened, for another task.
func (m *tuiModel) resetTaskView() {
	m.taskSel, m.taskToggled, m.taskOpenAll = -1, nil, false
	m.taskTab, m.summaryAskOpen = taskTabAuto, false
}

// The task view's tabs.
const (
	taskTabSummary = iota
	taskTabActivity
	taskTabMessages
	taskTabAuto = -1 // by the task's state: Activity while it runs
)

var taskTabNames = []string{"Summary", "Activity", "Messages"}

// currentTaskTab resolves the auto tab.
func (m tuiModel) currentTaskTab(t broker.TaskSnapshot) int {
	if m.taskTab != taskTabAuto {
		return m.taskTab
	}
	if t.State == broker.TaskDispatched {
		return taskTabActivity
	}
	return taskTabSummary
}

// taskTabsLine names the tabs, the open one marked, with counts.
func (m tuiModel) taskTabsLine(t broker.TaskSnapshot, cur int) string {
	counts := []string{"", fmt.Sprintf(" %d", len(m.taskFeed(t))), fmt.Sprintf(" %d", len(m.taskMessages(t)))}
	var parts []string
	for i, n := range taskTabNames {
		label := n + counts[i]
		if i == cur {
			parts = append(parts, styCyanBold.Render("▸ "+label))
		} else {
			parts = append(parts, styDim.Render("  "+label))
		}
	}
	return strings.Join(parts, styDim.Render("   "))
}

// taskSummaryLines is what is read together: the request, where the task
// stands, the check, what reached it from outside, the answer and what it
// produced.
func (m tuiModel) taskSummaryLines(t broker.TaskSnapshot, width int) []string {
	var out []string
	section := func(name string) {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, styTitle.Render(name))
	}
	indent := func(lines []string) {
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	inner := max(10, width-2)

	section("Asked")
	asked := renderMarkdown(t.Assignment, inner, lipgloss.NewStyle())
	if !m.summaryAskOpen && len(asked) > 8 {
		indent(asked[:8])
		out = append(out, styDim.Render(fmt.Sprintf("  … %d more lines — enter to open", len(asked)-8)))
	} else {
		indent(asked)
	}

	if t.State == broker.TaskDispatched || t.State == broker.TaskReady || t.State == broker.TaskPending {
		section("Now")
		a, ok := m.activity[t.ID]
		switch {
		case t.State != broker.TaskDispatched:
			out = append(out, styDim.Render("  "+m.noActivityReason(t)))
		case !ok:
			out = append(out, styDim.Render("  starting…"))
		default:
			if a.said != "" {
				out = append(out, "  "+a.said)
			}
			if a.tool != "" {
				out = append(out, styCyan.Render("  → "+truncate(a.tool, inner-4)))
			}
			if !a.last.IsZero() {
				out = append(out, styDim.Render("  last activity "+ago(a.last)+" ago — tab for the full activity"))
			}
		}
	}

	if c := strings.TrimSpace(t.Check); c != "" {
		section("Check")
		indent(renderMarkdown(c, inner, lipgloss.NewStyle()))
		if r := strings.TrimSpace(t.CheckResult); r != "" {
			out = append(out, styDim.Render("  ↳ result"))
			indent(renderMarkdown(r, inner, lipgloss.NewStyle()))
		} else {
			out = append(out, styDim.Render("  ↳ not answered yet"))
		}
	}

	if c := strings.TrimSpace(t.Criterion); c != "" {
		section("Criterion")
		indent(renderMarkdown(c, inner, lipgloss.NewStyle()))
		var last *broker.Verdict
		for i := range m.snap.Verdicts {
			if v := m.snap.Verdicts[i]; v.TaskID == t.ID && v.Question == broker.QuestionCriterion && v.Decided {
				last = &m.snap.Verdicts[i]
			}
		}
		switch {
		case last == nil:
			out = append(out, styDim.Render("  ↳ not judged yet — narwhal reads it against the outcome at task-done"))
		case last.Action == broker.ActionReturned:
			out = append(out, styYellow.Render(fmt.Sprintf("  ↳ not met (p=%.2f) — handed back once", last.P)))
		default:
			out = append(out, styGreen.Render(fmt.Sprintf("  ↳ met (p=%.2f)", last.P)))
		}
	}

	if inputs := m.taskInputs(t.ID); len(inputs) > 0 {
		section("Received")
		var st feedState
		st.sawPrompt = true
		for _, e := range inputs {
			indent(renderEntry(e, inner, &st))
		}
	}

	section("Result")
	final := strings.Join(m.workerOutputLines(t.ID), "\n")
	if strings.TrimSpace(final) == "" {
		final = t.Outcome
	}
	if strings.TrimSpace(final) == "" {
		out = append(out, styDim.Render("  not finished"))
	} else {
		if t.State == broker.TaskFailed {
			out = append(out, styRedBold.Render("  failed"))
		}
		indent(renderMarkdown(final, inner, lipgloss.NewStyle()))
	}

	res := broker.Snapshot{Tasks: []broker.TaskSnapshot{t}}.Result()
	if !res.Artifacts.Empty() {
		section("Artifacts")
		out = append(out, m.artifactLines(res, width, true)...)
	}
	return out
}

// taskMessages is the radio traffic a task sent or was addressed in.
func (m tuiModel) taskMessages(t broker.TaskSnapshot) []*broker.Message {
	tasks := make(map[string]bool, len(m.snap.Tasks))
	for _, x := range m.snap.Tasks {
		tasks[x.ID] = true
	}
	var out []*broker.Message
	for _, msg := range m.snap.Messages {
		if msg == nil {
			continue
		}
		if msg.Sender == "worker-"+t.ID {
			out = append(out, msg)
			continue
		}
		for _, id := range recipients(msg, "", tasks) {
			if id == t.ID {
				out = append(out, msg)
				break
			}
		}
	}
	return out
}

// taskMessageLines draws a task's messages in full, newest last: from it,
// or to it from whom.
func (m tuiModel) taskMessageLines(t broker.TaskSnapshot, width int) []string {
	msgs := m.taskMessages(t)
	if len(msgs) == 0 {
		return []string{styDim.Render("  no radio traffic from or to this task")}
	}
	var out []string
	for i, msg := range msgs {
		if i > 0 {
			out = append(out, "")
		}
		dir := styBlue.Render("sent")
		if msg.Sender != "worker-"+t.ID {
			dir = styYellow.Render("from " + shortName(msg.Sender))
		}
		head := styDim.Render(clock(msg.CreatedAt)+"  ") + dir
		if msg.Priority == broker.PriorityUrgent {
			head += "  " + styRedBold.Render("urgent")
		}
		out = append(out, head)
		text, proto := radioBody(msg)
		if proto {
			out = append(out, styDim.Render("  "+text))
			continue
		}
		for _, l := range renderMarkdown(msg.Content, max(10, width-2), lipgloss.NewStyle()) {
			out = append(out, "  "+l)
		}
	}
	return out
}

// noActivityReason says why a task has no feed rather than a blank pane.
func (m tuiModel) noActivityReason(t broker.TaskSnapshot) string {
	switch {
	case t.Dispatches == 0:
		return "not dispatched yet — nothing to show"
	case t.State == broker.TaskDispatched:
		return "the worker has started but has not written its first event yet"
	}
	return "no transcript found. This run predates session pinning, so the worker's session " +
		"cannot be located; only its final output would be available, at " + m.sessionLogPath(t.ID)
}

// switchTaskTab moves between tabs, each starting at its top.
func (m *tuiModel) switchTaskTab(delta int) {
	t, ok := m.selectedTask()
	if !ok {
		return
	}
	n := len(taskTabNames)
	m.taskTab = (m.currentTaskTab(t) + delta + n) % n
	m.detailScroll = 0
	m.sessionTail = m.taskTab == taskTabActivity && t.State == broker.TaskDispatched
}
