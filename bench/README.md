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

Per task the harness extracts the repository once, runs both arms against that
same copy, and judges each. `AGENT_TIMEOUT` (seconds, default 1800) and `CONC`
(Narwhal worker concurrency, default 3) tune the run. The Narwhal arm
defaults to Smart — haiku investigation + opus synthesis; set
`NARWHAL_WORKER_MODEL` and `NARWHAL_SYNTHESIS_MODEL` to override.
Re-running skips any trial that already has a verdict, so an interrupted
slice resumes.

## Pieces

| File | Role |
|---|---|
| `extract_repo.sh` | Copies a task image's `/app` onto the host |
| `make_prompt.py` | Remaps container paths in `instruction.md` for both arms |
| `run_b0.sh` | Single-session baseline arm |
| `run_narwhal.sh` | `narwhal plan` arm, with the synthesis contract appended |
| `judge.py` | Rubric judge (AgentRadio's scoring logic, `claude --print` backend) |
| `summarize.py` | Per-task and total rubric pass rates |
| `run_slice.sh` | Drives the above over a task list |

## Two things that will bite

**The judge must be pinned.** `ccproxy` rotates accounts, so consecutive calls
can be served by different models. `judge.py` calls `claude --print --model
opus` directly for this reason; routing it through ccproxy scores the two arms
against different judges and the comparison stops meaning anything.

**Calibrate before trusting a number.** Judge the task's own
`solution/answer.txt` and a deliberately vague answer first. The reference
should score near 1.0 and the vague one near 0. A judge that passes both is
measuring nothing.
