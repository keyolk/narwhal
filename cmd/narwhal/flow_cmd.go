// flow_cmd.go is `narwhal flow`: list flows, show one's steps, and take a
// step out of a flow. The monitor's flow view does the same with d.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/keyolk/narwhal/internal/store"
)

func flowCmd(args []string) {
	if err := runFlowCmd(os.Stdout, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runFlowCmd(w io.Writer, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		for _, f := range store.ListFlows() {
			state := "open"
			if f.Closed {
				state = "closed"
			}
			fmt.Fprintf(w, "%s  %-6s  %d steps  %s\n", f.ID, state, len(f.Steps), f.Title)
		}
		return nil
	}
	switch args[0] {
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: narwhal flow show FLOW")
		}
		f, err := store.LoadFlow(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s  %s\n", f.ID, f.Title)
		for i, s := range f.Steps {
			fmt.Fprintf(w, "  %d. %s  %s\n", i+1, s.RunID, truncate(oneLine(s.Request), 100))
		}
		if len(f.Removed) > 0 {
			fmt.Fprintf(w, "  removed: %v\n", f.Removed)
		}
	case "rm", "remove":
		if len(args) != 3 {
			return fmt.Errorf("usage: narwhal flow rm FLOW STEP   (STEP is its number or run id)")
		}
		f, removed, err := store.RemoveFlowStep(args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (%s) from flow %s; its run record is kept\n",
			removed.RunID, truncate(oneLine(removed.Request), 60), args[1])
		if len(f.Steps) == 0 {
			fmt.Fprintf(w, "flow %s had no steps left and was removed\n", args[1])
		}
	default:
		return fmt.Errorf("unknown flow command %q: list | show FLOW | rm FLOW STEP", args[0])
	}
	return nil
}
