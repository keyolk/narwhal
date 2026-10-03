// live.go makes the graph show what the workers are doing, not only where
// they stand.
//
// The graph used to be a state diagram: a box per task, coloured by its
// state, redrawn once a second. Every interesting thing about a running
// swarm — which worker is busy, on what, and who just told whom something —
// lived in other panes, so watching a run meant reading the radio list and
// mapping senders back onto boxes by eye.
//
// Three things move now:
//
//   - A running box shows its worker's current tool on a second line and a
//     spinner in place of the state icon, so "busy" and "stuck" stop looking
//     the same.
//   - A radio message is drawn travelling from its sender's box to each box
//     it mentions. The channel is the thing a swarm is made of, and seeing
//     it move is the point of a graph view.
//   - A box that did something in the last few seconds lights its frame.
//
// None of this reads files on the animation tick. Activity is gathered on
// the poll (once a second, through the incremental transcript cache) and
// the tick only advances time; a tick that re-read every transcript ten
// times a second is the shape of the lag ccx's session list had.
package main

import (
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
)

// animInterval is the animation frame rate. Fast enough for a spinner and
// a pulse to read as motion, slow enough to cost nothing measurable.
const animInterval = 100 * time.Millisecond

// pulseDuration is how long a message takes to cross from sender to
// recipient. Long enough to follow with the eye; short enough that a busy
// channel does not queue pulses behind each other.
const pulseDuration = 1200 * time.Millisecond

// hotWindow is how recently a worker must have acted for its box to light.
const hotWindow = 4 * time.Second

// sparkBuckets is how many one-second buckets the activity sparkline holds.
const sparkBuckets = 12

// animMsg drives the animation clock.
type animMsg time.Time

func animTick() tea.Cmd {
	return tea.Tick(animInterval, func(t time.Time) tea.Msg { return animMsg(t) })
}

// agentLive is what one worker is doing, as of the last poll.
type agentLive struct {
	// tool is the worker's latest tool call, summarized to one line.
	tool string
	// said is the first sentence of the latest thing the worker wrote —
	// text or thinking, never the assignment or a steer sent to it.
	said string
	// last is when the worker last wrote anything to its transcript.
	last time.Time
	// spark counts transcript entries per second over the last
	// sparkBuckets seconds, oldest first.
	spark []int
}

// pulse is one radio message in flight between two boxes.
type pulse struct {
	from, to string // task ids
	born     time.Time
	urgent   bool
}

// progress is how far along its path the pulse is, in [0, 1], or false
// once it has arrived.
func (p pulse) progress(now time.Time) (float64, bool) {
	f := float64(now.Sub(p.born)) / float64(pulseDuration)
	if f < 0 {
		f = 0
	}
	return f, f < 1
}

// gatherLive reads each worker's transcript and summarizes what it is
// doing. It runs inside the poll command, off the UI goroutine, and never
// on the animation tick — the transcript cache makes an unchanged file a
// stat, but a stat per worker ten times a second is still work for nothing.
//
// It takes the snapshot rather than reading m.snap because the poll hands
// it the snapshot it just fetched; m.snap is the previous one.
func (m tuiModel) gatherLive(snap broker.Snapshot, now time.Time) map[string]agentLive {
	out := map[string]agentLive{}
	for _, t := range snap.Tasks {
		if t.Dispatches == 0 {
			continue
		}
		sid := m.workerSessionID(t.ID)
		if sid == "" {
			continue
		}
		entries := globalTranscripts.read(transcriptPath(m.live.CWD, sid))
		if len(entries) == 0 {
			continue
		}
		out[t.ID] = summarizeLive(entries, now)
	}
	return out
}

// summarizeLive reduces a transcript to the latest tool, the latest write
// and a per-second activity histogram.
func summarizeLive(entries []transcriptEntry, now time.Time) agentLive {
	var a agentLive
	a.spark = make([]int, sparkBuckets)
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.at.After(a.last) {
			a.last = e.at
		}
		if a.tool == "" && e.kind == "tool" {
			a.tool = e.text
		}
		if a.said == "" && !e.user && (e.kind == "text" || e.kind == "thinking") {
			a.said = firstSentence(e.text)
		}
		age := now.Sub(e.at)
		if age < 0 {
			age = 0
		}
		if b := int(age / time.Second); b < sparkBuckets {
			a.spark[sparkBuckets-1-b]++
		} else if a.tool != "" && a.said != "" {
			// Entries are in time order, so once one falls outside the
			// window and the tool and prose are known, nothing older
			// matters.
			break
		}
	}
	return a
}

// mentionRe finds an @-mention of a task in a message body. Workers write
// "@worker-split-config" or "@split-config" in prose and rarely fill the
// structured Mentions field: across the last 15 runs on disk, 9 of 167
// messages had Mentions set and 24 named a peer in the body.
var mentionRe = regexp.MustCompile(`@(?:worker-)?([A-Za-z0-9_-]+)`)

// recipients is every task a message is addressed to, from its structured
// mentions and its body, without the sender itself.
func recipients(m *broker.Message, from string, tasks map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		id = strings.TrimPrefix(strings.TrimPrefix(id, "@"), "worker-")
		if id == from || !tasks[id] || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range m.Mentions {
		add(id)
	}
	for _, g := range mentionRe.FindAllStringSubmatch(m.Content, -1) {
		add(g[1])
	}
	return out
}

// newPulses turns radio messages that arrived since the last poll into
// pulses, and reports which tasks should flash. A message travels to every
// task it names. One that names nobody — most of them — flashes its
// sender. One from outside the graph (the operator, the coordinator)
// flashes whoever it names, since there is no box for it to leave from.
func newPulses(msgs []*broker.Message, after int64, tasks map[string]bool, now time.Time) ([]pulse, map[string]time.Time) {
	var out []pulse
	flash := map[string]time.Time{}
	for _, m := range msgs {
		if m == nil || m.Seq <= after {
			continue
		}
		from := strings.TrimPrefix(m.Sender, "worker-")
		to := recipients(m, from, tasks)
		if !tasks[from] {
			for _, id := range to {
				flash[id] = now
			}
			continue
		}
		flash[from] = now
		for _, id := range to {
			out = append(out, pulse{from: from, to: id, born: now,
				urgent: m.Priority == broker.PriorityUrgent})
		}
	}
	return out, flash
}

// spinnerFrames is the braille spinner a running box uses for its icon.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerAt(frame int) string {
	return spinnerFrames[frame%len(spinnerFrames)]
}

// sparkline renders counts as block characters, scaled to their own max so
// a quiet worker's activity is still visible.
func sparkline(counts []int) string {
	const ramp = "▁▂▃▄▅▆▇█"
	steps := []rune(ramp)
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		if max == 0 || c == 0 {
			b.WriteRune(' ')
			continue
		}
		i := c * (len(steps) - 1) / max
		b.WriteRune(steps[i])
	}
	return b.String()
}

// animating reports whether anything on screen moves, so the animation
// tick can stop when a run is idle rather than waking ten times a second
// to redraw the same frame.
func (m tuiModel) animating(now time.Time) bool {
	for _, p := range m.pulses {
		if _, live := p.progress(now); live {
			return true
		}
	}
	for _, t := range m.snap.Tasks {
		if t.State == broker.TaskDispatched {
			return true
		}
	}
	return false
}

// prunePulses drops pulses that have arrived.
func prunePulses(ps []pulse, now time.Time) []pulse {
	out := ps[:0]
	for _, p := range ps {
		if _, live := p.progress(now); live {
			out = append(out, p)
		}
	}
	return out
}

// absorbLive applies a poll's activity and turns new radio traffic into
// pulses, then starts the animation clock if anything is moving.
//
// The first snapshot of a run only records where the channel is. Opening
// the monitor on a run with forty messages would otherwise fire forty
// pulses at once — history drawn as if it were happening now.
func (m *tuiModel) absorbLive(activity map[string]agentLive, now time.Time) tea.Cmd {
	m.activity = activity
	m.now = now
	var newest int64
	for _, msg := range m.snap.Messages {
		if msg != nil && msg.Seq > newest {
			newest = msg.Seq
		}
	}
	if m.seenChannel {
		tasks := make(map[string]bool, len(m.snap.Tasks))
		for _, t := range m.snap.Tasks {
			tasks[t.ID] = true
		}
		ps, flash := newPulses(m.snap.Messages, m.lastSeq, tasks, now)
		m.pulses = append(m.pulses, ps...)
		if m.flash == nil {
			m.flash = map[string]time.Time{}
		}
		for id, at := range flash {
			m.flash[id] = at
		}
	}
	// A worker asking the judge is activity too: light its box when a new
	// question lands. Counted by position, since verdicts only append.
	if m.seenChannel && len(m.snap.Verdicts) > m.seenVerdicts {
		if m.flash == nil {
			m.flash = map[string]time.Time{}
		}
		for _, v := range m.snap.Verdicts[m.seenVerdicts:] {
			if v.Asker != "" {
				m.flash[v.TaskID] = now
			}
		}
	}
	m.seenVerdicts = len(m.snap.Verdicts)
	m.seenChannel = true
	if newest > m.lastSeq {
		m.lastSeq = newest
	}
	if m.animated || !m.animating(now) {
		return nil
	}
	m.animated = true
	return animTick()
}

// resetLive forgets everything about the previous run's motion.
func (m *tuiModel) resetLive() {
	m.activity = nil
	m.pulses = nil
	m.flash = nil
	m.lastSeq = 0
	m.seenVerdicts = 0
	m.seenChannel = false
}

// liveDetail is the second line of a box: what its worker is doing now.
// Only a running task gets one. A finished task's last line is history,
// and spending a row on it for every box would halve what fits.
//
// It leads with what the worker last said rather than its last tool. The
// tool flickers between Read and Grep every second and says little on its
// own; "Splitting the config loader before touching the API" says what the
// worker is about. The tool stands in only until the worker has written
// any prose.
func (m tuiModel) liveDetail(id string) string {
	t := m.taskByID(id)
	if t.State != broker.TaskDispatched {
		return ""
	}
	a, ok := m.activity[id]
	switch {
	case ok && a.said != "":
		return a.said
	case ok && a.tool != "":
		return a.tool
	default:
		return "starting…"
	}
}

// isHot reports whether a task acted, or was spoken to, within hotWindow.
func (m tuiModel) isHot(id string) bool {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	if at, ok := m.flash[id]; ok && now.Sub(at) < hotWindow {
		return true
	}
	if a, ok := m.activity[id]; ok && !a.last.IsZero() && now.Sub(a.last) < hotWindow {
		return true
	}
	return false
}

// boxCenters maps each task to the cell at the middle of its box, in the
// coordinates of rows. Pulses travel between these.
func boxCenters(rows []boxRow, nodes []graphNode) map[string][2]int {
	type extent struct{ x0, x1, y0, y1 int }
	ext := map[int]*extent{}
	for y, r := range rows {
		for _, s := range r.spans {
			e, ok := ext[s.node]
			if !ok {
				ext[s.node] = &extent{s.x0, s.x1, y, y}
				continue
			}
			e.y1 = y
		}
	}
	out := map[string][2]int{}
	for i, e := range ext {
		if i < 0 || i >= len(nodes) {
			continue
		}
		out[nodes[i].task.ID] = [2]int{(e.x0 + e.x1) / 2, (e.y0 + e.y1) / 2}
	}
	return out
}

// pulsePath is the route a pulse follows from one box to another: along
// the drawn edges wherever they lead there, across the gaps where they
// do not, and never through a box.
//
// The first version walked the gaps only, by shortest path, and a pulse
// between two connected boxes ran beside the edge joining them instead of
// on it — the line was right there and the message ignored it. Costing
// line cells far below blank ones makes the route ride the edges, and
// still lets a mention between unconnected peers cross open space, since
// most messages worth seeing follow no edge at all.
func pulsePath(rows []boxRow, from, to [2]int) [][2]int {
	h := len(rows)
	if h == 0 {
		return nil
	}
	grid := make([][]rune, h)
	w := 0
	for y, r := range rows {
		grid[y] = []rune(r.text)
		if len(grid[y]) > w {
			w = len(grid[y])
		}
	}
	cell := func(x, y int) rune {
		if x < len(grid[y]) {
			return grid[y][x]
		}
		return ' '
	}
	inBox := func(x, y int) bool {
		for _, s := range rows[y].spans {
			if x >= s.x0 && x < s.x1 {
				return true
			}
		}
		return false
	}
	// Leave each box through its border, on the side facing the other box,
	// at the column where an edge already attaches if there is one — the
	// tee in the border — so the route starts on the line.
	exit := func(c [2]int, toward int) [2]int {
		step := 1
		if toward < c[1] {
			step = -1
		}
		y := c[1]
		for y >= 0 && y < h && inBox(c[0], y) {
			y += step
		}
		if y < 0 || y >= h {
			return [2]int{-1, -1}
		}
		border := y - step
		if border < 0 || border >= h || border == c[1] && !inBox(c[0], border) {
			// The centre was not inside a box, so there is no border to
			// leave through; start where we are.
			return [2]int{c[0], y}
		}
		for dx := 0; dx < w; dx++ {
			for _, x := range []int{c[0] - dx, c[0] + dx} {
				if x < 0 || x >= w || !inBox(x, border) {
					continue
				}
				if r := cell(x, border); r == '┬' || r == '┴' || r == '┼' {
					if !inBox(x, y) {
						return [2]int{x, y}
					}
				}
			}
		}
		return [2]int{c[0], y}
	}
	start, goal := exit(from, to[1]), exit(to, from[1])
	if start[0] < 0 || goal[0] < 0 {
		return nil
	}

	const lineCost, gapCost = 1, 8
	cost := func(x, y int) int {
		if strings.ContainsRune("│─┌┐└┘├┤┬┴┼", cell(x, y)) {
			return lineCost
		}
		return gapCost
	}
	type node struct {
		c [2]int
		d int
	}
	dist := map[[2]int]int{start: 0}
	prev := map[[2]int][2]int{}
	// The grid is a few thousand cells; a linear scan for the minimum is
	// simpler than a heap and well inside a frame's budget.
	open := []node{{start, 0}}
	done := map[[2]int]bool{}
	for len(open) > 0 {
		bi := 0
		for i := range open {
			if open[i].d < open[bi].d {
				bi = i
			}
		}
		cur := open[bi]
		open = append(open[:bi], open[bi+1:]...)
		if done[cur.c] {
			continue
		}
		done[cur.c] = true
		if cur.c == goal {
			break
		}
		for _, d := range [][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
			n := [2]int{cur.c[0] + d[0], cur.c[1] + d[1]}
			if n[0] < 0 || n[0] >= w || n[1] < 0 || n[1] >= h || inBox(n[0], n[1]) {
				continue
			}
			nd := cur.d + cost(n[0], n[1])
			if old, ok := dist[n]; ok && old <= nd {
				continue
			}
			dist[n] = nd
			prev[n] = cur.c
			open = append(open, node{n, nd})
		}
	}
	if !done[goal] {
		return nil
	}
	var path [][2]int
	for c := goal; c != start; c = prev[c] {
		path = append(path, c)
	}
	path = append(path, start)
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// pulseGlyph is drawn at a pulse's position. A trail of two dimmer dots
// behind the head makes direction readable in a single frame.
const (
	pulseHead  = '●'
	pulseTrail = '·'
	// pulseTrailLen is how many cells behind the head stay lit. On an edge
	// those cells keep their line glyph, so the trail is a stretch of
	// brightened line rather than dots.
	pulseTrailLen = 3
)

// overlayPulses draws in-flight pulses onto the rendered rows, returning
// for each row the columns that carry one, so the styler can colour them.
// A pulse never overwrites a box: it would read as a glyph in the label.
func overlayPulses(rows []boxRow, centers map[string][2]int, pulses []pulse, now time.Time) map[int]map[int]pulseMark {
	marks := map[int]map[int]pulseMark{}
	put := func(x, y int, r rune, urgent, head bool) {
		if y < 0 || y >= len(rows) || x < 0 {
			return
		}
		for _, s := range rows[y].spans {
			if x >= s.x0 && x < s.x1 {
				return
			}
		}
		// On a line the trail keeps the line's own glyph and only takes the
		// pulse's colour, so the edge reads as lit rather than broken up;
		// the head replaces whatever is there.
		runes := []rune(rows[y].text)
		for len(runes) <= x {
			runes = append(runes, ' ')
		}
		if head || runes[x] == ' ' {
			runes[x] = r
		}
		rows[y].text = string(runes)
		if marks[y] == nil {
			marks[y] = map[int]pulseMark{}
		}
		if prev, ok := marks[y][x]; !ok || head || !prev.head {
			marks[y][x] = pulseMark{urgent: urgent, head: head}
		}
	}
	paths := map[[2]string][][2]int{}
	for _, p := range pulses {
		f, live := p.progress(now)
		if !live {
			continue
		}
		from, ok1 := centers[p.from]
		to, ok2 := centers[p.to]
		if !ok1 || !ok2 {
			continue
		}
		key := [2]string{p.from, p.to}
		path, ok := paths[key]
		if !ok {
			path = pulsePath(rows, from, to)
			paths[key] = path
		}
		if len(path) == 0 {
			continue
		}
		head := int(f * float64(len(path)-1))
		for back := pulseTrailLen; back >= 1; back-- {
			if i := head - back; i >= 0 {
				put(path[i][0], path[i][1], pulseTrail, p.urgent, false)
			}
		}
		put(path[head][0], path[head][1], pulseHead, p.urgent, true)
	}
	return marks
}

// pulseMark is one pulse cell on a row, for colouring.
type pulseMark struct {
	urgent bool
	head   bool
}

// headlineRows says, for each rendered row, which boxes have their first
// body line on it — the row right after their top border.
func headlineRows(rows []boxRow) map[int]map[int]bool {
	out := map[int]map[int]bool{}
	for y := 1; y < len(rows); y++ {
		for _, s := range rows[y].spans {
			if s.part != partBody {
				continue
			}
			above := rows[y-1]
			if above.owns(s.node) && above.spanOf(s.node).part == partTop {
				if out[y] == nil {
					out[y] = map[int]bool{}
				}
				out[y][s.node] = true
			}
		}
	}
	return out
}
