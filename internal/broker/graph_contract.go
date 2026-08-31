// graph_contract.go states, once, what a dep edge and a synthesis task mean
// to anyone who builds a graph — the planner agent, the MCP tool schema, and
// the operator reading a spawn reply.
//
// It exists because the knowledge lived in exactly one of the three places.
// server/plan_instructions.go told the planner that deps gate completion and
// not dispatch, and that a run wants a synthesis task; the MCP tool schema —
// the path nearly every run actually takes — said only "Task ids that must
// complete first", which reads as serialization. The result is on disk: every
// run built through narwhal_plan carries deps and a synthesis task, and the
// six most recent runs, all built through narwhal_spawn, carry neither.
//
// So the strings live next to DispatchableTasks and PendingDeps, which are
// the code they describe. A caller that renders one of these cannot be
// telling a different story than the dispatcher enforces.
package broker

// DepsContract explains what attaching a dep does. It is deliberately short
// enough to sit in a JSON schema description.
//
// The asymmetry is the part callers get wrong, and it is real rather than
// incidental: DispatchableTasks launches a pending task ahead of its deps
// only when the task is the synthesis step, because that task's job is to be
// listening while its peers work. Every other task waits. Stating only the
// completion-gate half would be the same mistake in the other direction.
const DepsContract = "Task ids this task depends on. For the synthesis task " +
	"these gate COMPLETION, not dispatch: it launches immediately alongside " +
	"its deps and accumulates their findings from the radio while they work, " +
	"and what the deps block is task-done, which is refused until every dep " +
	"finishes. So deps on a synthesis task cost no parallelism. For every " +
	"other task deps do gate dispatch — it stays pending until they complete, " +
	"which is what you want for work that genuinely needs an earlier result " +
	"and not what you want merely to have someone read the output."

// SynthesisContract explains why a multi-worker run wants a task that depends
// on all the others, and how to make one the dispatcher will recognize.
//
// Recognition is by name (see isSynthesisName), which is a real constraint on
// the caller and therefore stated rather than assumed.
const SynthesisContract = "A run with more than one worker usually wants a " +
	"synthesis task: name it \"synthesis\", give it deps on every other task, " +
	"and assign it to integrate peer findings from the radio rather than to " +
	"investigate anything itself. It is recognized by that name, so a task " +
	"that does the job under another name will not be launched ahead of its " +
	"deps. Without one, each worker returns its own fragment and nothing in " +
	"the run reconciles them."

// CheckContract explains the end condition a task can carry.
//
// Written for a caller that has not read broker/check.go: the value of a
// check is that it can come out wrong, and a check chosen after the answer
// exists is chosen to agree with it.
const CheckContract = "An end condition: something cheap to test that would " +
	"come out WRONG if this task got the wrong answer. task-done hands it " +
	"back and the task completes on the call that answers it. Write it from " +
	"the definition of the work, not from the answer you expect — e.g. " +
	"\"confirm the names you report are actually exported\", \"the count you " +
	"report should equal the number of names you list\". Omit it for a task " +
	"with no meaningful end condition; demanding one everywhere produces " +
	"filler."
