# Narwhal

Graph-engineered multi-agent runtime with passive awareness.

Narwhal combines **Orca-style graph engineering** (Run/Task/Dispatch DAG) with
**AgentRadio-style passive awareness** (background watcher + radio channel) to
coordinate multiple Claude Code workers on a single task.

Each worker runs as a directly-executed `ccproxy claude --print` process, which
preserves Claude Code's full tool surface — including background Bash task
completion notifications, the mechanism Workflow subagents currently do not
surface. ccproxy handles account routing and quota; Narwhal owns the
collaboration and planning layer.

## Status

Experimental. Not yet functional.

## Design

```
narwhal
├── graph layer (Orca style)
│   ├── Run         namespace + coordinator inbox
│   ├── Task        deps edges, state machine
│   └── Dispatch    attempt unit, failure_count, heartbeat
│
├── radio layer (AgentRadio style)
│   ├── channel     message bus scoped to a Run
│   ├── thread      planning / worklog / results
│   ├── message     content + mentions + priority
│   ├── cursor      monotonic sequence
│   └── watch       background long-poll + notification
│
├── launcher
│   ├── each worker: ccproxy claude --print
│   ├── per-agent identity token
│   └── wrapper scripts (send/drain/watch/state)
│
└── viewer (planned)
    ├── DAG visualization
    └── message timeline
```

### Naming

| Term | Meaning |
|---|---|
| tusk | broker endpoint (the narwhal's horn = antenna) |
| pod | agent group (a pod of narwhals) |
| dive | task dispatch |
| surface | task completion |
| click | radio message (echolocation click) |

### What graph and radio each own

**Graph owns:** what tasks exist, what depends on what, when a task is ready,
when to dispatch a worker, whether a task succeeded.

**Radio owns:** what workers tell each other while running, urgent assumption
corrections, dead-end sharing, split requests for new tasks.

Graph operates at task granularity (minutes to hours). Radio operates at
discovery granularity (seconds to minutes).

### Plan immutability

Existing tasks are immutable. New tasks can be added to the same Run
(split-request). This is a deliberate compromise between Orca's strict
immutability and AgentRadio's free-form plan negotiation.

## CLI (planned)

```bash
narwhal run --workers auto --prompt "analyze this repo's auth flow"
narwhal show <run-id>
narwhal session
```

## Implementation phases

1. **Broker + worker launcher** — Run/Task/Dispatch state, radio channel,
   `ccproxy claude --print` worker execution, send/drain/watch wrappers.
2. **DAG + concurrency** — deps-based ready calculation, parallel dispatch,
   automatic dependent activation.
3. **Passive awareness** — verify background watch delivers notifications in
   directly-executed Claude Code, message-driven behavior change.
4. **Dynamic task addition** — split-request from workers, coordinator adds
   new tasks to an active Run.
5. **Viewer** — DAG visualization, message timeline, token cost tracking.

## License

MIT
