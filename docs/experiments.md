# Experiments

Narwhal's design rests on one empirical claim: **a directly executed
`ccproxy claude --print` worker receives a background Bash task completion
notification when a peer posts a radio message, without polling.** This is
what makes AgentRadio-style passive awareness possible.

This document records the experiments that established (or refuted) that claim.

## E1 — Passive awareness in Workflow subagents: REFUTED

**Date:** 2026-08-11
**Environment:** Claude native Dynamic Workflow, `agent()` subagents
**Question:** Does a Workflow subagent receive a background Bash completion
notification?

Four runs with a file-based message probe:

| Check | Result |
|---|---|
| Distinct subagents ran in parallel | pass |
| Message crossed between subagents via shared store | pass |
| Background watcher overlapped foreground work | pass |
| Watcher completion delivered as automatic notification | **fail** |
| `TaskOutput` available to retrieve the background result | **fail** — tool not exposed |
| Boundary-based explicit `drain` worked | pass |
| Drained message changed subsequent behavior | pass |

Timestamps from run 1 confirmed real overlap:

```
watch_started      1786448504426296000
foreground_started 1786448513894041000
message_sent       1786448520549903000
foreground_finished 1786448521894087000
```

The receiver had to read the watcher's output file directly. No notification
arrived, and `ToolSearch` for `TaskOutput` returned
`No matching deferred tools found`.

**Conclusion:** Workflow subagents can exchange messages, but only through
explicit cooperative draining at tool boundaries. Automatic passive awareness
is not available there.

## E2 — Passive awareness in directly executed Claude Code: CONFIRMED

**Date:** 2026-08-12
**Environment:** `ccproxy claude --print` launched by `narwhal experiment`
**Question:** Same question, in a directly executed process.

Two real Claude Code workers against a live Narwhal broker:

- **receiver** — starts `scripts/watch` as a background Bash task, creates a
  ready marker, then runs an 8-second foreground compute loop.
- **sender** — waits for the ready marker, sleeps 3 seconds so the receiver is
  mid-work, then posts an URGENT message mentioning the receiver.

### First attempt: environment failure, not a measurement

Every Bash call touching `~/.narwhal/` was blocked by the permission gate.
Headless `--print` has no interactive approval path, so the watcher never
started. The receiver correctly refused to report this as a negative result:

> 아래 요약의 `BACKGROUND_NOTIFICATION: no`는 "백그라운드 알림이 전달되지 않는다"는
> 측정 결과가 아닙니다. watcher 자체가 시작되지 못했으므로 알림이 올 대상이 없었습니다.

Fix: pass `--permission-mode bypassPermissions`, the same flag AgentRadio's
own startup scripts use, for exactly this reason.

### Second attempt: confirmed

| Check | Result |
|---|---|
| Sender posted via wrapper script | pass — `Seq 1` in broker |
| Receiver started watcher as background task | pass |
| Foreground work ran while watcher waited | pass |
| **Automatic completion notification delivered** | **pass** |
| Receiver acted on message content | pass — ack artifact written |
| Task marked complete via wrapper | pass |

Receiver's own report:

```
BACKGROUND_NOTIFICATION: yes
WATCHER_OUTPUT: {"agent_id":"receiver","cursor":1,"messages":[{"Seq":1,
  "Sender":"sender","Mentions":["receiver"],"Priority":"urgent",
  "Content":"peer finding ACTION:WRITE_ACK"}]}
ACK_WRITTEN: yes
```

The receiver explicitly noted it had not polled anything before the
notification arrived: the harness delivered `<task-notification>`
(task-id `b2voyetgs`, status `completed`) right after the foreground command
finished.

Durable evidence:

- `experiment/receiver-ready` — created before the send
- broker message log — `Seq 1`, urgent, mentioning receiver
- `experiment/receiver-ack.json` — `{"event":"ack","reason":"ACTION:WRITE_ACK"}`
- task state — `completed`

**Conclusion:** Passive awareness works in directly executed Claude Code.
This is the mechanism Narwhal builds on, and it is the concrete reason
workers are launched as processes rather than as Workflow subagents.

## E3 — Live peer correction during a real run: CONFIRMED

**Date:** 2026-08-12
**Environment:** `narwhal run --workers 2`, coordinator loop driving the graph

Two workers analyzed a two-file codebase with a deliberately cross-cutting
concern: `auth.py` states "header takes precedence over body", `session.py`
carries an unresolved `# TODO: which wins, header or body?`.

The radio log shows genuine intellectual collaboration, not just status
reporting:

| Seq | From → To | Priority | Content |
|---|---|---|---|
| 1 | task-1 → task-2 | urgent | auth.py's rule answers session.py's TODO; flags the asymmetry risk |
| 2 | task-2 → task-1 | urgent | Frames it as two conflicting rules; warns task-1 not to over-generalize |
| 3 | task-2 → task-1 | normal | "RECONCILE — we crossed": **retracts its own seq=2 framing** as overstated |
| 4 | task-1 → task-2 | normal | ACK; **narrows its own claim** from "codebase rule" to "auth.py's rule" |

Messages 1 and 2 were sent within 400ms of each other — the workers crossed.
Message 3 is task-2 noticing the crossing and correcting itself in task-1's
favor. Message 4 is task-1 accepting the correction and scoping its claim down.

Both also independently converged on the sharpest defect, which neither file
exhibits alone: a request can carry a header-sourced token and an unrelated
body-sourced `session_id`, and nothing binds them to the same principal.

This is the behavior a barrier-synchronized design cannot produce. Under
`parallel()` with a join, each worker would have finished on its own
un-corrected framing, and the reconciliation would have had to happen in a
later synthesis stage — with both original errors already baked into the
returned findings.

### A bug the workers found first

In an earlier run on a different workspace, worker-task-1 radioed:

> GOTCHA for task-2: `./scripts/` does not exist in /private/tmp/narwhal-demo
> — use the absolute path /Users/…/agents/worker-task-2/scripts/

It was right. The generated instructions said `./scripts/`, but a worker's
cwd is the target repository, not its own workspace. `buildAgentInstructions`
now emits absolute paths. The radio layer surfaced a real defect in the
runtime that launched it.

## E6 — Interactive mode via MCP: CONFIRMED

**Date:** 2026-08-12
**Environment:** `narwhal daemon` + `narwhal mcp` registered at user scope

Registered with `claude mcp add --scope user narwhal narwhal mcp`, then
verified the full path with raw JSON-RPC over stdio.

| Check | Result |
|---|---|
| `initialize` returns protocol + serverInfo | pass — `narwhal 2025-06-18` |
| `tools/list` exposes all five tools with schemas | pass |
| Notification (`notifications/initialized`) gets no response | pass |
| Tool call with no daemon auto-starts one | pass |
| Daemon outlives the MCP process | pass — still running after stdin closed |
| `narwhal_spawn` dispatches a real worker | pass |
| Worker completes and calls `task-done` | pass |

Against a live daemon over the control API directly:

- spawned two workers, both dispatched
- drained a worker finding while the run was still active
- **added a third worker to the same run afterwards** — the thing batch
  mode structurally cannot do
- sent an urgent operator message to steer a running worker
- cancelled the run: the worker was killed and state went to `canceled`

The daemon is auto-started detached (`Setsid`) so Claude Code restarting the
MCP server does not orphan in-flight runs. Single-instance safety uses flock
on the pidfile rather than a pid liveness check, so a stale pidfile from
`kill -9` cannot let a second daemon bind a different port and split state.

## E7 — SWE-Atlas QnA benchmark slice: CONFIRMED

**Date:** 2026-08-12
**Environment:** host checkout of `kovidgoyal/kitty` extracted from
`ghcr.io/scaleapi/swe-atlas:swe_atlas_QnA_kovidgoyal_kitty_1.0`; judge pinned
to `claude --print --model opus`; three tasks with fully static rubrics.

A three-task slice of the benchmark AgentRadio publishes against, run with
Narwhal on one arm and a single Claude Code session (B0) on the other. Full
setup and caveats in [`benchmark.md`](./benchmark.md).

| Task | Rubrics | B0 | Narwhal | B0 wall | Narwhal wall | Narwhal DAG |
|---|---:|---:|---:|---:|---:|---|
| …9e5 rewrap | 8 | 8/8 | 8/8 | 28 min | 33 min | 4 tasks, 5 msgs |
| …9e4 ssh kitten | 9 | 8/8 | 9/9 | 36 min | 17 min | 4 tasks, 4 msgs |
| …9fc Python/C boundary | 8 | 8/8 | 8/8 | 58 min | 31 min | 5 tasks, 32 msgs |
| **total** | **25** | **24/24** | **25/25** | **122 min** | **81 min** | |

Both arms score reward=1 on every task; task accuracy is tied 3/3. Narwhal
covers one rubric B0 missed (9e4) and is 34% faster overall, 2× on the two
longer tasks. The slice cannot reproduce the published task-accuracy gap
because the static-rubric tasks it uses are ones B0 already aces — the gap
opens on runtime-heavy tasks this slice excludes.

### Bugs found during the run

Three harness bugs surfaced, two of which would have manufactured a Narwhal
loss had they gone unnoticed:

1. **cwd confound** — `--add-dir` grants access but does not change the
   working directory; the first B0 pilot ran inside the harness repo. Fixed
   with `cd "$REPO"`.
2. **Username path split** — Claude Code's scratchpad encodes the username
   with dots→hyphens, but a worker rebuilding a path under `$HOME` writes
   the dotted form. Both directories exist; the worker reported success and
   the judge found nothing. Narwhal's first answer (36 KB, 8/8 when
   recovered) was scored 0. Fixed by writing to `/tmp/narwhal-bench/…`.
3. **`send` positional-argument collision** — `send worklog "…" urgent`
   put `urgent` in the mentions slot, addressing a nonexistent agent. The
   synthesis worker on 9e4 worked around it by reading the state snapshot
   directly. Fixed in the wrapper with a priority-detection case + 4 tests.

## E8 — Per-task model assignment and synthesis parallelization: CONFIRMED

**Date:** 2026-08-13
**Environment:** same kitty slice as E7, 60-min timeout, 4 arms

E7's slice hit a ceiling: both arms scored 100% on easy (8–9 rubric) tasks.
E8 used harder tasks (11–17 rubrics) where B0 starts missing some, and tested
a fourth arm — Smart — that applies the Cursor economics insight at task
granularity rather than run granularity.

| Arm | Rubric pass | Wall clock | What it does |
|---|---:|---:|---|
| B0 | 89.7% | 58 min | single agent |
| Narwhal (all opus) | 92.3% | 100 min | planner + workers + synthesis, all opus |
| Hybrid (all haiku) | 74.4% | 33 min | same, all haiku |
| **Smart** | **94.9%** | **52 min** | haiku investigate + opus synthesis |

Smart has the highest rubric coverage and the lowest wall-clock of any arm
that beats B0. The rubric-level breakdown on 9f0 (17 rubrics, the hardest
task) shows why: 2 rubrics were passed only by Smart — opus synthesis filled
gaps the haiku investigators left. 2 rubrics were missed by Smart/Hybrid
(haiku cannot do them) but passed by B0/Narwhal (opus investigation needed).
1 rubric was passed only by B0 — a single thorough read beating every
multi-agent configuration, the overlap failure the paper's L1→L2 step
isolates.

### Improvements made during E8

Five issues found during E7/E8 were fixed:

1. **drain mention-filtering** — `drain` now returns all messages, not just
   mentioned ones. Mention filtering belongs on the watch path.
2. **Worker exit without `task-done`** — coordinator checks if the worker
   posted to the radio before failing the dispatch; marks complete if so.
3. **Per-task model** — `Task.Model` field + broker API `model` field let
   the planner assign a model per task, not per run.
4. **Synthesis parallelization** — synthesis task has no deps, starts
   alongside investigation, drains radio as peers post. Depth-1 DAG.
   *(Superseded — see E9. Running early was right; dropping the deps to
   achieve it was not.)*

## Implications for the design

1. Workers must be directly executed processes, not Workflow subagents.
2. `--permission-mode bypassPermissions` is mandatory for headless workers;
   without it the agent workspace is unreachable.
3. `drain` remains useful as a belt-and-braces check at natural boundaries,
   covering messages that land between watcher cycles.
4. The "exactly one watcher" invariant from AgentRadio carries over: the
   worker must restart its watcher as soon as one resolves.
5. Synthesis should run in parallel with investigation, not after it — a
   depth-2 DAG costs roughly 2× wall-clock of a single agent. Running it
   early must not mean dropping its dependencies; see E9.
6. Model assignment is per-task, not per-run: synthesis needs frontier
   intelligence even when investigation does not.

## E9 — "Wait for your peers" as an instruction: REFUTED

**Question.** E8 removed the synthesis task's dependencies so it could run
alongside the investigators, and moved the ordering requirement into its
assignment: *wait until every investigation task has called task-done
before writing the final answer.* Does a worker follow that?

**Method.** Read the radio log of every planner run executed after the
change, comparing the sequence number of the synthesis worker's last
message against its peers' last messages. If synthesis was still listening
when its peers finished, its last message is the later one.

**Result: refuted.** All three runs have it backwards.

| Run | synthesis last | peers last | |
|---|---|---|---|
| plan-1786554611131 | seq 10 | seq 14 | finished first |
| plan-1786553326689 | seq 8 | seq 9 | finished first |
| plan-1786551892357 | seq 8 | seq 9 | finished first |

The first is the clearest. The synthesis worker stopped at seq 10;
`worker-task-1` then posted seq 11, 12, 13 and — at seq 14 — its **final
summary**. Whatever synthesis wrote could not have contained it.

A second failure mode appears in the same log. At seq 6 the synthesis
worker announced it had run its own runtime verification "to fill the gap
left by task-4's one-line report". Rather than waiting for a peer, it
re-did the peer's work: a frontier model spent on duplicate investigation,
which is the opposite of the economics the arrangement exists to capture.

**Why the instruction could not be followed.** A worker has no way to
observe that a peer has finished. `drain` returns messages, not task
states; nothing in the worker's toolkit answers "is task-2 done?". So
"wait until every task has called task-done" is not an instruction a worker
can execute — it can only guess when it has heard enough, and three out of
three times it guessed early.

**Resolution.** Deps are restored on the synthesis task, but they gate
*completion* rather than *dispatch*:

- The coordinator dispatches a pending synthesis task without waiting for
  its deps, so it is alive and listening while its peers work (E8's real
  finding, which stands).
- `task-done` blocks until every dep has finished, announcing the wait on
  the radio so a held worker is not mistaken for a hung one.

A failed dep counts as finished — waiting on a peer that will never post
would strand the run.

**The first fix did not work, for an instructive reason.** `task-done`
initially *refused* — `409` with `pending_deps`, plus an urgent radio
message telling the worker to keep draining and call again. An end-to-end
run shows the worker receiving it and answering correctly:

> `fact-a`가 끝날 때까지 워처를 유지하고 대기합니다. 워처가 메시지를 받으면
> 재시작한 뒤 `task-done`을 다시 호출하겠습니다.

Then its process exited. `claude --print` ends when the model's turn ends,
so a worker that announces it will wait has, by announcing it, finished
speaking and died. The coordinator recorded a dispatch with no `task-done`,
retried, and the circuit breaker failed the task on the third attempt —
taking the peer down with it when the run went terminal.

The generalization: **you cannot make a `--print` worker wait by telling it
to.** Anything a worker must do *later* has to be attached to a call it is
making *now*. Blocking inside `task-done` holds the turn open, which is the
only thing that holds the process open.

**And waiting alone was not enough.** With the blocking gate in place, a
verification run behaved correctly and the synthesis worker still reported
the remaining hole itself:

> **task-done**: 100초 블로킹 후 반환. […] 다만 한 가지 짚을 점: 제가
> task-done에 넘긴 outcome 문자열은 **블로킹 시작 시점의 상태**로 고정돼
> 있습니다. 게이트가 저를 살려둔 덕에 fact A를 받긴 했지만, 그 내용은 이미
> 제출된 outcome에 반영되지 못했습니다.

The ordering was fixed and the content was still stale: the outcome is an
argument, evaluated before the call blocks. So the gate no longer completes
the task when messages arrive during the wait. It answers `202` with those
messages and asks for one more call — which the worker can act on, because
it is alive and mid-tool-call, the only moment it can. A `final` flag on
the second call completes it, so the same messages are not handed back
forever.

Three iterations, each exposed only by running it: instruction → refusal →
blocking → blocking with a fold-in round. The pattern across all of them is
that a guarantee must be attached to something the runtime does, not to
something the worker is asked to remember.

**Method note.** The regression was invisible to the benchmark. E8 measured
rubric coverage, and a synthesis written from 3 of 4 peers can still score
well when the missing peer's findings overlap the others'. Ordering was
never asserted. A benchmark that improves on the metric it measures can
still hide a correctness change underneath it.

## Reproducing

```bash
go build -o narwhal ./cmd/narwhal
./narwhal experiment --cwd /tmp --timeout 6m
```

Artifacts land under `~/.narwhal/sessions/<run-id>/`.
