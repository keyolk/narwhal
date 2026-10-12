package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/launcher"
)

// recordingWorker is a ccproxy that writes the arguments it was started
// with, one per line, and exits.
func recordingWorker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	// Written whole and renamed into place: a reader polling the file
	// must never see it half-written.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + ".tmp && mv " + out + ".tmp " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ccproxy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return out
}

func TestAReopenedTaskResumesItsWorkersSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	argsFile := recordingWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	cwd := t.TempDir()
	run := sess.Broker.CreateRun("r-fu", "test", cwd, "main")
	task := run.AddTask("schema", "schema", "add the field", nil)
	task.StartDispatch("d1", "worker-schema")
	task.CompleteDispatch("field added", run)

	// What the first worker left: its session id, and its transcript.
	const sid = "11111111-2222-4333-8444-555555555555"
	agentDir := filepath.Join(home, ".narwhal", "sessions", "r-fu", "agents", "worker-schema")
	must(t, os.MkdirAll(agentDir, 0o700))
	must(t, os.WriteFile(filepath.Join(agentDir, "claude-session-id"), []byte(sid+"\n"), 0o600))
	tp := launcher.TranscriptPath(cwd, sid)
	must(t, os.MkdirAll(filepath.Dir(tp), 0o700))
	must(t, os.WriteFile(tp, []byte("{}\n"), 0o600))

	task.Reopen("also cover the legacy payload")
	sess.LauncherFor("r-fu", cwd)
	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	var args string
	waitFor(t, "the reopened task to be dispatched", func() bool {
		b, err := os.ReadFile(argsFile)
		args = string(b)
		return err == nil && len(b) > 0
	})
	if !strings.Contains(args, "--resume\n"+sid+"\n") {
		t.Errorf("the worker was not resumed in its session:\n%s", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Errorf("a resumed worker was also given a new session id:\n%s", args)
	}
	if !strings.Contains(args, "also cover the legacy payload") || !strings.Contains(args, "reopened") {
		t.Errorf("the instruction did not reach the resumed worker:\n%s", args)
	}
}

func TestAReopenedTaskWithNoSessionStartsFreshWithTheInstruction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	argsFile := recordingWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-fu2", "test", t.TempDir(), "main")
	task := run.AddTask("schema", "schema", "add the field", nil)
	task.StartDispatch("d1", "worker-schema")
	task.CompleteDispatch("field added", run)
	task.Reopen("also cover the legacy payload")
	sess.LauncherFor("r-fu2", run.CWD)
	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	var args string
	waitFor(t, "the reopened task to be dispatched", func() bool {
		b, err := os.ReadFile(argsFile)
		args = string(b)
		return err == nil && len(b) > 0
	})
	for _, want := range []string{"--session-id", "add the field", "field added", "also cover the legacy payload"} {
		if !strings.Contains(args, want) {
			t.Errorf("a fresh worker for a reopened task lacks %q:\n%s", want, args)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
