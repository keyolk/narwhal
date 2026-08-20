# Self-audit criteria for narwhal

Two published taxonomies, used as the checklist. Both are empirical work at a
scale we cannot reproduce here, which is why they are the standard rather than
our own intuition.

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

- `~/.narwhal/runs/*.json` — 41 persisted runs (snapshots: tasks, states,
  outcomes, radio messages)
- `~/.narwhal/sessions/<run>/agents/worker-<task>/` — per-worker instructions,
  scripts, claude-output.txt, claude-session-id, outcome-*.json
- `~/.claude/projects/*/<session-id>.jsonl` — full worker transcripts,
  83 of them, ~7,800 tool calls
- `~/.local/state/kmd/rag.jsonl` — 7,500+ RAG decisions, including which
  worker prompts retrieved context and which came back empty
- the repo itself at `/Users/gavin.jeong/src/keyolk/narwhal`
