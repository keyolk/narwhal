// transcript.go reads a worker's Claude session transcript and renders it
// as a readable activity feed.
//
// This is what makes a running worker observable from inside the monitor.
// The captured stdout log is empty until a `--print` worker exits, so for
// the whole time you would want to watch one there is nothing to show. The
// transcript, on the other hand, is appended as the session happens.
//
// Attaching (`a`) hands the terminal over to a real Claude session, which
// is right when you want to read the whole thing or intervene. Most of the
// time the question is smaller — what is this worker doing right now? —
// and leaving the monitor to answer it is too much ceremony.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// transcriptEntry is one rendered line of a worker's activity.
type transcriptEntry struct {
	at   time.Time
	kind string // "text", "tool", "result"
	text string
}

// transcriptPath resolves a session id to the file Claude Code writes it
// to: ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl.
//
// The directory name is the working directory with every character that is
// not alphanumeric replaced by a hyphen — "/private/tmp/x" becomes
// "-private-tmp-x". Note this is the *resolved* path: /tmp is a symlink to
// /private/tmp on macOS, and Claude records the resolved form, so a
// transcript for a run whose cwd was recorded as /tmp/x is filed under
// -private-tmp-x.
func transcriptPath(cwd, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil || sessionID == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String(), sessionID+".jsonl")
}

// readTranscript parses a session transcript into activity entries.
//
// Only assistant text, tool calls, and tool results are kept. The file also
// carries attachments, queue operations and prompt bookkeeping, none of
// which say anything about what the worker is doing.
//
// A malformed line is skipped rather than failing the read: the file is
// being appended to while we read it, so the last line is routinely a
// partial write.
func readTranscript(path string) []transcriptEntry {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var out []transcriptEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Type != "assistant" && rec.Type != "user" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, rec.Timestamp)
		out = append(out, parseContent(rec.Message.Content, at)...)
	}
	return out
}

// parseContent turns one message's content into entries. Content is either
// a bare string (the initial prompt) or a list of typed blocks.
func parseContent(raw json.RawMessage, at time.Time) []transcriptEntry {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []transcriptEntry{{at: at, kind: "text", text: s}}
	}

	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}

	var out []transcriptEntry
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, transcriptEntry{at: at, kind: "text", text: b.Text})
		case "tool_use":
			out = append(out, transcriptEntry{
				at: at, kind: "tool", text: summarizeToolUse(b.Name, b.Input),
			})
		case "tool_result":
			text := decodeResult(b.Content)
			if strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, transcriptEntry{at: at, kind: "result", text: text})
		}
	}
	return out
}

// summarizeToolUse renders a tool call as one line. The full input is
// often a whole file's contents or a long command; what identifies the
// call is the tool plus its most distinguishing argument.
func summarizeToolUse(name string, input json.RawMessage) string {
	var args map[string]any
	_ = json.Unmarshal(input, &args)

	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}

	switch name {
	case "Bash":
		cmd := pick("command")
		// Wrapper scripts are the interesting Bash calls and their paths
		// are long and identical up to the last segment, so lead with the
		// script name: "send worklog ..." rather than a path prefix that
		// pushes the arguments off the line.
		if i := strings.Index(cmd, "/agents/"); i >= 0 {
			if j := strings.Index(cmd[i:], "/scripts/"); j >= 0 {
				cmd = strings.TrimPrefix(cmd[i+j+len("/scripts/"):], "/")
			}
		}
		return "Bash  " + oneLine(cmd)
	case "Read", "Write", "Edit":
		return name + "  " + shortenPath(pick("file_path"))
	case "Grep", "Glob":
		return name + "  " + oneLine(pick("pattern", "query"))
	default:
		if s := pick("command", "file_path", "pattern", "prompt", "description"); s != "" {
			return name + "  " + oneLine(s)
		}
		return name
	}
}

// decodeResult pulls readable text out of a tool result, which is either a
// string or a list of content blocks.
func decodeResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// oneLine collapses whitespace so a multi-line command does not break the
// one-entry-per-line layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// shortenPath keeps the last two segments, which is what tells two files
// apart in a feed.
func shortenPath(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, string(filepath.Separator))
	if len(parts) <= 2 {
		return p
	}
	return filepath.Join(parts[len(parts)-2:]...)
}

// renderTranscript formats entries for the activity pane, newest last.
//
// Results are clipped to a couple of lines: a tool result can be an entire
// file, and the feed is for seeing the shape of what a worker is doing, not
// for reading its inputs. Assistant text is kept in full — it is the
// worker explaining itself, which is the most informative part.
func renderTranscript(entries []transcriptEntry, width int) []string {
	// Three kinds of line, three weights. A tool call is what the worker
	// *did* and carries the colour; its result is evidence and recedes;
	// the worker's own prose is what it *thinks* and reads at full
	// contrast. Undifferentiated, the feed was a wall of grey where the
	// actions and the reasoning looked alike.
	var out []string
	for _, e := range entries {
		stamp := ""
		if !e.at.IsZero() {
			stamp = e.at.Format("15:04:05") + " "
		}
		pad := strings.Repeat(" ", len(stamp)) + "  "
		switch e.kind {
		case "tool":
			out = append(out, styDim.Render(stamp)+styCyan.Render("→ ")+
				styCyan.Render(truncate(e.text, width-len(stamp)-2)))
		case "result":
			for _, l := range clipLines(e.text, 2) {
				out = append(out, pad+styDim.Render(truncate(oneLine(l), width-len(pad))))
			}
		default:
			for i, l := range wrapText(e.text, width-len(stamp)-2) {
				if i == 0 {
					out = append(out, styDim.Render(stamp)+styGreen.Render("· ")+l)
					continue
				}
				out = append(out, pad+l)
			}
		}
	}
	return out
}

// clipLines returns at most n non-empty lines, noting how many were cut.
func clipLines(s string, n int) []string {
	var kept []string
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		kept = append(kept, l)
		if len(kept) == n {
			break
		}
	}
	if len(lines) > len(kept) {
		kept = append(kept, fmt.Sprintf("… %d more lines", len(lines)-len(kept)))
	}
	return kept
}
