package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/store"
)

// planFixture is a daemon-shaped server whose planner is a stand-in: a
// ccproxy on PATH that records its arguments and exits.
func planFixture(t *testing.T) (addr, argsFile string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args")
	// One file per invocation, written whole then renamed: two planners
	// run here, and a shared file could be read mid-write.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + ".$$.tmp && mv " + argsFile + ".$$.tmp " + argsFile + ".$$\n"
	if err := os.WriteFile(filepath.Join(bin, "ccproxy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	b := broker.New()
	srv := New(b, broker.NewAgentRegistry())
	srv.SetController(&seqController{cancelController{l: launcher.New("http://127.0.0.1:1", "r-x", t.TempDir())}, 0})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr, argsFile
}

// seqController is cancelController with a distinct id per run, which a
// flow of two steps needs.
type seqController struct {
	cancelController
	n int
}

func (c *seqController) NewRunID() string { c.n++; return fmt.Sprintf("r-%d", c.n) }

// KnownRuns is every run this controller named.
func (c *seqController) KnownRuns() []string {
	var out []string
	for i := 1; i <= c.n; i++ {
		out = append(out, fmt.Sprintf("r-%d", i))
	}
	return out
}

func postPlan(t *testing.T, addr string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/control/plan", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// A plan starts a flow; a plan with that flow is its next step, in its
// directory, planned with what the first step concluded.
func TestAPlanStartsAFlowAndAFollowUpContinuesIt(t *testing.T) {
	addr, argsFile := planFixture(t)
	repo := t.TempDir()
	code, out := postPlan(t, addr, map[string]any{"cwd": repo, "prompt": "plan the lock API"})
	if code != http.StatusCreated {
		t.Fatalf("plan: %d %v", code, out)
	}
	first, _ := out["run_id"].(string)
	if out["flow"] != first {
		t.Fatalf("a new request did not start its own flow: %v", out)
	}
	f, err := store.LoadFlow(first)
	if err != nil || f.Goal != "plan the lock API" || f.CWD != repo {
		t.Fatalf("flow %+v %v", f, err)
	}

	// The first step concludes something; the second must be told.
	snap := broker.Snapshot{RunID: first, Prompt: "plan the lock API", CWD: repo, State: broker.RunDone, Flow: first,
		Tasks: []broker.TaskSnapshot{{ID: "synthesis", Name: "synthesis", State: broker.TaskCompleted,
			Outcome: "Validate LockKey in a middleware."}}}
	if err := store.SaveRun(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFlowNotes(first, "keep the v1 wire format"); err != nil {
		t.Fatal(err)
	}

	code, out = postPlan(t, addr, map[string]any{"flow": first, "prompt": "build it"})
	if code != http.StatusCreated {
		t.Fatalf("follow-up: %d %v", code, out)
	}
	second, _ := out["run_id"].(string)
	status := getJSON(t, addr+"/api/v1/control/status?run_id="+second)
	got, _ := status["snapshot"].(map[string]any)
	if got["flow"] != first || got["parent_run"] != first || got["cwd"] != repo {
		t.Errorf("second step: flow=%v parent=%v cwd=%v", got["flow"], got["parent_run"], got["cwd"])
	}
	f, _ = store.LoadFlow(first)
	if len(f.Steps) != 2 || f.Steps[1].RunID != second || f.Steps[1].Request != "build it" {
		t.Errorf("steps %+v", f.Steps)
	}
	// The planner starts in the background; wait for the second one.
	var args []byte
	for i := 0; i < 100 && !strings.Contains(string(args), "build it"); i++ {
		time.Sleep(20 * time.Millisecond)
		files, _ := filepath.Glob(argsFile + ".*")
		for _, f := range files {
			if b, _ := os.ReadFile(f); strings.Contains(string(b), "build it") && !strings.HasSuffix(f, ".tmp") {
				args = b
			}
		}
	}
	for _, w := range []string{"Validate LockKey in a middleware.", "keep the v1 wire format", "flow-so-far.md"} {
		if !strings.Contains(string(args), w) {
			t.Errorf("the planner was not told %q:\n...%s", w, args[max(0, len(args)-1500):])
		}
	}

	if _, err := store.SetFlowClosed(first, true); err != nil {
		t.Fatal(err)
	}
	if code, _ := postPlan(t, addr, map[string]any{"flow": first, "prompt": "more"}); code != http.StatusConflict {
		t.Errorf("a closed flow took a step: %d", code)
	}
	if code, _ := postPlan(t, addr, map[string]any{"flow": "nope", "prompt": "x"}); code != http.StatusNotFound {
		t.Errorf("an unknown flow: %d", code)
	}
}

// A worker records what it makes through its token.
func TestAWorkerRecordsAnArtifact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("r1", "p", t.TempDir(), "main")
	run.AddTask("a", "a", "x", nil)
	agent := reg.Register("worker-a", "r1", false)
	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	post := func(kind, value string) int {
		raw, _ := json.Marshal(map[string]string{"kind": kind, "value": value})
		resp, err := http.Post(addr+"/api/v1/agents/"+agent.Token+"/task/a/artifact", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("pr", "https://github.com/o/r/pull/9"); c != http.StatusOK {
		t.Fatalf("record: %d", c)
	}
	if c := post("tarball", "x"); c != http.StatusBadRequest {
		t.Errorf("unknown kind: %d", c)
	}
	if a := run.Snapshot().Tasks[0].Artifacts; a == nil || a.PRs[0] != "https://github.com/o/r/pull/9" {
		t.Errorf("not recorded: %+v", a)
	}
}
