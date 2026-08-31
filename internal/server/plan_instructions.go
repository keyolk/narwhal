// plan_instructions.go builds the system prompt fragment that teaches the
// planner agent how to decompose a request into a task DAG. It is shared
// between the batch CLI (`narwhal plan`) and the daemon's /control/plan
// endpoint, so both paths produce identical DAGs.
package server

import (
	"fmt"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// BuildPlanInstructions returns the planner's system-prompt fragment.
// runID identifies the run, brokerURL is the API base, mainToken is the
// planner's agent token, and prompt is the user's request.
func BuildPlanInstructions(runID, brokerURL, mainToken, prompt string) string {
	return fmt.Sprintf(`You are the COORDINATOR (planner) for Narwhal run %s.

A broker HTTP API is running at %s. Your environment has:
  NARWHAL_BROKER_URL=%s
  NARWHAL_AGENT_TOKEN=%s  (use this as the agent token in API paths)

Your job is to decompose the user's request into a task DAG.

## User Request

%s

## Steps

1. Analyze the request and identify genuinely independent work areas.
   Do NOT create tasks for trivial work. Each task should be a meaningful
   unit that a dedicated worker can complete autonomously.

2. For each task, create it via the broker API using curl:

   curl -s -X POST $NARWHAL_BROKER_URL/api/v1/run/%s/task \
     -H "Content-Type: application/json" \
     -d '{"id":"task-1","name":"auth-audit","assignment":"Analyze auth/ for security issues","deps":[],"model":"haiku","check":"Name one specific finding and the file:line it is at. A finding with no location was not actually found."}'

   - id: unique task id (task-1, task-2, ...)
   - name: short human-readable name
   - assignment: what the worker should do (be specific — include file paths)
   - deps: %s
   - model: (optional) claude model for this task's worker, e.g. "haiku",
     "sonnet", "opus". Omit to use the launcher default. Use a cheaper model
     for narrow investigation tasks and a stronger one for synthesis.
   - check: (optional) %s

     The check that would have caught the worst run in this repo's history
     was "confirm the names you report are actually exported": a run
     answered 8 where the answer was 0, every reported name started with a
     lowercase letter, and it finished 3/3 completed with nothing in the
     record to show it was wrong.

3. Create a synthesis task that DEPENDS on every investigation task
   ("deps": ["task-1","task-2",...]). Narwhal treats these deps as a
   completion gate, not a dispatch gate: the synthesis worker starts
   immediately alongside the investigators and accumulates their findings
   live, but its task-done is refused until every dependency has finished.
   Its assignment must state that it:
     - starts a background watcher on the radio immediately
     - drains the radio repeatedly, accumulating peer findings as they arrive
     - does NOT investigate the codebase itself — peers are doing that, and
       duplicating their work is how a synthesis runs out of turn. The one
       exception is the headline itself, and it belongs in the task's
       "check" rather than here: peers with disjoint scopes fail the same
       way at the same time, and nothing else in the run is positioned to
       notice. The gate asks for the check by name at task-done, which an
       instruction buried in an assignment cannot do
     - calls task-done when it has written what it can; the call blocks
       until every peer has finished, which is what keeps the worker alive
       until then
   Set the synthesis task's "model" to "opus" — it integrates peer findings
   with fidelity, which needs frontier intelligence. Investigation tasks
   should use a cheaper model (haiku) since they are narrow:

   curl -s -X POST $NARWHAL_BROKER_URL/api/v1/run/%s/task \
     -H "Content-Type: application/json" \
     -d '{"id":"synthesis","name":"synthesis","assignment":"Accumulate peer findings from the radio and integrate them into one answer.","deps":["task-1","task-2"],"model":"opus","check":"Confirm two or three of the items your peers reported really meet the definition in the request, and report what that showed — including a result that contradicts the headline."}'

4. After creating ALL tasks, signal completion by sending a message:

   curl -s -X POST $NARWHAL_BROKER_URL/api/v1/agents/$NARWHAL_AGENT_TOKEN/send \
     -H "Content-Type: application/json" \
     -d '{"thread_id":"planning","content":"PLAN_DONE","mentions":[],"priority":"normal"}'

5. Aim for 2-5 tasks. Too many for a small codebase is wasteful.
   Too few wastes the parallelism opportunity.

## Rules

- Do NOT analyze the codebase yourself. You are a PLANNER, not a worker.
- Keep assignments specific: mention file paths, functions, or subsystems.
- If the request is simple enough for one worker, create exactly one task.
- Do NOT create more than 5 tasks.`,
		runID, brokerURL, brokerURL, mainToken, prompt,
		runID, broker.DepsContract, broker.CheckContract, runID)
}

// BuildPlanInstructionsWithHistory is BuildPlanInstructions plus a digest of
// how similar requests were decomposed before.
//
// The digest is appended to the rendered result rather than interpolated
// into the format string: a past prompt is arbitrary user text and can
// carry percent verbs, and #37 was an argument-order bug in exactly this
// Sprintf. With no past runs the output is byte-identical to the plain
// form, so the section can never appear as an empty heading.
func BuildPlanInstructionsWithHistory(runID, brokerURL, mainToken, prompt, cwd string, past []broker.Snapshot) string {
	return BuildPlanInstructions(runID, brokerURL, mainToken, prompt) +
		store.HistoryDigest(past)
}

// PlanHistoryLimit is how many past runs a planner is shown. Two: enough to
// contrast a decomposition that worked with one that did not, few enough
// that the digest stays a hint rather than the bulk of the prompt.
const PlanHistoryLimit = 2

// PlanInstructionsFor is what both plan paths call. It reads the store for
// past decompositions of similar requests in the same working directory and
// builds the fragment around them.
//
// One entry point on purpose. The CLI used to hold its own copy of the
// fragment behind an identical-looking name, the copies drifted, and #37's
// fix reached only the daemon. A lookup done at each call site would be the
// same mistake with different code.
func PlanInstructionsFor(runID, brokerURL, mainToken, prompt, cwd string) string {
	past := store.RecentRunsFor(cwd, prompt, PlanHistoryLimit)
	return BuildPlanInstructionsWithHistory(runID, brokerURL, mainToken, prompt, cwd, past)
}
