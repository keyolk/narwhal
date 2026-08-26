// check_report.go renders what each task was asked to verify, and what it
// answered.
//
// Without this the end conditions are write-only, which is the state the
// persisted run graph was in before #39 read it back at plan time. The
// question it answers is the one run s1787538246213-1 could not be asked:
// that run reported 8 where the answer was 0 and finished 3/3 completed,
// and nothing in the record distinguished it from a run that was right.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// CheckReport renders the end conditions for one run, or "" when no task
// carried one.
func CheckReport(s broker.Snapshot) string {
	tasks := append([]broker.TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	var b strings.Builder
	any := false
	for _, t := range tasks {
		c := strings.TrimSpace(t.Check)
		if c == "" {
			continue
		}
		if !any {
			b.WriteString("Checks\n")
			any = true
		}
		fmt.Fprintf(&b, "  %s\n", t.ID)
		fmt.Fprintf(&b, "    asked:  %s\n", c)
		if r := strings.TrimSpace(t.CheckResult); r != "" {
			fmt.Fprintf(&b, "    showed: %s\n", r)
		} else {
			// A completed task with an unanswered check went through
			// harvest, where the broker was gone and the worker had
			// already exited. Saying so beats an empty line that reads
			// like the check passed.
			fmt.Fprintf(&b, "    showed: (not answered)\n")
		}
	}
	if !any {
		return ""
	}
	return b.String()
}

// checkLine summarises a run's checks for a list row: how many tasks were
// asked, and how many answered.
//
// Both numbers, because they fail differently. No checks at all means the
// planner set none; checks asked and unanswered means the run completed
// off a path that could not ask.
func checkLine(s broker.Snapshot) string {
	asked, answered := 0, 0
	for _, t := range s.Tasks {
		if strings.TrimSpace(t.Check) == "" {
			continue
		}
		asked++
		if strings.TrimSpace(t.CheckResult) != "" {
			answered++
		}
	}
	if asked == 0 {
		return ""
	}
	return fmt.Sprintf("checks %d/%d answered", answered, asked)
}
