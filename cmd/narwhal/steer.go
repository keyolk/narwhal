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
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
)

// steerSentMsg reports how a posted instruction went.
type steerSentMsg struct {
	text string
	err  error
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

// sendSteer posts an instruction in the background.
func (m tuiModel) sendSteer(req steerRequest) tea.Cmd {
	url := m.live.BrokerURL + "/api/v1/control/send"
	client := m.client
	return func() tea.Msg {
		body, err := json.Marshal(req)
		if err != nil {
			return steerSentMsg{text: req.Content, err: err}
		}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return steerSentMsg{text: req.Content, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return steerSentMsg{text: req.Content, err: fmt.Errorf("daemon returned %d", resp.StatusCode)}
		}
		return steerSentMsg{text: req.Content}
	}
}

// handleSteerKey drives the open command line. Every key belongs to it
// while it is open, so typing "q" writes a q rather than quitting.
func (m tuiModel) handleSteerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.steering, m.steerBuf = false, nil
		return m, nil
	case tea.KeyCtrlC:
		m.quit = true
		return m, tea.Quit
	case tea.KeyEnter:
		line := string(m.steerBuf)
		m.steering, m.steerBuf = false, nil
		if m.live.BrokerURL == "" {
			m.steerNote("this run has no live daemon; nothing to steer", true)
			return m, nil
		}
		tasks := make(map[string]bool, len(m.snap.Tasks))
		for _, t := range m.snap.Tasks {
			tasks[t.ID] = true
		}
		req, ok := parseSteer(line, m.runID(), tasks)
		if !ok {
			return m, nil
		}
		return m, m.sendSteer(req)
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

// viewSteer is the footer while the command line is open or a result is
// still fresh, and "" otherwise.
func (m tuiModel) viewSteer() string {
	if m.steering {
		return styCyanBold.Render("» ") + string(m.steerBuf) + styCyan.Render("▏") +
			styDim.Render("   enter send · esc cancel · @task to address · ! urgent")
	}
	if m.steerMsg != "" && time.Since(m.steerAt) < steerNoticeFor {
		if m.steerFailed {
			return styRedBold.Render("✗ ") + styRed.Render(m.steerMsg)
		}
		return styGreenBold.Render("✓ ") + styDim.Render("sent: ") + truncate(m.steerMsg, m.width-10)
	}
	return ""
}

// steerStatus is the footer's standing steer control: the key that opens
// the command line, and what became of the last instruction sent.
//
// The command line only appeared once `i` was pressed, and its result for
// a few seconds after; the rest of the time nothing on screen said the run
// could be steered, or whether the last instruction had been picked up.
// The Brief knew both and was one key away from everything else.
func (m tuiModel) steerStatus() string {
	out := "  " + styCyanBold.Render("i") + styDim.Render(" » steer")
	ins := briefInstructions(m.snap)
	if len(ins) == 0 {
		return out
	}
	last := ins[len(ins)-1]
	state := styYellow.Render("… waiting")
	if len(last.answeredBy) > 0 {
		state = styGreen.Render("✓ " + strings.Join(last.answeredBy, ","))
	}
	if last.routed {
		state = styMagenta.Render("→"+strings.Join(last.addressed, ",")) + " " + state
	}
	return out + styDim.Render("  last: ") + truncate(last.text, 40) + "  " + state
}
