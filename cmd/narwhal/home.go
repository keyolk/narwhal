// home.go is what `narwhal` opens on: every request, the ones still being
// worked first, and a line to start a new one.
//
// The monitor began as a viewer for one run, reached with a subcommand and
// a run id, with a run list behind esc for when several were live. That
// put the thing a person comes for — what have I asked for, how is each
// going — two steps from where the program starts, and starting a request
// at all meant leaving it for an MCP call in another session. Home is the
// list, sorted by what needs looking at, with the request line above it:
//
//	╭─ New request ── in ~/src/repo ─────────────────────╮
//	│ » _                                                │
//	╰────────────────────────────────────────────────────╯
//	 In progress · 2
//	 ▸ ⠋ add a replay-safe lock API          ███░░ 3/5  12m
//	     ~/src/repo · running 2 · 6 msg
//	 Finished · 14
//	   ✓ audit the gateway TLS coverage      █████ 5/5  2d
//
// Enter opens a request — its graph, its history, and the steering line.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/store"
)

// requestStartedMsg reports how starting a new request went.
type requestStartedMsg struct {
	run store.LiveRun
	err error
}

// startRequest asks the daemon to plan a new request, starting the daemon
// first if it is not running. A variable so tests can stand in for it: the
// real one launches a planner, which is a Claude session.
var startRequest = func(cwd, prompt string) (store.LiveRun, error) {
	url, err := nd.URL()
	if err != nil {
		if url, err = autoStartDaemon(); err != nil {
			return store.LiveRun{}, err
		}
	}
	body, _ := json.Marshal(map[string]string{"cwd": cwd, "prompt": prompt})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url+"/api/v1/control/plan", "application/json", bytes.NewReader(body))
	if err != nil {
		return store.LiveRun{}, err
	}
	defer resp.Body.Close()
	var out struct {
		RunID     string `json:"run_id"`
		BrokerURL string `json:"broker_url"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return store.LiveRun{}, fmt.Errorf("daemon answered %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusCreated {
		return store.LiveRun{}, fmt.Errorf("daemon refused: %s", out.Error)
	}
	return store.LiveRun{
		RunID: out.RunID, BrokerURL: out.BrokerURL, Prompt: prompt, CWD: cwd,
		StartedAt: time.Now().Unix(), State: "active",
	}, nil
}

// launchDir is where a new request runs: the directory narwhal was started
// in, which is the repository the person is standing in.
func launchDir() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}

// submitRequest starts the typed request in the background.
func (m tuiModel) submitRequest(prompt string) tea.Cmd {
	cwd := m.homeCWD
	return func() tea.Msg {
		run, err := startRequest(cwd, prompt)
		return requestStartedMsg{run: run, err: err}
	}
}

// handleComposeKey drives the new-request line. Every key belongs to it
// while it is open, and arrives as typed: a request written in Korean must
// arrive in Korean.
func (m tuiModel) handleComposeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.composing = false
		return m, nil
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyCtrlE:
		// What has been typed so far goes into the editor with it.
		m.composing = false
		return m, m.openEditor()
	case tea.KeyEnter:
		prompt := strings.TrimSpace(string(m.composeBuf))
		if prompt == "" {
			return m, nil
		}
		m.composing, m.starting = false, prompt
		m.homeErr = nil
		return m, m.submitRequest(prompt)
	case tea.KeyBackspace:
		if n := len(m.composeBuf); n > 0 {
			m.composeBuf = m.composeBuf[:n-1]
		}
	case tea.KeySpace:
		m.composeBuf = append(m.composeBuf, ' ')
	case tea.KeyRunes:
		m.composeBuf = append(m.composeBuf, msg.Runes...)
	}
	return m, nil
}

// requestStarted opens the run a new request created.
func (m tuiModel) requestStarted(msg requestStartedMsg) (tea.Model, tea.Cmd) {
	m.starting = ""
	if msg.err != nil {
		// Back on the line with the request still in it, to fix or retry.
		m.homeErr, m.composing = msg.err, true
		return m, nil
	}
	m.composeBuf = nil
	m.mergeRuns(append(append([]store.LiveRun(nil), m.runs...), msg.run))
	for i, r := range m.runs {
		if r.RunID == msg.run.RunID {
			cmd := m.selectRun(i)
			m.picker = false
			return m, tea.Batch(cmd, m.refreshRuns())
		}
	}
	return m, nil
}

// runGroup orders the home list: what is moving, then what was abandoned
// and still has workers out, then history.
func runGroup(r store.LiveRun) int {
	switch {
	case runIsWorking(r):
		return 0
	case runIsOrphaned(r):
		return 1
	}
	return 2
}

var groupTitles = []string{"In progress", "Abandoned — workers may still be running", "Finished"}

// viewHome draws the home screen.
func (m tuiModel) viewHome() string {
	w := m.width
	where := abbreviatePath(m.homeCWD)
	head := styCyanBold.Render("Narwhal") + styDim.Render("  "+m.runCountLabel())

	// The request line, framed so it reads as the one place to type.
	var line string
	switch {
	case m.starting != "":
		first, _, _ := strings.Cut(m.starting, "\n")
		line = styCyan.Render("» ") + first + styDim.Render("   starting…")
	case m.composing:
		// An edited request can run to paragraphs; the line shows how it
		// starts and how much more there is.
		text := string(m.composeBuf)
		more := ""
		if first, rest, ok := strings.Cut(text, "\n"); ok {
			text, more = first, styDim.Render(fmt.Sprintf("  (+%d lines)", strings.Count(rest, "\n")+1))
		}
		line = styCyanBold.Render("» ") + text + styCyan.Render("▏") + more
	default:
		line = styDim.Render("» press n and describe the work, or e to write it in $EDITOR")
	}
	title := "New request"
	if m.composing {
		title = styCyanBold.Render(title)
	}
	box := framedBox(title+styDim.Render("  in "+where), []string{line}, w-2)
	if m.homeErr != nil {
		box += "\n" + styRedBold.Render(" ✗ ") + styRed.Render(truncate(m.homeErr.Error(), w-4))
	}

	keys := "enter open · n new request · e write in $EDITOR · j/k move · ? help · q quit"
	if m.composing {
		keys = "enter start · ctrl-e open in $EDITOR · esc cancel"
	}
	top := head + "\n" + box + "\n"
	room := m.height - strings.Count(top, "\n") - 2
	return pinFooter(top+m.homeList(w, room), styDim.Render(keys), m.height)
}

// homeList is the requests, grouped, in at most rows lines, scrolled so the
// selection is on screen.
func (m tuiModel) homeList(width, rows int) string {
	if len(m.runs) == 0 {
		return styDim.Render("\n   Nothing yet. Press n to start the first request.")
	}
	var lines []string
	selTop := 0
	group := -1
	counts := map[int]int{}
	for _, r := range m.runs {
		counts[runGroup(r)]++
	}
	for i, r := range m.runs {
		if g := runGroup(r); g != group {
			group = g
			style := styTitle
			if g == 1 {
				style = styYellow
			}
			lines = append(lines, " "+style.Render(groupTitles[g])+styDim.Render(fmt.Sprintf(" · %d", counts[g])))
		}
		if i == m.runCur {
			selTop = len(lines)
		}
		lines = append(lines, m.homeRow(r, i == m.runCur, width)...)
	}
	start := 0
	if rows > 0 && selTop+2 > rows {
		start = selTop + 2 - rows
	}
	end := len(lines)
	if rows > 0 {
		end = min(end, start+rows)
	}
	return strings.Join(lines[start:end], "\n")
}

// homeRow is one request: what was asked and how far it has got, then
// where and the detail.
func (m tuiModel) homeRow(r store.LiveRun, selected bool, width int) []string {
	icon, style := icons.runActive, styCyan
	switch {
	case runIsWorking(r):
		icon = spinnerAt(m.frame)
	case runIsOrphaned(r):
		icon, style = icons.runFailed, styYellow
	case r.Failed > 0:
		icon, style = icons.runFailed, styRed
	case r.State == "canceled":
		icon, style = icons.runCanceled, styDim
	default:
		icon, style = icons.runDone, styGreen
	}

	// Right side: progress and age, fixed width so the column lines up.
	progress := progressBar(r.Done, r.Failed, r.Tasks, 10) + " " + padRight(fmt.Sprintf("%d/%d", r.Done, r.Tasks), 5)
	if r.Tasks == 0 {
		// Padded as plain text, then styled: padding a styled string
		// counts its escapes as cells and the column drifts.
		word := "no tasks"
		if runIsWorking(r) {
			word = "planning…"
		}
		progress = styDim.Render(padRight(word, 16))
	}
	right := progress + " " + styDim.Render(fmt.Sprintf("%4s", ago(runStartTime(r))))
	rightW := ansi.StringWidth(right)

	marker := "   "
	if selected {
		marker = " " + styCyanBold.Render("▸") + " "
	}
	label := truncate(runLabel(r), max(10, width-rightW-8))
	// History recedes: what you can still act on is what you are choosing
	// between, and a list of identical rows hides the one live run.
	styled := label
	switch {
	case selected:
		styled = styTitle.Render(label)
	case runGroup(r) == 2:
		styled = styDim.Render(label)
	}
	first := marker + style.Render(icon) + " " + styled
	first += strings.Repeat(" ", max(1, width-ansi.StringWidth(first)-rightW)) + right

	var detail []string
	detail = append(detail, abbreviatePath(r.CWD))
	if r.Running > 0 {
		detail = append(detail, fmt.Sprintf("%d working", r.Running))
	}
	if r.Failed > 0 {
		detail = append(detail, styRed.Render(fmt.Sprintf("%d failed", r.Failed)))
	}
	if runIsOrphaned(r) {
		detail = append(detail, styYellow.Render("its daemon is gone"))
	}
	if r.Messages > 0 {
		detail = append(detail, fmt.Sprintf("%d msg", r.Messages))
	}
	second := "      " + styDim.Render(strings.Join(detail, styDim.Render(" · ")))
	return []string{first, ansi.Truncate(second, width, "…")}
}

// progressBar draws done and failed out of total in n cells.
func progressBar(done, failed, total, n int) string {
	if total <= 0 {
		return styDim.Render(strings.Repeat("░", n))
	}
	d := done * n / total
	f := min(n-d, (failed*n+total-1)/total)
	return styGreen.Render(strings.Repeat("█", d)) + styRed.Render(strings.Repeat("█", f)) +
		styDim.Render(strings.Repeat("░", n-d-f))
}

// ago is a short age: 40s, 12m, 3h, 2d.
func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// framedBox draws lines inside a rounded box w cells wide, with title on
// the top edge.
func framedBox(title string, lines []string, w int) string {
	// The first frame is drawn before the terminal has said how wide it
	// is, at width 0; a negative repeat there took the program down.
	inner := max(2, w-2)
	t := " " + title + " "
	top := styDim.Render("╭─") + t + styDim.Render(strings.Repeat("─", max(0, inner-1-ansi.StringWidth(t)))+"╮")
	var b strings.Builder
	b.WriteString(top)
	for _, l := range lines {
		b.WriteString("\n" + styDim.Render("│") + " " + fitCells(l, inner-1) + styDim.Render("│"))
	}
	b.WriteString("\n" + styDim.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return b.String()
}
