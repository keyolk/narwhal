// editor.go writes a request in the person's own editor.
//
// A request is often a paragraph — the problem, what has been tried, which
// files matter — and the one-line field on the home screen is the wrong
// place to write that: no newlines, no cursor movement past backspace, and
// nowhere to paste a log and trim it. ctrl-e (or e on home) hands the
// request to $VISUAL or $EDITOR, the way git hands a commit message, and
// what is saved comes back as the request.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/store"
)

// editorDoneMsg carries what the editor saved, or why it did not.
type editorDoneMsg struct {
	text string
	err  error
}

// editorTemplate goes below what has been typed so far. Lines starting with
// # are dropped, as in a commit message, so it can explain itself without
// becoming part of the request.
const editorTemplate = `

# Write the request above. Lines starting with # are ignored.
# It runs in %s.
# Save and quit to submit it; leave it empty to cancel.
`

// editorCommand is the editor to run: $VISUAL, then $EDITOR, then vi,
// which every system this runs on has. Either variable may carry
// arguments ("code --wait"), so it goes through the shell.
func editorCommand(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	return exec.Command("sh", "-c", ed+` "$1"`, "narwhal-editor", path)
}

// openEditor suspends the TUI and edits the request in a temporary file.
func (m tuiModel) openEditor() tea.Cmd {
	path, err := prepareEditorFile(string(m.composeBuf), m.homeCWD)
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}
	c := editorCommand(path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg { return finishEditor(path, err) })
}

// prepareEditorFile writes what has been typed so far, and the template
// under it, to a temporary file for the editor.
func prepareEditorFile(typed, cwd string) (string, error) {
	f, err := os.CreateTemp("", "narwhal-request-*.md")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_, werr := fmt.Fprintf(f, "%s"+editorTemplate, typed, abbreviatePath(cwd))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return "", fmt.Errorf("write %s: %w", path, werr)
	}
	return path, nil
}

// finishEditor reads back what the editor saved and removes the file.
func finishEditor(path string, runErr error) editorDoneMsg {
	defer os.Remove(path)
	if runErr != nil {
		return editorDoneMsg{err: fmt.Errorf("editor: %w", runErr)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return editorDoneMsg{err: err}
	}
	return editorDoneMsg{text: stripEditorComments(string(data))}
}

// notesTemplate follows a flow's notes in the editor.
const notesTemplate = `

# Notes for this piece of work. Lines starting with # are ignored.
# Every later step is planned with them: write what was decided, what is
# out of scope, what to be careful of. Save to keep them; an empty file
# clears them.
`

// openNotesEditor edits a flow's notes in $EDITOR. A run from before flows
// is adopted into one first, so it has notes to keep.
func (m *tuiModel) openNotesEditor(flowID string) tea.Cmd {
	if err := m.adoptFlow(flowID); err != nil {
		return func() tea.Msg { return noticeMsg{err: err} }
	}
	f, err := store.LoadFlow(flowID)
	if err != nil {
		return func() tea.Msg { return noticeMsg{err: err} }
	}
	tmp, err := os.CreateTemp("", "narwhal-notes-*.md")
	if err != nil {
		return func() tea.Msg { return noticeMsg{err: err} }
	}
	path := tmp.Name()
	_, werr := fmt.Fprintf(tmp, "%s"+notesTemplate, f.Notes)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return func() tea.Msg { return noticeMsg{err: werr} }
	}
	m.editorNotes = flowID
	c := editorCommand(path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg { return finishEditor(path, err) })
}

// stripEditorComments drops # lines and the blank lines around the text.
func stripEditorComments(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		keep = append(keep, strings.TrimRight(l, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// editorDone takes the edited request. Saved with text, it is submitted as
// is — the editor was where it was finished. Saved empty, nothing happens
// and the line keeps what it had.
func (m tuiModel) editorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if flow := m.editorNotes; flow != "" {
		m.editorNotes = ""
		if msg.err != nil {
			m.setNotice("", msg.err)
			return m, nil
		}
		f, err := store.SetFlowNotes(flow, msg.text)
		if err != nil {
			m.setNotice("", err)
			return m, nil
		}
		m.flows[f.ID] = f
		m.setNotice("notes saved — every later step is planned with them", nil)
		return m, nil
	}
	if msg.err != nil {
		m.homeErr, m.composing = msg.err, true
		return m, nil
	}
	if msg.text == "" {
		return m, nil
	}
	m.composeBuf = []rune(msg.text)
	m.composing, m.starting, m.homeErr = false, msg.text, nil
	return m, m.submitRequest(msg.text)
}
