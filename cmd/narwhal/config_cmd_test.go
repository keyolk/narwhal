package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/config"
)

func TestConfigCommandSetsListsAndUnsets(t *testing.T) {
	config.UseFile(filepath.Join(t.TempDir(), "config.json"))
	t.Cleanup(func() { config.UseFile("") })
	var out bytes.Buffer
	if err := runConfig(&out, []string{"set", "jev.followed.threshold", "0.5"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runConfig(&out, []string{"list", "jev.followed"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0.5") || !strings.Contains(out.String(), "(default 0.4)") {
		t.Errorf("list:\n%s", out.String())
	}
	if err := runConfig(&out, []string{"set", "jev.nope", "1"}); err == nil {
		t.Error("unknown key accepted")
	}
	out.Reset()
	runConfig(&out, []string{"unset", "jev.followed.threshold"})
	out.Reset()
	runConfig(&out, []string{"get", "jev.followed.threshold"})
	if strings.TrimSpace(out.String()) != "0.4" {
		t.Errorf("after unset: %q", out.String())
	}
}
