# Narwhal

Graph-engineered multi-agent runtime with passive awareness.

Narwhal combines **Orca-style graph engineering** (Run/Task/Dispatch DAG)
with **AgentRadio-style passive awareness** (background watcher + radio
channel) to coordinate multiple Claude Code workers on a single task.

Each worker runs as a directly-executed `ccproxy claude --print` process,
which preserves Claude Code's full tool surface — including background Bash
task completion notifications, the mechanism Workflow subagents currently
do not surface. ccproxy handles account routing and quota; Narwhal owns the
collaboration and planning layer.

## Status

Functional. 23 unit tests (race-clean), 5 end-to-end experiments with real
Claude Code workers.

## Quick Start

### Interactive (recommended)

Register Narwhal as an MCP server, then drive it from a normal Claude Code
session. Your session stays in the conversation; workers run headless
underneath and report back.

```bash
go build -o ~/.local/bin/narwhal ./cmd/narwhal
claude mcp add --scope user narwhal narwhal mcp
```

Six tools become available:

| Tool | Purpose |
|---|---|
| `narwhal_plan` | Decompose a request into a task DAG via a planner agent |
| `narwhal_spawn` | Launch workers on independent sub-tasks |
| `narwhal_drain` | Read radio messages since a cursor |
| `narwhal_status` | Task states and active workers |
| `narwhal_send` | Steer workers with an operator message |
| `narwhal_cancel` | Kill a run's workers |

`narwhal_plan` is the MCP path for what `narwhal plan` does in batch mode.
A planner agent decomposes the request into a DAG, the daemon dispatches
workers in parallel, and you observe progress via `narwhal_status` and
`narwhal_drain`. Use it instead of `narwhal_spawn` when the request has
genuinely independent parts worth decomposing — for work you can finish
yourself in a few steps, do it directly. It accepts `planner_model`,
`worker_model`, and `synthesis_model` so you can steer the Cursor
economics split (frontier planner, cheap workers, frontier synthesis)
without leaving the conversation.

The broker daemon starts on demand and outlives MCP server restarts, so a
run survives Claude Code reconnecting. You can add workers to a run that is
already in flight — the thing batch mode cannot do.

### Batch

```bash
# Flat parallel workers
narwhal run --workers 2 --cwd ~/src/myrepo --prompt "audit this codebase"

# A planner agent decomposes the request into a DAG,
# then the coordinator dispatches workers in dependency order
narwhal plan --prompt "audit this codebase" --cwd ~/src/myrepo --concurrency 3

# Frontier planner, cheap workers, frontier synthesis — the Cursor
# economics insight. Investigation is narrow and benefits from speed;
# synthesis integrates peer findings and needs frontier intelligence.
narwhal plan --prompt "audit this codebase" --cwd ~/src/myrepo \
  --planner-model opus --worker-model haiku --synthesis-model opus

# Watch a run as it happens (from another terminal)
narwhal monitor            # attach to the newest live run
narwhal monitor --run <id> # attach to a specific run

# Inspect past runs
narwhal show               # list recent runs
narwhal show <run-id>      # full snapshot
```

The monitor is an interactive TUI: a task list on the left, radio traffic on
the right, and a detail pane for reading a full message. Radio messages are
long — that is the point of them — so the list truncates and the detail view
is where you actually read one.

```
Narwhal  s1786471179534-1  ▶ active
account routing 경로의 일관성 검증

Graph                                    Radio (2)
        ┌──────────┐  ┌────────┐           1 · auth →api cross-path asymmetries...
        │ ▶ api ×2 │  │ ✓ auth │           2 ! api URGENT: provider lock releases...
        └─────┬────┘  └────┬───┘
              └────┬───────┘
                   │
            ┌──────┴──────┐
            │ · synthesis │
            └─────────────┘

done 1  running 1  ready 0  failed 0  [following]
tab pane · j/k move · enter detail · b boxes · f follow · q quit
```

Tasks are drawn as boxes wired together, in the style of Graph::Easy and
`dgraph`. Boxes are sized to their label and centered, so there are margins
for edges to route through; siblings share a row while they fit and wrap
when they do not. A fan-out or fan-in is drawn as one shape — a single
horizontal bar — rather than as independent edges that would overwrite each
other, and a run that would otherwise cut through a wrapped row detours
through the margin.

`b` switches to a compact lane view when the graph outgrows the pane:

```
●     ✓ anthropic-path
│ ●   ▶ mixed-path ×2
│ │ ● ▶ session-identity
◉─╰─╰ · synthesis
```

Here every task is a dot in its own lane and every edge is drawn in the
gutter, git-log style. `◉` continues the lane above it, `●` opens a new one,
`╰` ends a lane and `├` branches one that still has work below.

### Several runs at once

An interactive session starts a run per request, so more than one is usually
live. Opening the monitor with several running shows a picker:

```
Narwhal  3 live runs

▸ s1786471179534-1       daemon
    account routing 경로의 일관성 검증
  s1786472797321-2       daemon
    auth 모듈 보안 감사
  s1786473001-3          daemon
    monitor TUI 리팩터링 계획
```

Runs are named by timestamp, so the prompt is what actually distinguishes
them. Inside a run the header shows its position (`[2/3]`), `[` and `]` move
between runs, and `r` returns to the picker. The list refreshes while the
monitor is open — a run started afterwards appears, a finished one drops
out — and the selection follows the run being watched rather than its index.
`narwhal monitor --run <id>` opens one directly.

Graph glyphs are plain box-drawing, so they render in any font. Task and
priority icons use Nerd Font when the terminal has one:

```bash
NARWHAL_ICONS=nerd     # force Nerd Font icons
NARWHAL_ICONS=unicode  # force the portable set
```

Detection is conservative — an unrecognized terminal gets portable glyphs,
since guessing wrong toward Nerd Font fills the pane with tofu boxes.

| Key | Action |
|---|---|
| `tab` | Switch between the graph and radio panes |
| `j` / `k` | Move the cursor (also `↓` / `↑`) |
| `ctrl+d` / `ctrl+u` | Page down / up |
| `g` / `G` | Jump to first / last |
| `enter` | Open the selected task or message |
| `b` | Toggle between box and lane graph views |
| `[` / `]` | Previous / next live run |
| `r` | Open the run picker |
| `n` / `p` | Next / previous item inside the detail view |
| `f` | Re-arm tail following after manual navigation |
| `esc` | Close the detail view |
| `q` | Quit |

The radio list follows the newest message until you navigate manually —
reading an older message is not yanked away by the next poll. Press `f` to
start following again.

## Design

```
narwhal
├── graph layer (Orca style)
│   ├── Run         namespace + coordinator inbox
│   ├── Task        deps edges, state machine
│   └── Dispatch    attempt unit, circuit breaker (3 failures)
│
├── radio layer (AgentRadio style)
│   ├── channel     message bus scoped to a Run
│   ├── thread      planning / worklog / results
│   ├── message     content + mentions + priority (fyi/normal/urgent)
│   ├── cursor      monotonic sequence (no message lost on watcher restart)
│   └── watch       background long-poll + completion notification
│
├── planner
│   └── ccproxy claude --print decomposes request → task DAG via API
│
├── coordinator
│   └── deps-driven parallel dispatch, split-request intake, terminal state
│
├── launcher
│   ├── each worker: ccproxy claude --print --permission-mode bypassPermissions
│   ├── per-agent identity token (endpoint = identity)
│   └── wrapper scripts (send/drain/watch/state/task-done/split)
│
├── store
│   ├── atomic JSON snapshots under ~/.narwhal/runs/<id>.json
│   └── live registry (~/.narwhal/live.json) for monitor discovery
│
├── daemon
│   ├── long-lived broker for interactive use (flock single-instance)
│   └── session: per-run launchers, so one session can hold several runs
│
├── mcp
│   └── stdio JSON-RPC server; thin client of the daemon's /control API
│
└── monitor
    └── Bubble Tea TUI: task list, radio traffic, message detail pane
```

### What graph and radio each own

**Graph owns:** what tasks exist, what depends on what, when a task is
ready, when to dispatch a worker, whether a task succeeded.

**Radio owns:** what workers tell each other while running, urgent
assumption corrections, dead-end sharing, split requests for new tasks.

Graph operates at task granularity (minutes to hours). Radio operates at
discovery granularity (seconds to minutes).

### Plan immutability

Existing tasks are immutable. New tasks can be added to the same Run
(split-request). This is a deliberate compromise between Orca's strict
immutability and AgentRadio's free-form plan negotiation.

### Why directly-executed workers, not Workflow subagents

Experiments (see `docs/experiments.md`) showed that Workflow subagents do
not receive background Bash task completion notifications, making AgentRadio-
style passive awareness impossible there. Directly-executed `ccproxy claude
--print` processes do receive these notifications, which is why Narwhal
launches workers as processes rather than as Workflow subagents.

### Model steering

Narwhal decides what *tier* of intelligence a task needs; ccproxy decides what
*backend model* serves that tier. The two layers are separate on purpose:

```
narwhal: "this task is narrow"  → --worker-model haiku
  └→ ccproxy: haiku tier → cheapest available backend (fable/glm/haiku)
  └→ token limit hit → auto-failover to next account

narwhal: "this task is synthesis" → --synthesis-model opus
  └→ ccproxy: opus tier → strongest available backend
  └→ token limit hit → auto-failover to next account
```

Three working modes, chosen with `--worker-model` and `--synthesis-model`:

| Mode | `--worker-model` | `--synthesis-model` | When |
|---|---|---|---|
| Uniform | (omit) | (omit) | 1–2 workers, narrow task, token-limit evasion via ccproxy rotation |
| Smart | haiku | opus | code-understanding, broad investigation, division of labor |
| Heavy | sonnet | opus | security audit, debugging, subtle dependency tracing |

The coordinator applies `--synthesis-model` to any task whose name or
assignment contains "synthesis" — the one that integrates peer findings with
fidelity. A per-task `model` field on the broker API lets the planner
override the model for an individual task, which wins over the run-level
default. The priority is: per-task `model` > `--synthesis-model` (synthesis
only) > `--worker-model` (everything else) > ccproxy rotation.

## CLI

| Command | Description |
|---|---|
| `narwhal run` | Flat parallel workers on a prompt |
| `narwhal plan` | Planner agent decomposes request into DAG, then coordinator executes |
| `narwhal monitor` | Live view of a running run (DAG + radio traffic) |
| `narwhal daemon` | `start` / `stop` / `status` for the interactive broker |
| `narwhal mcp` | MCP server over stdio (registered with Claude Code) |
| `narwhal experiment` | Two-worker passive-awareness validation scenario |
| `narwhal show` | List recent runs (from disk) |
| `narwhal show <id>` | Full run snapshot (from disk) |
| `narwhal version` | Print version |

## Coverage

### Orca-style graph engineering

- ✓ Run/Task/Dispatch state machine
- ✓ DAG dependency-driven readiness
- ✓ Parallel dispatch with concurrency cap
- ✓ Circuit breaker (3 failures → task failed)
- ✓ Diamond dependency (A→B, A→C, B+C→D)
- ✓ Partial failure → dependents unreached
- ✓ Dynamic task addition (split-request, immutable existing tasks)
- ✓ Run terminal state (done/failed)
- ✓ Run persistence + inspection
- ✓ Coordinating agent (planner decomposes request into DAG)
- ✓ Live monitor (DAG progress + radio traffic during a run)
- ✓ Interactive mode (daemon + MCP; add workers to an in-flight run)

### AgentRadio-style passive awareness

- ✓ Thread/message/mention with priority (fyi/normal/urgent)
- ✓ Monotonic cursor message log
- ✓ Long-poll watch with wake channels
- ✓ Non-blocking drain
- ✓ Agent identity token = endpoint path
- ✓ Background notification (verified in directly-executed Claude Code)
- ✓ Live peer correction (verified: workers cross-correct mid-flight)
- ✓ Multi-thread radio (planning/worklog/results)
- ✓ Watcher restart preserves messages (cursor-based recovery)
- ✓ Mention-based wake (broadcast wakes all, mention wakes specific)
- ✓ Worker = `ccproxy claude --print` with `--permission-mode bypassPermissions`

## Experiments

See `docs/experiments.md` for the full record:

- **E1**: Passive awareness in Workflow subagents — **refuted** (notifications not delivered)
- **E2**: Passive awareness in directly-executed Claude Code — **confirmed**
- **E3**: Live peer correction during a real run — **confirmed** (workers cross-corrected)
- **E4**: Split-request — **confirmed** (worker created 3 new tasks mid-run)
- **E5**: Multi-thread radio + coordinating agent — **confirmed** (planner built 5-task DAG, 10 radio messages, synthesis task integrated findings)

### SWE-Atlas QnA benchmark slice

Two slices of the benchmark AgentRadio publishes against, with Narwhal on
one arm and a single Claude Code session (B0) on the other. Slice 1 (easy
tasks) hits ceiling — both arms 100%. Slice 2 (harder tasks, 4 arms) shows
Smart — haiku investigate + opus synthesis — at 94.9% rubric coverage,
above Narwhal's 92.3% and B0's 89.7%, at wall-clock below B0 (52 min vs
58 min). See [`docs/benchmark.md`](docs/benchmark.md) and
[`docs/experiments.md`](docs/experiments.md) §E7–E8.

## License

MIT
