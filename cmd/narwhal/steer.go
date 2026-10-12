// steer.go is the monitor's command line: type an instruction while the
// run is going and it lands on the radio as the operator.
//
// Steering a run meant leaving the monitor for another terminal and an MCP
// call, which is exactly the moment the thought that prompted it is lost.
// `i` opens a line at the foot of the screen; Enter posts it through the
// daemon's control route, the same one narwhal_send uses, as "main".
//
// "@worker-x" or "@x" in the text addresses that task, and a leading "!"
// marks it urgent — the two things a worker's watcher acts on.
//
// Addressing a task that has finished reopens it: the daemon resumes the
// worker's Claude session with the instruction (see broker/followup.go).
// That is the one thing a finished run can still be told, so its line
// takes keystrokes too, and an instruction to no one in particular goes
// to the task whose outcome is the run's answer — resuming a finished run
// means continuing its answer. From a task's view, i starts the line
// addressed to it; from a flow's, it goes to the selected step's run.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/store"
)

// steerSentMsg reports how a posted instruction went, and which finished
// tasks it reopened.
type steerSentMsg struct {
	text     string
	reopened []string
	err      error
}

// steerNoticeFor is how long the footer shows a send result.
const steerNoticeFor = 4 * time.Second

// steerRequest is one instruction ready to post.
type steerRequest struct {
	RunID    string   `json:"run_id"`
	Content  string   `json:"content"`
	Mentions []string `json:"mentions,omitempty"`
	Priority string   `json:"priority"`
}

// parseSteer turns a typed line into a request: urgent when it starts with
// "!", addressed to every task it @-mentions.
func parseSteer(line, runID string, tasks map[string]bool) (steerRequest, bool) {
	text := strings.TrimSpace(line)
	prio := broker.PriorityNormal
	if strings.HasPrefix(text, "!") {
		prio = broker.PriorityUrgent
		text = strings.TrimSpace(strings.TrimPrefix(text, "!"))
	}
	if text == "" {
		return steerRequest{}, false
	}
	req := steerRequest{RunID: runID, Content: text, Priority: string(prio)}
	msg := &broker.Message{Content: text}
	for _, id := range recipients(msg, "", tasks) {
		req.Mentions = append(req.Mentions, "worker-"+id)
	}
	return req, true
}

// daemonURL finds the running daemon, for a run read from disk: the
// daemon that ran it may be gone, but the one running now can reopen it.
// A variable so tests can stand in for it.
var daemonURL = func() (string, error) { return nd.URL() }

// steerTarget is the run an instruction goes to: the watched one, or a
// flow step's, chosen in the flow view.
type steerTarget struct {
	brokerURL string
	snap      broker.Snapshot
	// step is the flow step's 1-based number, for the line's label; 0 for
	// the watched run.
	step int
}

// currentSteerTarget is where the open line sends.
func (m tuiModel) currentSteerTarget() steerTarget {
	if m.steerStep != nil {
		return *m.steerStep
	}
	snap := m.snap
	if snap.RunID == "" {
		snap.RunID = m.runID()
	}
	return steerTarget{brokerURL: m.live.BrokerURL, snap: snap}
}

// live reports whether the target has work going that an instruction to
// no one in particular could reach.
func (t steerTarget) live() bool {
	if t.brokerURL == "" {
		return false
	}
	switch t.snap.State {
	case broker.RunDone, broker.RunFailed, broker.RunCanceled:
		return false
	}
	return true
}

// resumes is the task an unaddressed instruction to a finished run goes
// to: the one whose outcome is the run's answer — usually the synthesis —
// or the only task there is. "" when the answer was composed from several,
// and the instruction must say which.
func (t steerTarget) resumes() string {
	if r := t.snap.Result(); r.AnswerFrom != "" {
		return r.AnswerFrom
	}
	if len(t.snap.Tasks) == 1 {
		return t.snap.Tasks[0].ID
	}
	return ""
}

// sendSteer posts an instruction to the target's daemon in the background.
func (m tuiModel) sendSteer(req steerRequest) tea.Cmd {
	base := m.currentSteerTarget().brokerURL
	client := m.client
	return func() tea.Msg {
		if base == "" {
			u, err := daemonURL()
			if err != nil {
				return steerSentMsg{text: req.Content, err: fmt.Errorf("no daemon is running to reopen this run (narwhal daemon start): %w", err)}
			}
			base = u
		}
		body, err := json.Marshal(req)
		if err != nil {
			return steerSentMsg{text: req.Content, err: err}
		}
		resp, err := client.Post(base+"/api/v1/control/send", "application/json", bytes.NewReader(body))
		if err != nil {
			return steerSentMsg{text: req.Content, err: err}
		}
		defer resp.Body.Close()
		var out struct {
			Reopened []string `json:"reopened"`
			Error    string   `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			if out.Error != "" {
				return steerSentMsg{text: req.Content, err: errors.New(out.Error)}
			}
			return steerSentMsg{text: req.Content, err: fmt.Errorf("daemon returned %d", resp.StatusCode)}
		}
		if decodeErr != nil {
			return steerSentMsg{text: req.Content, err: fmt.Errorf("daemon answer: %w", decodeErr)}
		}
		return steerSentMsg{text: req.Content, reopened: out.Reopened}
	}
}

// startSteer opens the command line to the watched run, addressed to task
// when one is given.
func (m *tuiModel) startSteer(task string) {
	m.steering, m.steerBuf, m.steerStep = true, nil, nil
	if task != "" {
		m.steerBuf = []rune("@" + task + " ")
	}
}

// startStepSteer opens the command line to a flow step's run.
func (m *tuiModel) startStepSteer(run store.LiveRun, step int) {
	m.steering, m.steerBuf = true, nil
	m.steerStep = &steerTarget{brokerURL: run.BrokerURL, snap: m.runSnapshot(run.RunID), step: step}
	if m.steerStep.snap.RunID == "" {
		m.steerStep.snap.RunID = run.RunID
	}
}

// handleSteerKey drives the open command line. Every key belongs to it
// while it is open, so typing "q" writes a q rather than quitting.
func (m tuiModel) handleSteerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.steering, m.steerBuf, m.steerStep = false, nil, nil
		return m, nil
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEnter:
		line := string(m.steerBuf)
		target := m.currentSteerTarget()
		m.steering, m.steerBuf = false, nil
		tasks := make(map[string]bool, len(target.snap.Tasks))
		for _, t := range target.snap.Tasks {
			tasks[t.ID] = true
		}
		req, ok := parseSteer(line, target.snap.RunID, tasks)
		if !ok {
			m.steerStep = nil
			return m, nil
		}
		if !target.live() && len(req.Mentions) == 0 {
			// Nothing is running to read it. Resuming a finished run
			// means continuing its answer, so it goes to the task that
			// wrote it.
			id := target.resumes()
			if id == "" {
				m.steerStep = nil
				m.steerNote("this run's answer came from several tasks — say which with @task", true)
				return m, nil
			}
			req.Mentions = []string{"worker-" + id}
		}
		cmd := m.sendSteer(req)
		m.steerStep = nil
		return m, cmd
	case tea.KeyBackspace:
		if n := len(m.steerBuf); n > 0 {
			m.steerBuf = m.steerBuf[:n-1]
		}
		return m, nil
	case tea.KeySpace:
		m.steerBuf = append(m.steerBuf, ' ')
		return m, nil
	case tea.KeyRunes:
		m.steerBuf = append(m.steerBuf, msg.Runes...)
		return m, nil
	}
	return m, nil
}

// steerNote shows a short result line in the footer.
func (m *tuiModel) steerNote(text string, failed bool) {
	m.steerMsg, m.steerFailed, m.steerAt = text, failed, time.Now()
}

// runIsLive reports whether the watched run has work going that an
// instruction to no one in particular could reach.
func (m tuiModel) runIsLive() bool {
	return steerTarget{brokerURL: m.live.BrokerURL, snap: m.snap}.live()
}

// steerPrompt is the open line's lead: where what is typed will go.
func (m tuiModel) steerPrompt() string {
	t := m.currentSteerTarget()
	var to []string
	if t.step > 0 {
		to = append(to, fmt.Sprintf("step %d", t.step))
	}
	if !t.live() && !strings.HasPrefix(strings.TrimSpace(string(m.steerBuf)), "@") {
		if id := t.resumes(); id != "" {
			to = append(to, "→ "+id)
		}
	}
	if len(to) == 0 {
		return styCyanBold.Render("» ")
	}
	return styMagenta.Render(strings.Join(to, " ")) + styCyanBold.Render(" » ")
}

// steerBar is the run view's standing input line.
//
// Typing, it is the line being typed. Otherwise it says how to start, and
// what became of the last instruction — where it was routed and who has
// answered — which the Brief knew and kept one key away. A run that cannot
// be steered says why instead of taking keystrokes into nowhere.
func (m tuiModel) steerBar() string {
	w := max(1, m.width)
	fit := func(s string) string { return ansi.Truncate(s, w, "…") }
	if m.composing {
		return m.composeLine(w)
	}
	if n := m.noticeLine(w); n != "" {
		return n
	}
	if m.steering {
		hint := "   enter send · esc cancel · @task addresses one · ! urgent"
		if !m.currentSteerTarget().live() {
			hint = "   enter resumes its worker with this · @task picks another task · esc cancel"
		}
		return fit(m.steerPrompt() + string(m.steerBuf) + styCyan.Render("▏") + styDim.Render(hint))
	}
	if m.steerMsg != "" && time.Since(m.steerAt) < steerNoticeFor {
		if m.steerFailed {
			return fit(styRedBold.Render("✗ ") + styRed.Render(m.steerMsg))
		}
		return fit(styGreenBold.Render("✓ ") + m.steerMsg)
	}
	if !m.runIsLive() {
		why := "this request has finished"
		if m.live.BrokerURL == "" && m.snap.State == broker.RunActive {
			why = "its daemon is gone"
		}
		return fit(styDim.Render("» "+why+" · ") + styCyanBold.Render("i") +
			styDim.Render(" resumes it with an instruction · r result · n next step"))
	}
	left := styCyanBold.Render("» ") + styDim.Render("press ") + styCyanBold.Render("i") +
		styDim.Render(" to tell the workers something")
	ins := briefInstructions(m.snap)
	if len(ins) == 0 {
		return fit(left + styDim.Render(" — @task addresses one, ! makes it urgent"))
	}
	last := ins[len(ins)-1]
	state := styYellow.Render("… no reply yet")
	if len(last.answeredBy) > 0 {
		state = styGreen.Render("✓ " + strings.Join(last.answeredBy, ","))
	}
	if last.routed {
		state = styMagenta.Render("→"+strings.Join(last.addressed, ",")) + " " + state
	}
	right := styDim.Render("last: ") + truncate(last.text, 40) + "  " + state
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return fit(left + "  " + right)
	}
	return left + strings.Repeat(" ", gap) + right
}
