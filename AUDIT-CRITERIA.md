# Self-audit criteria for narwhal

Published taxonomies and harness standards, used as the checklist. All are
empirical work at a scale we cannot reproduce here, which is why they are the
standard rather than our own intuition.

## A. MAST — multi-agent failure taxonomy

*Why Do Multi-Agent LLM Systems Fail?* — 1,600+ annotated traces across 7
frameworks, 3 annotators, Cohen's kappa = 0.88. 14 failure modes in 3
categories. https://arxiv.org/abs/2503.13657

The mode names we have confirmed from the paper:

- FM-1.2 Disobey role specification
- FM-1.3 Step repetition
- FM-1.4 Loss of conversation history
- FM-2.1 Conversation reset
- FM-3.1 Premature termination

Categories: (i) system design issues, (ii) inter-agent misalignment,
(iii) task verification.

## B. Resume Contract — checkpoint/interrupt/resume conformance

*Resume Means Resume* — a machine-checked conformance contract, with LangGraph
1.2.9, LlamaIndex Workflows 2.22.2 and CrewAI 1.15.2 measured against it.
https://arxiv.org/html/2608.03836v3

Six properties:

| | property | meaning |
|---|---|---|
| PC | Prefix Continuation | recovery resumes from the durably recorded frontier |
| EO | Effect Exactly-Once | each effect fires at most once across crashes and resumes |
| FD | Fork Determinism | different resume values produce different branch outcomes |
| CV | Checkpoint Validity | persisted checkpoints satisfy the state schema |
| CO | Consume-Once | an interrupt is consumed by at most one resume |
| RD | Recovery Determinism | recovery decisions are a function of durable state alone |

Two findings that bear on us directly:

1. Every framework measured violates something. LangGraph is exactly-once on
   interrupt paths but **at-least-once across crashes**; CrewAI documents
   exactly-once restoration and re-executes completed effect-bearing methods.
   So "we violate a property" is a finding, not a disgrace — but it must be
   known and deliberate rather than accidental.

2. "The contract does not envision deciding behavior from output files."
   narwhal's `readOutcome` does exactly that. This is off-contract by
   construction and may be *better* — it reads evidence that work completed —
   but it means RD has to be argued rather than assumed.

## C. What the surveys say the field is missing

*The Horizon Gap* — systematic survey of 1,547 arXiv papers, 2024–2026.
https://arxiv.org/html/2608.06663

- "How much of long-horizon capability lives in the model versus the harness
  wrapped around it" is unresolved, and **harness engineering is often the
  binding constraint.**
- "The cross-task-persistent column is the sparsest in the literature" —
  state that survives beyond one session is the least-studied thing.
- "outcome-only signals — a single reward, a single pass/fail check — grow
  uninformative as horizon grows."

*The Long-Horizon Task Mirage?* — 3,100+ trajectories, 4 domains.
https://arxiv.org/html/2604.11978v1 — its 7 failure categories are all
trajectory-internal; it does **not** separate agent-reasoning failures from
harness failures. Defects of the kind fixed in PRs #24–#31 have no home in
that taxonomy.

## D. Harness failure is a category the trajectory taxonomies lack

*Prime Agent: A Self-Improving RLM Harness* — https://arxiv.org/abs/2608.23552

Its framing is the one this file arrived at from the other direction:

> A model should fail an evaluation because the task exceeds its capability,
> not because the harness dropped state, restricted useful actions,
> **miscounted resources**, or terminated prematurely.

That is the gap noted under section C — the Mirage paper's seven categories
are all trajectory-internal, and the defects fixed in #24–#31 have no home in
them. Prime Agent names the category and builds a harness around it.

Two of its standards are ones narwhal can be held to:

1. **Resource accounting aggregates the root and descendant sessions, so
   delegation stays visible in test-time cost.** narwhal had none until #42.
   The README's economics claim — frontier planner, cheap workers, frontier
   synthesis — was an argument from the price list, not an observation.
   Measured across the backlog once accounting existed: 93 tasks in 27 runs
   are recoverable, 5.6M output tokens, and **5 tasks were served by a
   different tier than they asked for**. Counting where they fell found the
   cause — all 5 are interactive runs and the batch runs have none, because
   the two dispatch paths built their worker config by hand and only one
   passed the task's model. Fixed in #44 by giving both one constructor.
   80 dispatched tasks predate the launcher pinning session ids and cannot
   be measured at all, so every total over the backlog is a floor.

   Worth recording as method: the defect was invisible for the life of the
   feature and was not found by reading the two dispatch sites, which look
   nothing alike. It was found by counting an artifact — the same thing
   this file says below about every other defect in this repo.

2. **A task-specified end-condition evaluated after each turn.** narwhal's
   `task-done` gate checked that deps finished, not that the work was
   right. #43 adds one: the planner writes a task's `check` at
   decomposition time, and the gate hands it back at task-done and
   completes on the call that answers it. Both the check and its result
   land in the snapshot, which is what run s1787538246213-1 lacked — it
   reported 8 where the answer was 0 and finished 3/3 completed, and no
   field in the record distinguished it from a run that was right.

   The narwhal version is weaker than Prime Agent's in a way worth
   stating: theirs *evaluates* the end condition, ours *records* it. The
   worker runs its own check and reports its own result, and nothing
   re-executes the work. The broker has neither the worker's working
   directory nor a safe way to run planner-authored shell, and a gate that
   did would be a larger security surface than the thing it verifies. What
   the gate buys is that the claim is fixed before the work starts and the
   answer is in the record afterwards — enough for an audit to count,
   which is what finding #41 by hand cost.

   This is also off-contract in the same way `readOutcome` is (§B): a
   check result is evidence a worker produced about itself, so RD has to
   be argued rather than assumed. It is argued the same way — the gate's
   decision is a function of durable state (`Check` set, `CheckResult`
   empty), and both survive a restore, so a restarted daemon asks exactly
   the tasks that had not answered. Harvest is the deliberate exception:
   there the broker was gone and the worker has exited, so the result is
   recorded if the outcome file carries one and the task completes either
   way. Demanding an answer on that path would strand finished work
   rather than verify anything, and `narwhal show` names such a task as
   `(not answered)` instead of leaving a blank that reads like a pass.

Not adopted, and this one was decided by counting rather than by taste:
**accumulating worker instructions across runs.** The argument for it was
that a worker's repeated mistakes should carry forward instead of every run
starting from the same generated instructions. Measured over 109 worker
transcripts, they do not repeat:

| occurrences | sessions | what |
|---|---|---|
| 59 | 33 | `No such file or directory` — 32 are ordinary misses in the repo under investigation, 21 are `cd` into a path that is not there, and only 6 touch a wrapper script |
| 26 | 16 | task-done could not reach the broker — concentrated in 6 runs, and a dead broker is what harvest already recovers |
| 9 | 5 | the fold-in round, which is the gate working |
| 6 | 4 | task-done timed out waiting on peers, likewise |

And 173 dispatched tasks produced 8 failures, 3 of them in a run the
operator cancelled. There is no recurring instruction-level defect in the
corpus to accumulate. Revisit if one appears; building the mechanism first
would be building it for a problem this machine does not have.

Not adopted, deliberately: `/refine`, where the model edits its own harness
state between runs. The paper supplies the counter-evidence itself — a
Factorio agent discovered an RCON resource-injection exploit and **preserved
it as a skill**, past an anti-cheating heartbeat. narwhal's plan immutability
is a defence of the same kind and is not worth trading away.

Its numbers are not a baseline for ours: a 7-day Factorio run with 633
depth-one subagents and 23.4M output tokens is a different scale of
experiment than a 3-task benchmark slice. Read it for the design, the way
`docs/benchmark.md` already treats AgentRadio's published figures.

## How this audit must be done

The method that worked repeatedly in this codebase is **counting real
artifacts, not reading code and reasoning about it**. Every defect found in
this repo recently came from a count:

- auditing 32 stored runs found the duplicate-run defect
- counting transcript tool calls (drain 92 / plan 0) found the MCP cursor bug
- rendering a real 708-line transcript found the node-scroll bug
- measuring 153ms per scroll found the render-cache need
- hashing prompts proved 5 "duplicate titles" were 5 runs of one prompt

A finding is only real when it names a file and a line, or a count over
artifacts on disk, and states how to reproduce it. "This looks fragile" is
not a finding.

## Evidence available on this machine

- `~/.narwhal/runs/*.json` — 47 persisted runs (snapshots: tasks, states,
  outcomes, radio messages, and — since #42 — per-task token usage and the
  model that actually served each one)
- `~/.narwhal/sessions/<run>/agents/worker-<task>/` — per-worker instructions,
  scripts, claude-output.txt, claude-session-id, outcome-*.json
- `~/.claude/projects/*/<session-id>.jsonl` — full worker transcripts,
  109 of them, 8,982 tool calls
- `~/.local/state/kmd/rag.jsonl` — 7,500+ RAG decisions, including which
  worker prompts retrieved context and which came back empty
- the repo itself at `/Users/gavin.jeong/src/keyolk/narwhal`
