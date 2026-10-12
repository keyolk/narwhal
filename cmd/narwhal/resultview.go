// resultview.go is a run's result as one screen: its answer, what it
// produced, and each task's conclusion — and the ways to hand it on.
//
// The answer was the synthesis task's outcome, reached by finding that task
// in the graph and opening it; the artifacts were scattered through it as
// prose; and sharing it meant locating the export file by hand. Here the
// answer reads first, the artifacts are a list with each PR's live state,
// and e, y and 1-9 write the report, copy the answer, and open a PR.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// noticeMsg is a one-line result of an action, shown in the footer.
type noticeMsg struct {
	text string
	err  error
}

// prStateMsg carries a PR's state as gh reports it.
type prStateMsg struct{ url, state string }

// prStatusCmd asks gh for the state of each PR the watched run produced.
// A PR gh cannot read — another account, no network — shows "?".
func (m tuiModel) prStatusCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, u := range m.snap.Result().Artifacts.PRs {
		if _, ok := m.prState[u]; ok {
			continue
		}
		u := u
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "gh", "pr", "view", u, "--json", "state", "-q", ".state").Output()
			if err != nil {
				return prStateMsg{u, "?"}
			}
			return prStateMsg{u, strings.ToLower(strings.TrimSpace(string(out)))}
		})
	}
	return tea.Batch(cmds...)
}

// openables are the artifacts a digit opens, in the order they are
// numbered: PRs, then files. Branches and commits have nothing to open.
func openables(a broker.Artifacts) []string {
	return append(append([]string(nil), a.PRs...), a.Files...)
}

// artifactLines lists a result's artifacts. With numbered set, PRs and
// files carry the digit that opens them.
func (m tuiModel) artifactLines(res broker.RunResult, width int, numbered bool) []string {
	var out []string
	label := func(s string) string { return styDim.Render(padRight(s, 8)) }
	n := 0
	key := func() string {
		n++
		if numbered && n <= 9 {
			return styCyanBold.Render(fmt.Sprintf("%d", n)) + " "
		}
		return "  "
	}
	for _, u := range res.Artifacts.PRs {
		state := m.prState[u]
		st := styDim.Render("…")
		switch state {
		case "merged":
			st = styMagenta.Render("merged")
		case "open":
			st = styGreen.Render("open")
		case "closed":
			st = styRed.Render("closed")
		case "?":
			st = styDim.Render("?")
		}
		out = append(out, ansi.Truncate(key()+label("PR")+prShort(u)+"  "+st+styDim.Render("  "+u), width, "…"))
	}
	// Files one to a line: each is something to open.
	for _, f := range res.Artifacts.Files {
		out = append(out, ansi.Truncate(key()+label("file")+abbreviatePath(f)+styDim.Render("  "+f), width, "…"))
	}
	list := func(name string, vals []string) {
		if len(vals) == 0 {
			return
		}
		out = append(out, ansi.Truncate("  "+label(name)+strings.Join(vals, "  ")+
			styDim.Render(fmt.Sprintf("  (%d)", len(vals))), width, "…"))
	}
	list("branch", res.Artifacts.Branches)
	list("commit", res.Artifacts.Commits)
	if res.Extracted > 0 {
		out = append(out, styDim.Render(fmt.Sprintf("  %d read from the outcome text rather than recorded by a worker", res.Extracted)))
	}
	return out
}

// openArtifact opens the n-th openable (1-based): a PR in the browser, a
// file in $PAGER. A relative path is the run's directory's.
func openArtifact(a broker.Artifacts, n int, cwd string) tea.Cmd {
	list := openables(a)
	if n < 1 || n > len(list) {
		return nil
	}
	target := list[n-1]
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return openURLCmd(target)
	}
	path := target
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return func() tea.Msg {
			if err == nil {
				err = fmt.Errorf("%s is a directory", path)
			}
			return noticeMsg{err: fmt.Errorf("cannot open %s: %w", target, err)}
		}
	}
	return pagerCmd(path, false)
}

// prShort is "owner/repo#123" for a PR URL.
func prShort(u string) string {
	s := strings.TrimPrefix(u, "https://github.com/")
	if i := strings.Index(s, "/pull/"); i > 0 {
		return s[:i] + "#" + s[i+len("/pull/"):]
	}
	return s
}

// viewResult draws the watched run's result.
func (m tuiModel) viewResult() string {
	w := max(m.width, 1)
	res := m.snap.Result()
	icon, state := runGlyph(m.snap.State)
	head := styDim.Render("‹ ") + styCyanBold.Render("Result") + "   " + icon + " " + state + "   " + m.taskCounts()
	if ref := styDim.Render(m.runID()); ansi.StringWidth(head)+ansi.StringWidth(ref)+3 < w {
		head += strings.Repeat(" ", w-ansi.StringWidth(head)-ansi.StringWidth(ref)) + ref
	}
	var body []string
	section := func(name string) { body = append(body, "", styTitle.Render(name)) }
	body = append(body, styDim.Render("asked  ")+truncate(oneLine(m.snap.Prompt), w-8))
	if stages := m.flowStages(w); stages != "" {
		body = append(body, stages)
	}
	section("Answer")
	if res.Answer == "" {
		body = append(body, styDim.Render("  nothing yet — no task has finished with an outcome"))
	} else {
		if res.AnswerFrom != "" {
			body = append(body, styDim.Render("  from "+res.AnswerFrom))
		}
		for _, l := range renderMarkdown(res.Answer, w-2, lipgloss.NewStyle()) {
			body = append(body, "  "+l)
		}
	}
	section("Artifacts")
	if res.Artifacts.Empty() {
		body = append(body, styDim.Render("  none recorded"))
	} else {
		body = append(body, m.artifactLines(res, w, true)...)
	}
	section("Tasks")
	for _, t := range m.sortedTasks() {
		ic, st := taskIconStyle(t.State)
		line := "  " + st.Render(ic) + " " + styCyan.Render(padRight(t.ID, 14)) + " " + flattenMarkdown(firstLine(t.Outcome))
		body = append(body, ansi.Truncate(line, w, "…"))
	}

	keys := "o outputs · e write report · y copy answer · 1-9 open a PR or file · n next step · esc back"
	if m.composing {
		keys = m.composeLine(w) + "\n" + styDim.Render("enter start · ctrl-e open in $EDITOR · esc cancel")
	} else if n := m.noticeLine(w); n != "" {
		keys = n + "\n" + styDim.Render(keys)
	} else {
		keys = styDim.Render(keys)
	}
	avail := max(1, m.height-1-lipgloss.Height(keys))
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))
	return pinFooter(head+pos+"\n"+strings.Join(body[scroll:end], "\n"), keys, m.height)
}

// handleResultKey drives the result view's own keys; scrolling and esc
// fall through to the detail handler.
func (m tuiModel) handleResultKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	k := msg.String()
	switch {
	case k == "e":
		snap := m.snap
		return m, func() tea.Msg {
			path, err := store.ExportRun(store.DefaultExportDir(), snap, true)
			return noticeMsg{text: "report written to " + path, err: err}
		}, true
	case k == "y":
		res := m.snap.Result()
		if res.Answer == "" {
			m.setNotice("", fmt.Errorf("nothing to copy yet"))
			return m, nil, true
		}
		return m, copyCmd(res.Answer, "the answer"), true
	case len(k) == 1 && k[0] >= '1' && k[0] <= '9':
		return m, openArtifact(m.snap.Result().Artifacts, int(k[0]-'0'), m.snap.CWD), true
	case k == "n" || k == "i":
		m.startCompose(m.flowOf(m.live))
		return m, nil, true
	case k == "o" || k == "O":
		return m, m.openOutputs(""), true
	}
	return m, nil, false
}

// copyCmd puts text on the clipboard; what names it in the notice.
func copyCmd(text, what string) tea.Cmd {
	return func() tea.Msg {
		for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}} {
			if _, err := exec.LookPath(c[0]); err != nil {
				continue
			}
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err != nil {
				return noticeMsg{err: fmt.Errorf("%s: %w", c[0], err)}
			}
			return noticeMsg{text: fmt.Sprintf("copied %s (%d lines)", what, strings.Count(text, "\n")+1)}
		}
		return noticeMsg{err: fmt.Errorf("no clipboard command (pbcopy, wl-copy, xclip) found")}
	}
}

// openURLCmd opens a URL in the browser.
func openURLCmd(u string) tea.Cmd {
	return func() tea.Msg {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		if err := exec.Command(opener, u).Start(); err != nil {
			return noticeMsg{err: fmt.Errorf("%s: %w", opener, err)}
		}
		return noticeMsg{text: "opened " + prShort(u)}
	}
}

// setNotice shows a one-line result for a few seconds.
func (m *tuiModel) setNotice(text string, err error) {
	m.notice, m.noticeErr, m.noticeAt = text, err, time.Now()
}

// noticeLine is the fresh notice, or "".
func (m tuiModel) noticeLine(w int) string {
	if time.Since(m.noticeAt) > 5*time.Second {
		return ""
	}
	if m.noticeErr != nil {
		return ansi.Truncate(styRedBold.Render("✗ ")+styRed.Render(m.noticeErr.Error()), w, "…")
	}
	if m.notice != "" {
		return ansi.Truncate(styGreenBold.Render("✓ ")+m.notice, w, "…")
	}
	return ""
}
