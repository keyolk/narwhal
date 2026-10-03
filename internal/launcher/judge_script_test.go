package launcher

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestJudgeScriptIsValidBash(t *testing.T) {
	dir := agentScripts(t, "http://127.0.0.1:1")
	if out, err := exec.Command("bash", "-n", filepath.Join(dir, "scripts", "judge")).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

// The script is how a worker actually asks, so drive it the way a worker
// does — by argument and by stdin — against a live endpoint.
func TestJudgeScriptPostsQuestionAndState(t *testing.T) {
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/judge") {
			t.Errorf("posted to %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]string
		json.Unmarshal(raw, &m)
		got = append(got, m)
		w.Write([]byte(`{"decided":true,"probability":0.9}`))
	}))
	defer srv.Close()
	dir := agentScripts(t, srv.URL)
	script := filepath.Join(dir, "scripts", "judge")

	out, err := exec.Command("bash", script, "Is it flaky?", "FAIL: TestX 'quoted' \"both\"").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"probability":0.9`) {
		t.Fatalf("by argument: %v %s", err, out)
	}
	cmd := exec.Command("bash", script, "Does this answer it?")
	cmd.Stdin = strings.NewReader("line one\nline two\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("by stdin: %v %s", err, out)
	}
	if len(got) != 2 || got[0]["question"] != "Is it flaky?" ||
		got[0]["state"] != "FAIL: TestX 'quoted' \"both\"" || !strings.Contains(got[1]["state"], "line two") {
		t.Errorf("requests = %+v", got)
	}
}

func TestInstructionsTellTheWorkerAboutJudge(t *testing.T) {
	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-1", "r", false)
	instr := buildAgentInstructions(a, WorkerConfig{TaskID: "t1"}, "/scripts")
	if !strings.Contains(instr, "/scripts/judge") || !strings.Contains(instr, "you decide") {
		t.Errorf("instructions do not describe judge:\n%s", instr)
	}
}
