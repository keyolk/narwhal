// grounds.go finds what a question rests on, for an operator who did not
// read what the asker read.
//
// A planner asked "install SessionStart only and hold UserPromptSubmit
// (N3: p99 55.6ms > 50ms)?" — N3 was a gap named in the previous step's
// findings, which the planner had read and the operator, looking at the
// questions view, had not. Asking for context helps; an asker that forgets
// still leaves the operator unable to answer. So the question carries the
// sentences of the asker's sources that define the labels it uses.
package broker

import (
	"regexp"
	"strings"
)

// labelPattern matches the short labels findings name things by: N3, G2,
// I4, R10, Q8, T1, M5 — and "step 3".
var labelPattern = regexp.MustCompile(`\b([A-Z]{1,2}[0-9]{1,2})\b|(?i)\b(step [0-9]+)\b`)

// Labels returns the labels text uses, in order, once each.
func Labels(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range labelPattern.FindAllStringSubmatch(text, -1) {
		l := m[1]
		if l == "" {
			l = m[2]
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// GroundsFor returns, for each label the question uses, the passages of
// source that say what it is: up to two per label, each clipped to a
// sentence's worth around the label.
func GroundsFor(question, source string) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	var out []string
	for _, l := range Labels(question) {
		flags := ""
		if strings.Contains(l, " ") {
			flags = "(?i)"
		}
		re := regexp.MustCompile(flags + `\b` + regexp.QuoteMeta(l) + `\b`)
		n := 0
		lines := strings.Split(source, "\n")
		for i, line := range lines {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				// A heading names the thing; what it is comes after it.
				for _, next := range lines[i+1:] {
					// The first line of prose: a run id or a status line
					// under the heading says nothing about what it is.
					if t := strings.TrimSpace(next); len([]rune(t)) >= 40 && !strings.HasPrefix(t, "#") {
						line = strings.TrimLeft(strings.TrimSpace(line), "# ") + " — " + t
						loc = []int{0, 0}
						break
					}
				}
			}
			out = append(out, l+": "+around(line, loc[0], 220))
			if n++; n == 2 {
				break
			}
		}
	}
	return out
}

// around is the part of line near byte offset at, about n runes long, cut
// on a word boundary.
func around(line string, at, n int) string {
	r := []rune(line)
	pos := len([]rune(line[:at]))
	start := max(0, pos-n/3)
	end := min(len(r), start+n)
	for start > 0 && start < pos && r[start-1] != ' ' && r[start-1] != '(' {
		start++
	}
	s := strings.TrimSpace(string(r[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}
