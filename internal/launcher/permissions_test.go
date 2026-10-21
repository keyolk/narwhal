package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func settingsRules(t *testing.T, args []string) map[string][]string {
	t.Helper()
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("no --settings in %v", args)
	}
	var s struct {
		Permissions map[string][]string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(args[i+1]), &s); err != nil {
		t.Fatal(err)
	}
	return s.Permissions
}

func TestPermissionArgsDefaultIsBypass(t *testing.T) {
	got, err := permissionArgs(broker.Permissions{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"--permission-mode", "bypassPermissions"}) {
		t.Fatalf("args = %v", got)
	}
}

func TestPermissionArgsDontAskAllowsTheWorkersOwnScripts(t *testing.T) {
	// In dontAsk a worker that cannot run its own wrapper scripts cannot
	// report, and its task fails although the investigation succeeded.
	dir := t.TempDir()
	for _, name := range []string{"send", "task-done"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p := broker.Permissions{Mode: "dontAsk", Allow: []string{"Read"}, Deny: []string{"Bash(kubectl delete:*)"}}
	got, err := permissionArgs(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "--permission-mode" || got[1] != "dontAsk" {
		t.Fatalf("mode args = %v", got[:2])
	}
	rules := settingsRules(t, got)
	done := filepath.Join(dir, "task-done")
	for _, want := range []string{"Read", "Bash(bash " + done + ":*)", "Bash(" + done + ":*)"} {
		if !slices.Contains(rules["allow"], want) {
			t.Errorf("allow is missing %q: %v", want, rules["allow"])
		}
	}
	if !slices.Equal(rules["deny"], p.Deny) {
		t.Errorf("deny = %v, want %v", rules["deny"], p.Deny)
	}
}

func TestPermissionArgsDenyInBypassMode(t *testing.T) {
	got, err := permissionArgs(broker.Permissions{Deny: []string{"Bash(rm:*)"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "bypassPermissions" {
		t.Fatalf("mode = %q", got[1])
	}
	if rules := settingsRules(t, got); !slices.Equal(rules["deny"], []string{"Bash(rm:*)"}) || len(rules["allow"]) != 0 {
		t.Errorf("rules = %v", rules)
	}
}
