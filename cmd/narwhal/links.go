// links.go makes the URLs the monitor shows reachable (E11): a PR's short
// label is an OSC 8 hyperlink to the PR, and o on an open message opens the
// URL in its text. Opening goes through tuikit/link, which launches only
// http(s) URLs: the URLs here come from workers' output, which a prompt
// can steer, so a file: or custom-scheme URL must never reach the opener.
package main

import (
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/link"
)

// linked makes an output's label a hyperlink to what it names, when that is
// a URL: a PR's "owner/repo#12" hides the URL it stands for.
func (o output) linked(label string) string {
	if o.kind == outPR {
		return link.Wrap(o.ref, label)
	}
	return label
}

// messageLinkBinding is o on an open message. Only there: in the run view
// o is run.outputs (ctxRun), and the radio pane binding o to links would
// shadow it; the task view's o is task.outputs and act.pager.
func messageLinkBinding() keys.Binding {
	bd := link.Binding(ctxMessage)
	bd.Group = grpRead
	bd.Help = "open the link in the message (the first, when there are several)"
	return bd
}

// openMessageLink opens the first URL in the open message, and says so
// when it has none or more than one.
func (m *tuiModel) openMessageLink() tea.Cmd {
	if m.detail != detailMessage || m.radioCur >= len(m.snap.Messages) {
		return nil
	}
	urls := link.Find(m.snap.Messages[m.radioCur].Content)
	switch len(urls) {
	case 0:
		m.notifyFrom(srcNotice, "no link in this message", nil)
		return nil
	case 1:
		return link.Open(urls[0])
	}
	// Several: the first opens, and the toast that reports it says how
	// many there were, so the rest are not mistaken for gone.
	open, n := link.Open(urls[0]), len(urls)
	return func() tea.Msg {
		msg, _ := open().(link.OpenedMsg)
		return linkOfMsg{OpenedMsg: msg, of: n}
	}
}

// linkOfMsg is an OpenedMsg for the first of several links.
type linkOfMsg struct {
	link.OpenedMsg
	of int
}

// noteOpened says where o went, or why it did not.
func (m *tuiModel) noteOpened(msg link.OpenedMsg, of int) {
	if msg.Err != nil {
		m.notifyFrom(srcNotice, "", errors.New(msg.Note()))
		return
	}
	note := msg.Note()
	if of > 1 {
		note += fmt.Sprintf(" (the first of %d links)", of)
	}
	m.notifyFrom(srcNotice, note, nil)
}
