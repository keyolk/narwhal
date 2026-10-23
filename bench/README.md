# bench

Harness for running a slice of SWE-Atlas QnA — the benchmark AgentRadio
publishes against — with Narwhal on one arm and a single Claude Code session
on the other.

Results and caveats: [`../docs/benchmark.md`](../docs/benchmark.md).

## Prerequisites

- A checkout of [Coral-Protocol/AgentRadio](https://github.com/Coral-Protocol/AgentRadio)
  for `data/qa`. The task fixtures are not vendored here.
- Docker, to extract each task's `/app` from its `ghcr.io/scaleapi/swe-atlas:*`
  image.
- `ccproxy`, `claude`, `narwhal`, and `gtimeout` on `PATH`.

## Run

```bash
QA_DIR=~/src/AgentRadio/data/qa \
  bash bench/run_slice.sh /tmp/bench-results \
  task-6905333b74f22949d97ba9e5 \
  task-6905333b74f22949d97ba9e4
```

Per task the harness extracts the repository once, then runs every arm
`REPEATS` times (default 3), interleaved, and judges each trial. Trials land in
`<results>/<task>/<arm>/r<k>/`. Re-running skips any trial that already has a
verdict, so an interrupted slice resumes.

| Arm | What runs | Knobs |
|---|---|---|
| `b0` | one `claude --print` session | `B0_MODEL` (default `opus`) |
| `fanout` | `FANOUT_N` independent B0 sessions, then one merge session | `FANOUT_N` (3), `FANOUT_MODEL` (`opus`) |
| `narwhal` | `narwhal plan`, Smart split by default | `NARWHAL_WORKER_MODEL` (`haiku`), `NARWHAL_SYNTHESIS_MODEL` (`opus`), `CONC` |
| `narwhal-nogate` | `narwhal` with the gave-up gate off (`jev.gave-up.enabled=false`) | same as `narwhal` |
| `hybrid` | `narwhal plan`, opus planner and haiku workers | — |

`ARMS` picks arms (default `b0 fanout narwhal`); `AGENT_TIMEOUT` (seconds,
default 1800) bounds each trial.

`fanout` is the control that matters most. Narwhal spends more than B0, so
beating B0 does not show that coordination earns anything; beating `fanout` at
similar cost does. Pick `FANOUT_N` so the two arms' `cost$` land close.

## Isolation

A trial measures one arm's approach and nothing else on the machine, which took
four fixes to make true. `run_slice.sh` re-executes itself under
`isolated.sh`:

- `env -i`, so nothing from the session that started the slice reaches an
  agent (`CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING`, `CLAUDE_EFFORT`, plugin dirs).
- `isolate/claude` first on PATH: every Claude session — both arms, narwhal's
  planner and workers, the judge — runs with `--setting-sources ""
  --strict-mcp-config`, so no user hook injects other sessions' activity
  (which once carried the judge's rubric text) and no user CLAUDE.md applies.
- narwhal is built from this checkout into `<results>/.bin`, its version in
  `narwhal-version.txt`; the copy on PATH can be reinstalled by anything.
- narwhal reads `<results>/.bin/config.json`: the operator's settings with
  `plan.history=false`. With history on, a repeat's planner is shown how the
  previous trial's tasks ended.

- `isolate/ccproxy` adds `--accounts $BENCH_ACCOUNTS` to every ccproxy call
  when it is set. When every Claude account is throttled ccproxy routes a
  turn to another backend, and that trial is excluded by the tier check;
  pinned, it waits instead. `pilot.sh` pins the two Claude accounts.

Task repositories live under `~/.cache/narwhal-bench/repos`, outside this
checkout, so an agent cannot walk up into `bench/results`.

The judge calls `claude` directly. When that account is out of quota, route it
through one ccproxy account: `JUDGE_CMD="ccproxy --accounts <name> claude"`.
Every judge call's served model is checked; a judge that could not finish
leaves the trial unjudged, and the next slice run judges it again without
re-running the agent.

## Runtime environment

Every arm's prompt ends with the same note: the repository is a copy of the
task image's `/app`, the image is pulled, and code can be run in it with
`docker run --platform linux/amd64`. The published harness runs agents inside
that container, so runtime rubrics ("reports the log line X") are meant to be
met by running the code. Without the note, whether an arm can meet them comes
down to whether it thinks of docker — on measure-2 the single agent did and
the narwhal worker did not. `BENCH_NO_ENV=1` leaves the note out, which is how
the earlier slices ran.

## Pilot

`pilot.sh` runs eight runtime-heavy tasks across six repositories, arms
`b0 fanout narwhal narwhal-nogate`, three repeats, `FANOUT_N=2` (measure-2's
costs put two independent runs closest to narwhal's spend).
`PREPARE_ONLY=1 bash bench/pilot.sh` pulls the images and extracts the
repositories without starting an agent.

## Reading the summary

`summarize.py` prints, per arm: mean rubric pass rate, reward, the spread
across repeats (`sd`), wall clock, estimated cost, and answer size; then each
arm against `b0` with a paired bootstrap CI over tasks.

- An unjudged trial scores 0. A harness failure is the failing arm's loss.
- A trial whose served model is not the tier it asked for is excluded and
  counted under `excl`. `usage.json` in the trial says which agent drifted.
- Cost is an estimate from transcript tokens and the price table in
  `usage.py`. A trial served by a model with no price there has unknown cost,
  and so does any arm total that includes it.
- If `sd` is as large as the gap between arms, the gap is noise. Run a pilot
  first and size the task count from its spread.

## Pieces

| File | Role |
|---|---|
| `extract_repo.sh` | Copies a task image's `/app` onto the host |
| `make_prompt.py` | Remaps container paths in `instruction.md` for both arms |
| `run_b0.sh` | Single-session baseline arm |
| `run_fanout.sh` | N independent sessions plus a merge, same budget class as Narwhal |
| `run_narwhal.sh` | `narwhal plan` arm, with the synthesis contract appended |
| `judge.py` | Rubric judge (AgentRadio's scoring logic, `claude --print` backend) |
| `meta.py` | Writes `run_meta.json` for every arm: outcome, cost, served-model check |
| `usage.py` | Tallies tokens and cost from Claude transcripts by session id |
| `summarize.py` | Per-arm score, spread, cost; paired bootstrap CI against `b0` |
| `run_slice.sh` | Drives the above over a task list |
| `pilot.sh` | The pilot task list and arm set |

## Two things that will bite

**The judge must be pinned.** `ccproxy` rotates accounts, so consecutive calls
can be served by different models. `judge.py` calls `claude --print --model
opus` directly for this reason; routing it through ccproxy scores the two arms
against different judges and the comparison stops meaning anything.

**Calibrate before trusting a number.** Judge the task's own
`solution/answer.txt` and a deliberately vague answer first. The reference
should score near 1.0 and the vague one near 0. A judge that passes both is
measuring nothing.
