package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// runMainEnv makes TestMain run main() with these arguments, one per line,
// instead of the tests.
const runMainEnv = "NARWHAL_TEST_RUN_MAIN"

func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// runNarwhal runs `narwhal args...` as a separate process, the way an agent's
// shell does: stdin is /dev/null and stdout is a pipe, so neither is a
// terminal. HOME is a fresh directory, so no config, live run or daemon of
// the developer's is read.
func runNarwhal(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		runMainEnv+"="+strings.Join(args, "\n"),
		"HOME="+t.TempDir(),
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("run narwhal %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

// An agent types --version and -h by reflex. --version answered "unknown
// command" and exited 1, and a bare `narwhal` without a terminal failed on
// /dev/tty without saying what to run instead.
func TestArgumentHandling(t *testing.T) {
	versionLine := "narwhal " + version + "\n"
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string   // exact, when set
		stdoutHas  []string // substrings of stdout
		stderrHas  []string // substrings of stderr
		maxStderr  int      // max stderr lines, when set
	}{
		{name: "version", args: []string{"version"}, wantStdout: versionLine},
		{name: "--version", args: []string{"--version"}, wantStdout: versionLine},
		{name: "-v", args: []string{"-v"}, wantStdout: versionLine},
		{name: "-V", args: []string{"-V"}, wantStdout: versionLine},
		{name: "help", args: []string{"help"}, stdoutHas: []string{"Usage:", "narwhal show"}},
		{name: "-h", args: []string{"-h"}, stdoutHas: []string{"Usage:"}},
		{name: "--help", args: []string{"--help"}, stdoutHas: []string{"Usage:"}},
		{name: "unknown command", args: []string{"--bogus"}, wantCode: 1,
			stderrHas: []string{"unknown command: --bogus", "Usage:"}},
		{name: "bare without a terminal", args: nil, wantCode: exitNoTerminal, wantStdout: "",
			stderrHas: []string{"terminal", "narwhal show", "--prompt", "narwhal mcp", "narwhal daemon status"},
			maxStderr: 5},
		{name: "monitor without a terminal", args: []string{"monitor"}, wantCode: exitNoTerminal,
			stderrHas: []string{"narwhal show"}, maxStderr: 5},
		{name: "monitor --list needs no terminal", args: []string{"monitor", "--list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := runNarwhal(t, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.wantCode, stdout, stderr)
			}
			if tt.wantStdout != "" && stdout != tt.wantStdout {
				t.Errorf("stdout %q, want %q", stdout, tt.wantStdout)
			}
			for _, s := range tt.stdoutHas {
				if !strings.Contains(stdout, s) {
					t.Errorf("stdout lacks %q:\n%s", s, stdout)
				}
			}
			for _, s := range tt.stderrHas {
				if !strings.Contains(stderr, s) {
					t.Errorf("stderr lacks %q:\n%s", s, stderr)
				}
			}
			if strings.Contains(stderr, "/dev/tty") {
				t.Errorf("stderr is the raw TTY error:\n%s", stderr)
			}
			if n := strings.Count(stderr, "\n"); tt.maxStderr > 0 && n > tt.maxStderr {
				t.Errorf("stderr is %d lines, want at most %d:\n%s", n, tt.maxStderr, stderr)
			}
		})
	}
}

func TestShowJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var out, errb bytes.Buffer
	if code := runShow(&out, &errb, []string{"--json"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("no runs: %q, want []", out.String())
	}

	snap := broker.Snapshot{
		RunID:  "r-json",
		Prompt: "audit the auth module",
		State:  broker.RunDone,
		Tasks: []broker.TaskSnapshot{
			{ID: "task-1", State: broker.TaskCompleted},
			{ID: "task-2", State: broker.TaskFailed},
		},
	}
	if err := store.SaveRun(snap); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := runShow(&out, &errb, []string{"--json"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var runs []runSummary
	if err := json.Unmarshal(out.Bytes(), &runs); err != nil {
		t.Fatalf("list is not JSON: %v\n%s", err, out.String())
	}
	want := runSummary{RunID: "r-json", Prompt: "audit the auth module", State: broker.RunDone,
		Tasks: 2, Done: 1, Failed: 1}
	if len(runs) != 1 || runs[0] != want {
		t.Errorf("list = %+v, want [%+v]", runs, want)
	}

	// The flag goes on either side of the run id.
	for _, args := range [][]string{{"--json", "r-json"}, {"r-json", "--json"}} {
		out.Reset()
		if code := runShow(&out, &errb, args); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
		var got broker.Snapshot
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%v: snapshot is not JSON: %v\n%s", args, err, out.String())
		}
		if got.RunID != "r-json" || len(got.Tasks) != 2 {
			t.Errorf("%v: snapshot = %+v", args, got)
		}
	}

	if code := runShow(&out, &errb, []string{"r-missing", "--json"}); code != 1 {
		t.Errorf("missing run exited %d, want 1", code)
	}
}
