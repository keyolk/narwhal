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

```bash
# Simple: flat parallel workers
narwhal run --workers 2 --cwd ~/src/myrepo --prompt "audit this codebase"

# Smart: a planner agent decomposes the request into a DAG,
# then the coordinator dispatches workers in dependency order
narwhal plan --prompt "audit this codebase" --cwd ~/src/myrepo --concurrency 3

# Watch a run as it happens (from another terminal)
narwhal monitor            # attach to the newest live run
narwhal monitor --run <id> # attach to a specific run

# Inspect past runs
narwhal show               # list recent runs
narwhal show <run-id>      # full snapshot
```

The monitor shows the task DAG updating in place plus radio traffic as a
scrolling transcript, so an operator can watch workers cross-correct each
other, a split-request land, or a task fail and retry:

```
Narwhal  run-1786468655383  active

Tasks  2 total   0 done  2 running  0 ready  0 pending  0 failed

  ▶ task-1         worker-1
  ▶ task-2         worker-2

Agents  main, worker-task-1, worker-task-2

Radio  6 messages
    1 worker-task-1    ·                       task-1 starting security audit...
    2 worker-task-2    URGENT → worker-task-1  ACK worker-1's split: I take PER-FILE DEPTH...
    3 worker-task-2    URGENT → worker-task-1  INCONSISTENT CREDENTIAL SOURCE...
```

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
└── monitor
    └── polls the broker's read-only endpoint; DAG in place, radio scrolling
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

## CLI

| Command | Description |
|---|---|
| `narwhal run` | Flat parallel workers on a prompt |
| `narwhal plan` | Planner agent decomposes request into DAG, then coordinator executes |
| `narwhal monitor` | Live view of a running run (DAG + radio traffic) |
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

## License

MIT
