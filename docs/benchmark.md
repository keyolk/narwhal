# Benchmark slice: Narwhal vs single-agent baseline on SWE-Atlas QnA

A short slice of the benchmark AgentRadio publishes against
([arXiv:2607.28430](https://arxiv.org/abs/2607.28430), SWE-Atlas QnA), run
against Narwhal's own implementation rather than AgentRadio's.

## What this is and is not

This is a **directional read**, not a reproduction. The published numbers come
from 124 tasks in the task's own Docker container under Harbor on Modal, with
four agents on a fixed five-phase protocol. This slice is a handful of tasks on
a host checkout with Narwhal's planner-and-DAG protocol. It answers "does the
machinery work end to end and roughly where does it land," not "does Narwhal
beat AgentRadio."

Differences that matter, stated up front:

| | Published harness | This slice |
|---|---|---|
| Tasks | 124 | 3 |
| Environment | task container (`ghcr.io/scaleapi/swe-atlas:*`) | `/app` extracted to the host |
| Multi-agent protocol | 4 peers, five-phase negotiation | planner → DAG → workers → synthesis |
| Judge | `anthropic/claude-opus-4-5` via API | `claude --print --model opus` |
| Task selection | all | 3 kitty tasks with fully static rubrics |

The task selection is the largest caveat. Extracting `/app` to the host loses
the container's runnable build environment, so rubrics of the form "reports the
captured output of X" cannot be satisfied by either arm. Tasks were screened to
those whose rubrics are entirely code-reading claims ("identifies function F in
file G"), which removes that confound but also removes the runtime-heavy tasks
where the published gap is widest.

## Judge calibration

Before either arm ran, the judge was checked against both ends of the scale on
`task-…9e5`:

| Input | Rubrics passed | Reward |
|---|---|---|
| Task's own reference answer (`solution/answer.txt`) | 8/8 | 1 |
| Deliberately vague answer (correct in outline, no specifics) | 0/8 | 0 |

The judge is pinned to `claude --print --model opus` rather than routed through
ccproxy: ccproxy rotates accounts, and a run served partly by `glm-5p2` and
partly by `kimi-k2p7` would score the two arms against different judges.

## Protocol

Both arms receive the same question with the same path remapping (`/app` → the
host checkout, `/logs/agent/answer.txt` → the trial's answer path). Neither is
given a hint the other lacks. Arms are interleaved per task so a mid-run quota
cliff cannot land systematically on one of them.

- **B0** — one `ccproxy claude --print --permission-mode bypassPermissions`,
  no coordination. This is the single-agent baseline the published table calls
  B0 — the thing every multi-agent configuration has to beat.
- **Narwhal** — `narwhal plan`: a planner agent decomposes the question into a
  task DAG, the coordinator dispatches workers in dependency order over a
  shared radio, and a synthesis task writes the answer. All workers run on the
  same model ccproxy routes.
- **Hybrid** — `narwhal plan --planner-model opus --worker-model haiku`: same
  DAG protocol, but the planner runs on a frontier model and every worker on a
  cheap one. This tests the Cursor economics insight — that decomposition and
  design decisions need frontier intelligence, but narrow execution does not.
- **Smart** — `narwhal plan` with per-task model assignment: investigation
  workers run on haiku, synthesis on opus. The `Task.Model` field lets the
  planner pick a model per task rather than per run, so the synthesis task — the
  one that needs frontier intelligence to integrate peer findings with fidelity —
  gets opus while the narrow investigation tasks get haiku.

## Results

Two slices were run. Slice 1 (60-min timeout, 8–9 rubric tasks) confirmed the
machinery works but hit ceiling — both arms scored 100%. Slice 2 (60-min timeout,
11–17 rubric tasks) pushed past the ceiling and tested the economics.

### Slice 1 — easy tasks, ceiling effect

Three tasks from the kitty codebase with 8–9 static rubrics. Both arms clear
the must-have bar on every task.

| Task | Rubrics | B0 | Narwhal | B0 wall | Narwhal wall |
|---|---:|---:|---:|---:|---:|
| …9e5 rewrap | 8 | 8/8 | 8/8 | 28 min | 33 min |
| …9e4 ssh kitten | 9 | 8/8 | **9/9** | 36 min | **17 min** |
| …9fc Python/C boundary | 8 | 8/8 | 8/8 | 58 min | **31 min** |
| **total** | **25** | **24/24** | **25/25** | **122 min** | **81 min** |

Task accuracy tied 3/3. Narwhal picked up one rubric B0 missed (9e4) and was
34% faster overall.

### Slice 2 — harder tasks, 4 arms

Three tasks with 11–17 rubrics, where B0 starts missing some. Four arms:
B0, Narwhal (all opus), Hybrid (all haiku workers), Smart (haiku investigate +
opus synthesis).

| Task | Rubrics | B0 | Narwhal | Hybrid | Smart | B0 wall | Narwhal wall | Hybrid wall | Smart wall |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| …9f0 startup | 17 | 14/17 | 14/17 | 7/17 | **15/17** | 19 min | 43 min | 11 min | **18 min** |
| …9fd file transfer | 11 | 11/11 | 11/11 | 11/11 | **11/11** | 20 min | 28 min | 12 min | **18 min** |
| …9aa01 choose-fonts | 11 | 10/11 | 11/11 | 11/11 | **11/11** | 20 min | 30 min | 9 min | **16 min** |
| **total** | **39** | **35/39** | **36/39** | **29/39** | **37/39** | **58 min** | **100 min** | **33 min** | **52 min** |

| Metric | B0 | Narwhal | Hybrid | Smart |
|---|---:|---:|---:|---:|
| Rubric pass rate | 89.7% | 92.3% | 74.4% | **94.9%** |
| Task accuracy (reward=1) | 1/3 | 2/3 | 2/3 | **2/3** |
| Wall clock | 58 min | 100 min | 33 min | **52 min** |

**Smart wins on both axes that matter.** It has the highest rubric coverage
(37/39, 94.9%) — above Narwhal's 92.3% — and it is faster than B0 (52 min
vs 58 min) and much faster than Narwhal (52 min vs 100 min).

The key finding is on 9f0, the 17-rubric task where B0 misses 3. Hybrid
(haiku-only) collapsed to 7/17 — haiku workers could not extract enough
detail. Narwhal (all opus) matched B0 at 14/17 but took 43 min. Smart split
the difference: haiku workers investigated in parallel (fast, broad), and the
opus synthesis task filled the gaps they left (slow, precise). 15/17 in 18 min.

### Rubric-level breakdown on 9f0

The 17 rubrics on 9f0 break into a pattern that shows what each arm's
weakness is:

| Pattern | Rubrics | Who passes |
|---|---:|---|
| All pass | 7 | every arm (easy rubrics) |
| All fail | 1 (`f94d252f`) | every arm (needs runtime) |
| **B0 only** | 1 (`63e12598`) | B0 passes, all multi-agent miss |
| **Narwhal/Hybrid fail, Smart passes** | 2 (`ebb9479e`, `410307bd`) | Smart only |
| **Smart/Hybrid fail, B0/Narwhal pass** | 2 (`56f34159`, `f5a78774`) | B0, Narwhal |
| Hybrid-only fail | 4 | everyone but Hybrid |

The two rubrics only Smart passes are the ones where opus synthesis filled
gaps the haiku investigators left. The two rubrics Smart misses (along with
Hybrid) are ones where opus investigation was needed — Smart has no opus
investigators, only an opus synthesizer. The one rubric only B0 passes is a
case where a single thorough read beat every multi-agent configuration, the
overlap failure mode the published paper's L1→L2 step is about.

### What the published numbers say

AgentRadio's table (Opus 4.6, 124 tasks, 1306 rubrics):

| | B0 | L3 (AgentRadio) |
|---|:---:|:---:|
| Task accuracy | 32.3% | 62.1% |
| Rubric pass rate | 84.2% | 93.1% |

Our slice has B0 at 89.7% rubric pass — close to the published 84.2%. Smart
at 94.9% is close to L3's 93.1%. The task-accuracy gap (1/3 → 2/3) is in the
same direction as the published gap (32.3% → 62.1%) but with 3 tasks it is
statistically meaningless.

The runtime-heavy tasks where the published gap is widest are excluded here —
they need the Docker container, not a host checkout. That is a different
harness.

## Reading the numbers

**What the slice shows.** Narwhal's planner-and-DAG pipeline works end to end
on real benchmark tasks: it decomposes a question, dispatches workers in
parallel, they coordinate over the radio, a synthesis task writes the answer,
and the answer passes the same rubric judge the published harness uses. On the
harder tasks (slice 2), Smart — haiku investigate + opus synthesis — achieves
the highest rubric coverage of any arm (94.9%) at wall-clock below B0 (52 min
vs 58 min). The Cursor economics insight holds for code-understanding tasks
too, with the refinement that synthesis needs frontier intelligence even when
investigation does not.

**What the slice does not show.** It cannot show the 30-point task-accuracy
gap the paper reports, because the task selection removes the runtime-heavy
tasks where that gap opens. With three static-rubric tasks, B0 is close to
ceiling — the rubric judge cannot distinguish a 94% answer from a 100% one
when the rubric is binary "names function F." A slice that could show the
published gap would need the runtime tasks, which means running inside the
Docker containers rather than on a host checkout. That is a different harness.

**Three harness bugs were found and fixed during the run, all of which would
have manufactured a Narwhal loss had they gone unnoticed:**

1. **Username path split.** Claude Code's scratchpad encodes the username
   with dots replaced by hyphens (`-Users-gavin-jeong`), but a worker
   rebuilding a path under its own `$HOME` writes the dotted form
   (`-Users-gavin.jeong`). Both directories exist; the worker reported
   success and the judge found nothing. Narwhal's first answer (36 KB, 399
   lines, 8/8 when recovered) was scored 0 until the cause was traced. Both
   arms now write to `/tmp/narwhal-bench/…` (no username in the path).

2. **`send` positional-argument collision.** `send worklog "…" urgent` put
   `urgent` in the mentions slot, addressing a nonexistent agent and hiding
   the message from peers. The synthesis worker on 9e4 noticed it could not
   drain a peer's message and worked around it by reading the state
   snapshot directly — which is why 9e4 still scored 9/9. The wrapper now
   treats a bare priority in the mentions slot as the priority.

3. **`drain` mention-filtering.** `drain` returned only messages that
   mentioned the calling agent, not all messages. A synthesis task that needs
   every peer finding would silently miss anything not @-mentioning it — the
   same failure as bug 2, from the other side. `drain` now returns all
   messages; mention filtering belongs on the watch (notification) path only.

**Two coordinator improvements were made to handle workers that did their
job but forgot the protocol call:**

4. **Worker exit without `task-done`.** A worker that posted findings to the
   radio but exited without calling `task-done` was recorded as a failed
   dispatch and retried, wasting a full worker run. The coordinator now
   checks whether the worker posted to the radio before failing the dispatch,
   and marks it complete if so.

5. **Synthesis serialized after investigation.** A synthesis task with deps
   on every investigation task could not start until all of them finished,
   making Narwhal's wall-clock roughly 2× B0's. The plan requirement now
   asks for a synthesis task with no deps, so it starts in parallel and
   drains the radio as peers post — depth-1 DAG, not depth-2.
