// toasts.go is where an action's result is said: the kit's toast queue,
// one line per result above the footer.
//
// There were two status strings — notice for actions, steerMsg for sent
// instructions — each with its own timestamp, and expiry computed in View:
// an idle monitor kept showing "sent" until some other message happened to
// redraw it, and an error vanished after five seconds whether or not anyone
// had read it. A toast expires on its own tick, an error stays until a key
// after it was on screen, and a newer result from the same source replaces
// the older one instead of stacking.
package main

import (
	"strings"

	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/toast"
)

// Toast sources. A newer toast from a source replaces its older one.
const (
	srcNotice = "notice"
	srcSteer  = "steer"
	srcRemove = "remove"
	srcKeys   = "keys"
	srcPrefs  = "prefs"
)

var toastStyles = toast.Styles{
	Info:    styDim,
	Success: styGreen,
	Warn:    styYellow,
	Error:   styRed,
}

// pushToast queues t. Update arms its expiry tick (toast.Ticks), so call
// sites that only mutate the model need not thread a command back.
func (m *tuiModel) pushToast(t toast.Toast) {
	m.toasts = toast.Add(m.toasts, t)
}

// setNotice says how an action went: text when it worked, err when not.
func (m *tuiModel) setNotice(text string, err error) {
	m.notifyFrom(srcNotice, text, err)
}

// steerNote says what became of an instruction.
func (m *tuiModel) steerNote(text string, failed bool) {
	lvl := toast.Success
	if failed {
		lvl = toast.Error
	}
	m.pushToast(toast.Toast{Source: srcSteer, Text: text, Level: lvl})
}

// askRemove is the question a first d asks; any other key dismisses it.
func (m *tuiModel) askRemove(text string) {
	m.pushToast(toast.Toast{Source: srcRemove, Text: text, Level: toast.Warn})
}

// setRemoveResult replaces the question with what the second d did.
func (m *tuiModel) setRemoveResult(text string, err error) {
	m.notifyFrom(srcRemove, text, err)
}

func (m *tuiModel) notifyFrom(src, text string, err error) {
	if err != nil {
		m.pushToast(toast.Toast{Source: src, Text: err.Error(), Level: toast.Error})
		return
	}
	if text != "" {
		m.pushToast(toast.Toast{Source: src, Text: text, Level: toast.Success})
	}
}

// noticeLine is the toasts, one line each at most w cells, or "".
func (m tuiModel) noticeLine(w int) string {
	return toast.View(m.toasts, max(1, w), toastStyles)
}

// footerBlock is the bottom of a screen: the toasts, the line being typed
// (with its own keys) or the open steering line, then keysLine.
func (m tuiModel) footerBlock(w int, keysLine string) string {
	if m.composing {
		return m.composeLine(w) + "\n" + m.keyFooter([]keys.Ctx{ctxCompose}, w)
	}
	var parts []string
	if n := m.noticeLine(w); n != "" {
		parts = append(parts, n)
	}
	if m.steering {
		parts = append(parts, m.steerBar())
	}
	return strings.Join(append(parts, keysLine), "\n")
}
