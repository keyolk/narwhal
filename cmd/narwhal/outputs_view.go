// outputs_view.go draws the Outputs view, each kind read its own way.
//
// One renderer for everything was wrong in both directions: markdown
// rendering turned a Go file's comments into headings and its indentation
// into nothing, and a PR, a branch or a commit has no text of its own at
// all — what there is to read about one is what gh or git says.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/render"
)

// fileReadCap bounds what is read of a file for the pane; o pages the rest.
const fileReadCap = 512 << 10

// outputKindStyle colours a kind in the list.
func outputKindStyle(k outputKind) lipgloss.Style {
	switch k {
	case outFile:
		return styCyan
	case outPR:
		return styGreen
	case outBranch:
		return styMagenta
	case outCommit:
		return styYellow
	case outReport:
		return styBlue
	}
	return styDim
}

// detailCmd asks gh or git about a PR, a branch or a commit.
func detailCmd(o output) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var text string
		var err error
		switch o.kind {
		case outPR:
			text, err = prDetail(ctx, o.ref)
		case outCommit:
			text, err = gitOut(ctx, o.cwd, "show", "--stat", "--no-color", "--format=commit %H%nAuthor: %an%nDate:   %ad%n%n%B", o.ref)
		case outBranch:
			text, err = gitOut(ctx, o.cwd, "log", "--no-color", "--format=%h %s  (%ar)", "-n", "50", branchRange(o.cwd, o.ref))
			if err == nil && strings.TrimSpace(text) == "" {
				text = "no commits beyond the default branch"
			}
		}
		return outputDetailMsg{key: o.key(), text: text, err: err}
	}
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// branchRange is what a branch adds: its commits beyond the remote's
// default branch when there is one, else the branch itself.
func branchRange(dir, branch string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ref := branch
	if _, err := gitOut(ctx, dir, "rev-parse", "--verify", "-q", branch); err != nil {
		ref = "origin/" + branch
	}
	if _, err := gitOut(ctx, dir, "rev-parse", "--verify", "-q", "origin/HEAD"); err == nil {
		return "origin/HEAD.." + ref
	}
	return ref
}

// prDetail is a PR as markdown: its title, state, size and description.
func prDetail(ctx context.Context, url string) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json",
		"title,state,author,additions,deletions,changedFiles,headRefName,baseRefName,body").Output()
	if err != nil {
		var ee *exec.ExitError
		if e, ok := err.(*exec.ExitError); ok {
			ee = e
		}
		if ee != nil && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("gh: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("gh: %w", err)
	}
	var pr struct {
		Title, State, HeadRefName, BaseRefName, Body string
		Author                                       struct{ Login string }
		Additions, Deletions, ChangedFiles           int
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return "", fmt.Errorf("gh output: %w", err)
	}
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		body = "_no description_"
	}
	return fmt.Sprintf("# %s\n\n**%s** · `%s` → `%s` · +%d −%d in %d files · %s\n\n%s\n",
		pr.Title, strings.ToLower(pr.State), pr.HeadRefName, pr.BaseRefName,
		pr.Additions, pr.Deletions, pr.ChangedFiles, pr.Author.Login, body), nil
}

// outputBodyCache memoizes a rendered body: the view is drawn on every
// animation frame and a plan is hundreds of lines of markdown.
var outputBodyCache = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

// outputBody is the selected output as the pane shows it, with a line
// saying where it came from.
func (m tuiModel) outputBody(o output, width int) []string {
	src := styDim.Render(outputLabels[o.kind] + " · " + o.task)
	switch o.kind {
	case outFile:
		return append([]string{styDim.Render("file · " + abbreviatePath(o.path) + " · recorded by " + o.task)}, fileBody(o.path, width)...)
	case outPR, outBranch, outCommit:
		head := styDim.Render(outputLabels[o.kind] + " · " + o.ref + " · from " + o.task)
		d := m.outputDetails[o.key()]
		switch {
		case d.pending:
			return []string{head, "", styDim.Render("  asking " + map[bool]string{true: "gh", false: "git"}[o.kind == outPR] + "…")}
		case d.err != nil:
			return []string{head, "", styRed.Render(d.err.Error())}
		case o.kind == outPR:
			return append([]string{head, ""}, cachedRender("pr|"+o.ref, d.text, width, true)...)
		}
		return append([]string{head, ""}, cachedRender("git|"+o.key(), d.text, width, false)...)
	case outReport, outAnswer:
		src = styDim.Render(outputLabels[o.kind] + " · " + o.task + " · " + o.from)
	case outRequest:
		src = styDim.Render("what this step was asked")
	}
	return append([]string{src, ""}, cachedRender(o.key()+o.task+o.from, o.text, width, true)...)
}

// cachedRender renders text as markdown or as plain lines, memoized.
func cachedRender(id, text string, width int, markdown bool) []string {
	key := fmt.Sprintf("%s|%d|%v|%d|%s", id, width, markdown, len(text), headOf(text))
	outputBodyCache.Lock()
	if c, ok := outputBodyCache.m[key]; ok {
		outputBodyCache.Unlock()
		return c
	}
	outputBodyCache.Unlock()
	var out []string
	if markdown {
		out = renderMarkdown(text, width, lipgloss.NewStyle())
	} else {
		for _, l := range strings.Split(strings.TrimRight(expandTabs(text), "\n"), "\n") {
			out = append(out, render.Truncate(l, width))
		}
	}
	outputBodyCache.Lock()
	if len(outputBodyCache.m) > 200 {
		outputBodyCache.m = map[string][]string{}
	}
	outputBodyCache.m[key] = out
	outputBodyCache.Unlock()
	return out
}

func headOf(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// markdownExts are the files rendered rather than shown as written.
var markdownExts = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

// fileBody reads a file the way its kind is read: markdown rendered, a
// directory listed, a binary file described, anything else as written with
// line numbers.
func fileBody(path string, width int) []string {
	fi, err := os.Stat(path)
	if err != nil {
		return []string{"", styRed.Render(err.Error())}
	}
	info := styDim.Render(fmt.Sprintf("%s · modified %s ago", humanSize(fi.Size()), ago(fi.ModTime())))
	if fi.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return []string{"", styRed.Render(err.Error())}
		}
		out := []string{styDim.Render(fmt.Sprintf("directory · %d entries", len(entries))), ""}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name = styCyan.Render(name + "/")
			}
			out = append(out, "  "+name)
		}
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return []string{"", styRed.Render(err.Error())}
	}
	defer f.Close()
	buf := make([]byte, min(fi.Size(), fileReadCap))
	n, _ := f.Read(buf)
	data := buf[:n]
	if strings.IndexByte(string(data[:min(len(data), 8000)]), 0) >= 0 {
		return []string{info, "", styDim.Render("  a binary file — o to open it with $PAGER")}
	}
	id := fmt.Sprintf("file|%s|%d", path, fi.ModTime().UnixNano())
	var body []string
	if markdownExts[strings.ToLower(filepath.Ext(path))] {
		body = cachedRender(id, string(data), width, true)
	} else {
		body = numbered(cachedRender(id, string(data), max(4, width-6), false))
	}
	out := append([]string{info, ""}, body...)
	if fi.Size() > fileReadCap {
		out = append(out, "", styDim.Render(fmt.Sprintf("… the first %s of %s — o reads it all", humanSize(fileReadCap), humanSize(fi.Size()))))
	}
	return out
}

// numbered puts a line-number gutter before code.
func numbered(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = styDim.Render(fmt.Sprintf("%4d ", i+1)) + l
	}
	return out
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// outputRows is the reading pane's height.
func (m tuiModel) outputRows() int { return max(3, m.height-6) }

// outputList is the left pane's lines and the line the selection is on:
// grouped by kind, and in a flow by step first.
func (m tuiModel) outputList(outs []output, lw int) ([]string, int) {
	var lines []string
	selLine := 0
	flow := len(m.outputsRuns) > 0
	lastStep, lastKind := -1, outputKind(-1)
	for i, o := range outs {
		if flow && o.step != lastStep {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, styTitle.Render(render.Truncate(fmt.Sprintf("Step %d  %s", o.step+1, o.run), lw)))
			lastStep, lastKind = o.step, -1
		}
		if o.kind != lastKind {
			lines = append(lines, outputKindStyle(o.kind).Render(" "+kindHeading(o.kind)))
			lastKind = o.kind
		}
		label := render.Truncate(o.title, max(4, lw-14))
		task := render.PadRight(render.Truncate(o.task, 10), 10)
		if i == m.outputSel {
			selLine = len(lines)
			lines = append(lines, stySel.Render(render.PadRight(render.Truncate("  "+task+" "+label, lw), lw)))
			continue
		}
		lines = append(lines, "  "+styDim.Render(task)+" "+label)
	}
	return lines, selLine
}

func kindHeading(k outputKind) string {
	switch k {
	case outRequest:
		return "Asked"
	case outFile:
		return "Files"
	case outPR:
		return "Pull requests"
	case outBranch:
		return "Branches"
	case outCommit:
		return "Commits"
	case outReport:
		return "Reports"
	}
	return "Answers"
}

// viewOutputs draws the list on the left and the selected output on the
// right.
func (m tuiModel) viewOutputs() string {
	w := max(m.width, 1)
	outs := m.outputs()
	var sel *output
	if m.outputSel < len(outs) {
		sel = &outs[m.outputSel]
	}
	from, prompt := m.snap.RunID, m.snap.Prompt
	if len(m.outputsRuns) > 0 {
		from = fmt.Sprintf("%d steps", len(m.outputsRuns))
		if f, ok := m.flows[m.flowID]; ok {
			prompt = f.Title
		}
	}
	head := styDim.Render("‹ ") + styCyanBold.Render("Outputs") + styDim.Render(fmt.Sprintf("   %d from %s", len(outs), from))
	title := "  " + render.Truncate(oneLine(prompt), w-3)
	g := m.outputsGeom()
	keys, bodyH, rows, lw, rw := g.keys, g.bodyH, g.rows, g.lw, g.rw

	list, selLine := m.outputList(outs, lw)
	if len(outs) == 0 {
		list = []string{styDim.Render("  nothing yet")}
	}
	// Keep the selection on screen, with its heading when it fits.
	top := 0
	if selLine >= rows {
		top = selLine - rows + 1
	}
	list = list[top:min(len(list), top+rows)]
	left := append([]string{paneTitle("Outputs", false, lw)}, list...)

	right := []string{paneTitle("", false, rw)}
	if sel != nil {
		lines, scroll := g.lines, g.scroll
		end := min(len(lines), scroll+rows)
		label := render.Truncate(sel.title, max(4, rw-20))
		if len(lines) > rows {
			label += fmt.Sprintf("  %d-%d/%d", scroll+1, end, len(lines))
			if end < len(lines) {
				label += " ↓"
			}
		}
		right = append([]string{paneTitle(label, true, rw)}, lines[scroll:end]...)
	}
	body := framedBand(lw, rw, rows, padRows(left, lw, bodyH-2), padRows(right, rw, bodyH-2))
	return pinFooter(head+"\n"+title+"\n"+body, keys, m.height)
}

// outputsGeom is the Outputs screen's layout: the footer, the band's height,
// the rows each column shows and their widths, and the selected output's
// lines with the scroll they are drawn from. viewOutputs draws with it and
// drag selection reads it, so both use one set of numbers.
type outputsGeom struct {
	keys        string
	bodyH, rows int
	lw, rw      int
	lines       []string
	scroll      int
	top         int // the band's top border row
}

func (m tuiModel) outputsGeom() outputsGeom {
	w := max(m.width, 1)
	g := outputsGeom{keys: m.footerBlock(w, m.keyFooter(m.screenStack(), w))}
	g.bodyH = max(3, m.height-2-lipgloss.Height(g.keys))
	g.rows = max(1, g.bodyH-3)
	g.lw = min(max(w/3, 28), 48)
	g.rw = max(10, w-g.lw-bandBorderCols)
	// The head and the prompt line, one row each: the head is a label and a
	// count, far short of any width the screen is drawn at, and the prompt
	// line is truncated to the width.
	g.top = 2
	outs := m.outputs()
	if m.outputSel < len(outs) {
		g.lines = m.outputBody(outs[m.outputSel], g.rw)
		g.scroll = min(m.outputScroll, max(0, len(g.lines)-g.rows))
	}
	return g
}
