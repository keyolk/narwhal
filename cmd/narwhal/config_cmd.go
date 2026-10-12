// config_cmd.go is `narwhal config`: read and change the settings in
// ~/.narwhal/config.json (internal/config). A change applies to the
// running daemon's next judgement; nothing restarts.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/keyolk/narwhal/internal/config"
)

func configCmd(args []string) {
	if err := runConfig(os.Stdout, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runConfig(w io.Writer, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		prefix := ""
		if len(args) > 1 {
			prefix = args[1]
		}
		return listConfig(w, prefix)
	}
	switch args[0] {
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: narwhal config get KEY")
		}
		if _, ok := config.Lookup(args[1]); !ok {
			return fmt.Errorf("unknown setting %q (narwhal config lists them)", args[1])
		}
		v, _ := config.Value(args[1])
		fmt.Fprintln(w, v)
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("usage: narwhal config set KEY VALUE")
		}
		if err := config.Set(args[1], args[2]); err != nil {
			return err
		}
		v, _ := config.Value(args[1])
		fmt.Fprintf(w, "%s = %s  (%s)\n", args[1], v, config.Path())
	case "unset", "reset":
		if len(args) != 2 {
			return fmt.Errorf("usage: narwhal config unset KEY")
		}
		if err := config.Unset(args[1]); err != nil {
			return err
		}
		v, _ := config.Value(args[1])
		fmt.Fprintf(w, "%s = %s  (default)\n", args[1], v)
	case "path":
		fmt.Fprintln(w, config.Path())
	default:
		return fmt.Errorf("unknown config command %q: list [PREFIX] | get KEY | set KEY VALUE | unset KEY | path", args[0])
	}
	return nil
}

func listConfig(w io.Writer, prefix string) error {
	fmt.Fprintf(w, "# %s\n", config.Path())
	if err := config.Err(); err != nil {
		fmt.Fprintf(w, "# not read, defaults in force: %v\n", err)
	}
	width := 0
	keys := config.Keys()
	for _, k := range keys {
		width = max(width, len(k.Name))
	}
	for _, k := range keys {
		if !strings.HasPrefix(k.Name, prefix) {
			continue
		}
		v, set := config.Value(k.Name)
		note := ""
		if set && v != k.Default {
			note = "  (default " + k.Default + ")"
		}
		fmt.Fprintf(w, "%-*s  %-6s%s\n%-*s    %s\n", width, k.Name, v, note, width, "", k.Help)
	}
	return nil
}
