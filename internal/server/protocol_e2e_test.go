// protocol_e2e_test.go runs the wrapper scripts a worker is actually given
// and checks the effect they have on the graph.
//
// Every other test builds its protocol messages with the Format* helpers —
// Go calling Go. The worker does not: it runs bash, which builds the same
// strings by hand. Nothing checked that those two agree, so a rename on
// either side would leave the feature dead and every test green.
//
// That is not a hypothetical drift. An audit of the runs on disk counted
// protocol usage with the literal "MODEL_ESCALATION", which is not the
// prefix — the constant is MODEL_ESCALATE — so it could never have matched
// a real escalation. The count happened to be right for another reason.
// Three of these features have never been used in a real run, which is
// exactly the condition under which such a break goes unnoticed.
//
// So these tests assert the graph *changed*, not that a message parsed.
// Script → curl → server → radio is only half the path; the other half is
// intake applying it, and an intake-side filter (wrong thread, wrong
// priority) would pass a parse-only check while the feature stayed dead.
package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// protocolFixture wires real scripts to a real server over a real socket,
// and returns the run they act on plus the scripts directory.
func protocolFixture(t *testing.T) (*broker.Run, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("r-proto", "audit", t.TempDir(), "main")
	run.CreateStandardThreads()
	run.AddTask("task-1", "first", "do the first thing", nil)
	run.AddTask("task-2", "second", "do the second thing", nil)

	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	a := reg.Register("worker-task-1", "r-proto", false)
	l := launcher.New(addr, "r-proto", t.TempDir())

	dir, err := l.SetupAgent(a, launcher.WorkerConfig{
		AgentID: a.ID, TaskID: "task-1", Assignment: "investigate",
	})
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}
	return run, filepath.Join(dir, "scripts")
}

// runScript executes one of the worker's wrapper scripts and applies
// whatever it posted, the way the dispatch tick would.
func runScript(t *testing.T, run *broker.Run, scripts, name string, args ...string) {
	t.Helper()
	out, err := exec.Command("bash", append([]string{filepath.Join(scripts, name)}, args...)...).
		CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	drainIntake(run)
}

// drainIntake applies every pending request, as one dispatch tick does.
func drainIntake(run *broker.Run) {
	run.IntakeSplitRequests(0)
	run.IntakeGraphRequests(0)
}

func TestEveryWrapperScriptIsValidBash(t *testing.T) {
	// A syntax error in a script only shows up when a worker reaches for
	// that feature mid-run, which for three of them has never happened.
	_, scripts := protocolFixture(t)
	for _, name := range []string{
		"send", "drain", "watch", "state", "task-done",
		"split", "dep-add", "dep-remove", "file-claim", "file-release", "escalate",
	} {
		if out, err := exec.Command("bash", "-n", filepath.Join(scripts, name)).
			CombinedOutput(); err != nil {
			t.Errorf("bash -n rejected %s: %v\n%s", name, err, out)
		}
	}
}

func TestDepAddScriptChangesTheGraph(t *testing.T) {
	// DEP_ADD has never been sent by a real worker. If bash and the parser
	// had drifted apart, nothing would have noticed.
	run, scripts := protocolFixture(t)
	if got := run.PendingDeps("task-2"); len(got) != 0 {
		t.Fatalf("setup: task-2 already has deps %v", got)
	}

	runScript(t, run, scripts, "dep-add", "task-2", "task-1")

	pending := run.PendingDeps("task-2")
	if len(pending) != 1 || pending[0] != "task-1" {
		t.Fatalf("dep-add left PendingDeps(task-2) = %v, want [task-1]", pending)
	}
}

func TestDepRemoveScriptChangesTheGraph(t *testing.T) {
	run, scripts := protocolFixture(t)
	runScript(t, run, scripts, "dep-add", "task-2", "task-1")
	if len(run.PendingDeps("task-2")) != 1 {
		t.Fatal("setup: dep-add did not take")
	}

	runScript(t, run, scripts, "dep-remove", "task-2", "task-1")

	if got := run.PendingDeps("task-2"); len(got) != 0 {
		t.Fatalf("dep-remove left PendingDeps(task-2) = %v, want none", got)
	}
}

func TestFileClaimScriptTakesOwnership(t *testing.T) {
	run, scripts := protocolFixture(t)

	runScript(t, run, scripts, "file-claim", "task-1", "src/shared.go")

	// The proof is that someone else is now refused.
	conflicts := run.ClaimFiles("task-2", []string{"src/shared.go"})
	if len(conflicts) == 0 {
		t.Fatal("file-claim did not take ownership: a peer claimed the same path")
	}
	if conflicts["src/shared.go"] != "task-1" {
		t.Errorf("the path is held by %q, want task-1", conflicts["src/shared.go"])
	}
}

func TestFileReleaseScriptGivesItBack(t *testing.T) {
	run, scripts := protocolFixture(t)
	runScript(t, run, scripts, "file-claim", "task-1", "src/shared.go")

	runScript(t, run, scripts, "file-release", "task-1", "src/shared.go")

	if conflicts := run.ClaimFiles("task-2", []string{"src/shared.go"}); len(conflicts) > 0 {
		t.Fatalf("file-release did not give the path back: %v", conflicts)
	}
}

func TestEscalateScriptMovesTheModelUp(t *testing.T) {
	// MODEL_ESCALATE has never been sent by a real worker either, and it
	// is the one whose prefix an audit got wrong — the drift this file
	// exists to catch, made by a reader rather than by the code.
	run, scripts := protocolFixture(t)
	task := run.GetTask("task-1")
	task.SetModel("haiku")

	// An empty model means "one tier up".
	runScript(t, run, scripts, "escalate", "task-1", "", "the area is deeper than the tier")

	if got := task.CurrentModel(); got != "sonnet" {
		t.Fatalf("escalate left task-1 on %q, want sonnet", got)
	}
}

func TestEscalateScriptHonoursAnExplicitTier(t *testing.T) {
	run, scripts := protocolFixture(t)
	run.GetTask("task-1").SetModel("haiku")

	runScript(t, run, scripts, "escalate", "task-1", "opus", "this needs the strongest tier")

	if got := run.GetTask("task-1").CurrentModel(); got != "opus" {
		t.Fatalf("escalate to an explicit tier left task-1 on %q", got)
	}
}

func TestSplitScriptCreatesATask(t *testing.T) {
	run, scripts := protocolFixture(t)

	runScript(t, run, scripts, "split", "task-3", "extra", "look at the other half", "task-1")

	created := run.GetTask("task-3")
	if created == nil {
		t.Fatal("split did not create task-3")
	}
	if created.Assignment != "look at the other half" {
		t.Errorf("the new task's assignment is %q", created.Assignment)
	}
	if deps := run.PendingDeps("task-3"); len(deps) != 1 || deps[0] != "task-1" {
		t.Errorf("the new task's deps are %v, want [task-1]", deps)
	}
}

func TestSendAndStateScriptsWork(t *testing.T) {
	// The two a worker uses constantly. Cheap to cover here, and they
	// share the quoting machinery with the rest.
	run, scripts := protocolFixture(t)

	runScript(t, run, scripts, "send", "worklog", "found something")

	msgs := run.MessagesSince(0)
	if len(msgs) == 0 {
		t.Fatal("send posted nothing")
	}
	last := msgs[len(msgs)-1]
	if last.Content != "found something" {
		t.Errorf("the posted message is %q", last.Content)
	}

	out, err := exec.Command("bash", filepath.Join(scripts, "state")).CombinedOutput()
	if err != nil {
		t.Fatalf("state failed: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Error("state returned nothing")
	}
}

func TestTheInstructionsDescribeReleaseAccurately(t *testing.T) {
	// The instructions said claims are released "when you exit". That was
	// true when only reap released them, and it is why a worker could
	// reasonably skip file-release: exiting looked like enough. Completion
	// releases them now, and a worker told the old story would still be
	// told the wrong thing about when a peer gets unblocked.
	_, scripts := protocolFixture(t)
	instructions, err := os.ReadFile(filepath.Join(filepath.Dir(scripts), "instructions.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(instructions)

	if strings.Contains(text, "released when you\n    exit") ||
		strings.Contains(text, "released when you exit") {
		t.Error("the instructions still say claims are released on exit")
	}
	if !strings.Contains(text, "when your\n    task completes") {
		t.Errorf("the instructions do not say when claims are actually released")
	}
}
