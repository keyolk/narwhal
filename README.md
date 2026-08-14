# Narwhal

Graph-engineered multi-agent runtime with passive awareness.

Narwhal combines **Orca-style graph engineering** (Run/Task/Dispatch DAG)
with **AgentRadio-style passive awareness** (background watcher + radio
channel) to coordinate multiple Claude Code workers on a single task.

Each worker runs as a directly-executed `claude --print` process, which
preserves Claude Code's full tool surface — including background Bash task
completion notifications, the mechanism Workflow subagents currently do not
surface. Narwhal owns the collaboration and planning layer and asks for a
model *tier*; whatever fronts the API (ccproxy here) decides which backend
serves that tier and handles account routing and quota.

## Status

Functional. 158 unit tests (race-clean), 8 end-to-end experiments with real
Claude Code workers, and a benchmark slice against SWE-Atlas QnA.

## Quick Start

### Interactive (recommended)

Register Narwhal as an MCP server, then drive it from a normal Claude Code
session. Your session stays in the conversation; workers run headless
underneath and report back.

```bash
make install   # builds and puts narwhal on PATH (~/.local/bin)
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

The monitor is an interactive TUI: the task graph on the left, a node
inspector and the radio channel stacked on the right, and detail views for
reading a full message or watching a worker. Radio messages are long — that
is the point of them — so the list truncates and the detail view is where
you actually read one.

`s` opens the selected task's session: a live activity feed of what the
worker is doing — its reasoning, each tool call, and a clipped result —
read from the session transcript and following new events until you scroll
up. `n`/`p` walk between workers without leaving the view.

```
Session  ▶ fact-a  dispatched          agent=worker-fact-a  23 events  [following]

14:33:07 · I'll start the background watcher and read note.md first.
14:33:08 → Bash  watch
14:33:08 → Read  narwhal-gate-e2e/note.md
           1 # Gate E2E fixture
           … 3 more lines
14:34:28 → Bash  send worklog "fact A: the color is blue (from note.md line 3)"
           {"Seq":2,"RunID":"s1786631577522-1","ThreadID":"worklog",…
14:34:31 · worklog 게시 완료. 이제 task-done을 호출합니다.
```

The transcript, not the captured stdout: a `claude --print` worker buffers
its output until it exits, so the log is empty for the whole time you would
want to watch. The transcript is appended as the session happens. When the
worker finishes, its final answer is appended to the end of the same feed.

`a` attaches to the session itself, handing the terminal to a real Claude
session — for reading the whole thing or intervening. The launcher pins
each worker's session id with `--session-id`, so both the feed and the
attach find exactly that worker's conversation rather than guessing which
transcript belongs to whom. Attaching opens forked (`--fork-session`):
watching a worker must not append to the transcript it is still writing.

```
Narwhal  s1786471179534-1  ▶ active
account routing 경로의 일관성 검증

Graph ───────────────────────────────── Node ──────────────────────────────────
        ┌──────────┐  ┌────────┐        ▶ api (api-audit)  dispatched
        │ ▶ api ×2 │  │ ✓ auth │         ◆ model haiku
        └─────┬────┘  └────┬───┘         ↓ blocks synthesis
              └────┬───────┘             ↻ tries 2 of 3
                   │                     ▤ holds api/router.go
            ┌──────┴──────┐              » recent
            │ · synthesis │                14:34:28 → Bash  send worklog "..."
            └─────────────┘                14:34:31 · worklog 게시 완료.

                                        Radio (4) ─────────────────────────────
                                        03:12:00 · auth cross-path asymmetries…
                                        03:13:34 ! api URGENT: provider lock re…
                                        03:15:30 · coordinator →synthesis ⋮ tas…
                                        03:16:00 · api ⋮ claims api/router.go

done 1  running 2  ready 0  failed 0  [following]
tab pane · hjkl move · enter detail · s session · a attach · esc runs · q quit
```

The right side follows the graph cursor. Moving between nodes used to change
nothing else on screen, so reading one meant opening a detail view and
backing out of it — heavy for a question like *what is this waiting on*. The
inspector answers those while you navigate: model, unfinished dependencies
(not every edge — the graph already draws those), what the task blocks,
retries, files it holds, and the tail of what its worker has been doing.

Below it the radio stays the **whole** channel rather than being filtered to
the selected node. A channel is what everyone is talking on, and plenty of
traffic belongs to no node at all. Rows carry a timestamp, because a sequence
number says the order but only the clock says whether two findings landed
together or an hour apart.

Coordination traffic is rendered as sentences. Workers speak a pipe-delimited
wire format — `FILE_CLAIM|api|internal/api/router.go` — which is right for a
shell script to emit and wrong to put on screen, where it reads as noise
until you split it on pipes yourself. It shows as `⋮ claims api/router.go`,
dimmed so it recedes behind a worker's actual finding.

Tasks are drawn as boxes wired together, in the style of Graph::Easy and
`dgraph`. Boxes are sized to their label and centered, so there are margins
for edges to route through; siblings share a row while they fit and wrap
when they do not. A fan-out or fan-in is drawn as one shape — a single
horizontal bar — rather than as independent edges that would overwrite each
other, and a run that would otherwise cut through a wrapped row detours
through the margin.

The graph is two-dimensional, so navigation is too — and each axis moves
along what it actually means. `h` and `l` walk a row of boxes and wrap into
the next. `j` and `k` follow the **dependency edges**: in a diagram that
line is what "below" means, and it is the one relationship the horizontal
keys cannot express. So `j` from any sibling of a fan-in reaches the child
it feeds, even when the child is drawn off to one side.

With no edge to follow, `j` falls back to a box sharing your columns, so it
still works in a graph with no dependencies at all. With neither, the cursor
stays put — moving to an unrelated box because it was the only candidate
teaches you the arrow keys are unpredictable. Backing out of the graph is
`esc`, not `h`: a direction key inside a diagram should move, not exit.

A lone child is drawn under its parent rather than centred in the pane.
Rows are otherwise centred independently, which is right for a row of
siblings and wrong the moment a row holds one box — the edge still pointed
at the right parent, but the diagram read as though it pointed elsewhere.

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
Narwhal  2 live, 1 finished

▸ ▶ 08-13 13:58  /tmp/nw-picker         running
    auth 모듈 보안 감사
  ✓ 08-13 13:58  .../keyolk/narwhal     5/5  12 msg
    monitor TUI 리팩터링 계획
  ✗ 08-12 23:03  .../src/myrepo         3/5  2 failed
```

A live run is marked `▶` and keeps its colour; a finished one is `✓` — or
`✗` when a task failed, which is the reason to open a run you would
otherwise scroll past. Each row carries its outcome, because a list that
says only when and where makes you open every run to learn whether it
worked, which is the question you had before opening anything. With the list now mostly history, a wall of identically dim rows
would bury the one run you can still act on — and the glyph carries that
where colour cannot, in a screenshot or a washed-out palette.

The list carries recent history, not just what is running: a finished run
is exactly when you want to ask what it did, and the snapshots on disk were
readable only by `narwhal show`. Live runs come first, then the last 20
finished ones, marked as such. Opening a finished run reads its snapshot —
there is no broker left to poll.

A run id is just a timestamp, so the list is keyed on what actually tells
runs apart: when it started, an abbreviated working directory, and the
prompt. `enter` digs into a run and `esc` backs out to the list — `[` and
`]` also step between runs from inside one, and the header shows the
position (`[2/3]`). The list refreshes while the monitor is open — a run
started afterwards appears, a finished one drops out — and the selection
follows the run being watched rather than its index.
`narwhal monitor --run <id>` opens one directly.

Colour carries meaning rather than decorating, and the same hue means the
same thing in every pane:

| | |
|---|---|
| green | finished, and finished well |
| cyan | in flight — the things actually moving |
| yellow | waiting on something |
| red | failed, or urgent |
| blue | who said it (agent and task names) |
| magenta | structure the run is built from (threads, models, files) |
| dim | furniture: borders, labels, keys, anything you read past |

A box takes its task's colour, frame included, so it reads as one object. A
count of zero stays dim — it is not news. An urgent radio message is the one
thing pulled across a full pane, since it may invalidate what a peer is
doing right now. Colours are ANSI 0–7, so the terminal's own theme picks the
shades; a hard-coded palette fights whatever you have chosen and loses.

Graph glyphs are plain box-drawing, so they render in any font. Task,
priority and inspector field icons use Nerd Font when the terminal has one:

```bash
NARWHAL_ICONS=nerd     # force Nerd Font icons
NARWHAL_ICONS=unicode  # force the portable set
```

Detection is conservative — an unrecognized terminal gets portable glyphs,
since guessing wrong toward Nerd Font fills the pane with tofu boxes. It
does look through tmux, though: `TERM_PROGRAM` reads "tmux" inside a
session, which hid the terminal actually drawing the pixels and left users
on fallback glyphs for as long as they stayed in tmux. `TERM` and the
terminal's own shell-integration variables survive, and either is enough.

| Key | Action |
|---|---|
| `tab` | Switch between the graph and radio panes |
| `j` / `k` | Move the cursor; in the graph, along dependency edges (also `↓` / `↑`) |
| `h` / `l` | Move between boxes along a row (also `←` / `→`); from the radio, switch panes |
| `ctrl+d` / `ctrl+u` | Page down / up |
| `g` / `G` | Jump to first / last |
| `enter` | Open the selected task or message |
| `s` | Open the selected task's session activity feed (follows live) |
| `a` | Attach to the worker's Claude session itself (forked) |
| `b` | Toggle between box and lane graph views |
| `[` / `]` | Previous / next live run |
| `esc` | Back out: detail → run, run → run list, list → quit |
| `n` / `p` | Next / previous item inside the detail view |
| `f` | Re-arm tail following after manual navigation |
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
│   ├── thread      planning / worklog / results / environment
│   ├── message     content + mentions + priority (fyi/normal/urgent)
│   ├── cursor      monotonic sequence (no message lost on watcher restart)
│   └── watch       background long-poll + completion notification
│
├── planner
│   └── ccproxy claude --print decomposes request → task DAG via API
│
├── coordinator
│   └── deps-driven parallel dispatch, terminal state (batch runs)
│
│   Graph-mutating intake — split-request, dep edges, file claims, model
│   escalation — lives on broker.Run, because the daemon drives the same
│   protocol and a rule only one dispatcher knows is a rule that silently
│   does not apply to interactive runs.
│
├── launcher
│   ├── each worker: ccproxy claude --print --permission-mode bypassPermissions
│   ├── per-agent identity token (endpoint = identity)
│   ├── pinned --session-id, so the monitor can attach to a live worker
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
    └── Bubble Tea TUI: task list, radio traffic, message detail,
        per-worker session activity read from the Claude transcript
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

### Deps gate completion, not dispatch

The synthesis task depends on every investigation task, but it is
dispatched immediately alongside them. Its job is to be listening while
they work — a watcher on the radio, folding findings in as they land — and
that only works if it is alive at the same time as its peers.

The dependency is enforced at the other end: `task-done` blocks until every
dep has finished, announcing the wait on the radio so a held worker is
distinguishable from a hung one. If peers posted while the call was held,
it answers `202` with those messages instead of completing — the outcome
was written before they arrived — and the worker folds them in and calls
once more.

Blocking rather than refusing is the part that took two tries. The first
version answered `409` and told the worker to try again later. The worker
read it, replied "I will keep the watcher up and wait" — and its process
exited, because `claude --print` ends when the model's turn ends. Intending
to wait is not waiting. The coordinator saw a dispatch with no `task-done`,
retried, and the circuit breaker failed the task on the third attempt.
Holding the HTTP request open is what actually keeps the worker alive.

This replaced an instruction-only version, where the synthesis task had no
deps at all and its assignment simply told it to wait. It did not. Across
three consecutive runs the synthesis worker posted its last message before
its peers posted theirs — in one case four messages early, missing the
peer's final summary entirely — and in another it gave up waiting and
re-ran a peer's investigation itself, spending a frontier model on
duplicate work. A worker has no way to observe that a peer has finished, so
"wait until they are done" was not a followable instruction. The gate makes
it one.

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
- ✓ Radio activity counts as completion (a worker that posted but forgot task-done is not retried)
- ✓ Diamond dependency (A→B, A→C, B+C→D)
- ✓ Partial failure → dependents unreached
- ✓ Dynamic task addition (split-request, immutable existing tasks)
- ✓ Dynamic dependency edges (DEP_ADD / DEP_REMOVE mid-run)
- ✓ Completion gate (synthesis runs early, but cannot finish before its deps)
- ✓ File ownership (claim / release, conflicts answered on the radio)
- ✓ Model escalation (worker asks for a stronger tier, breaker still bounds retries)
- ✓ Run terminal state (done/failed)
- ✓ Run persistence + inspection
- ✓ Coordinating agent (planner decomposes request into DAG)
- ✓ Live monitor (DAG progress + radio traffic + per-worker session view)
- ✓ Interactive mode (daemon + MCP; add workers to an in-flight run)

### AgentRadio-style passive awareness

- ✓ Thread/message/mention with priority (fyi/normal/urgent)
- ✓ Monotonic cursor message log
- ✓ Long-poll watch with wake channels
- ✓ Non-blocking drain
- ✓ Agent identity token = endpoint path
- ✓ Background notification (verified in directly-executed Claude Code)
- ✓ Live peer correction (verified: workers cross-correct mid-flight)
- ✓ Multi-thread radio (planning/worklog/results/environment)
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
- **E9**: "Wait for your peers" as an instruction — **refuted** (synthesis finished early in 3 of 3 runs; deps now gate completion)

### SWE-Atlas QnA benchmark slice

Two slices of the benchmark AgentRadio publishes against, with Narwhal on
one arm and a single Claude Code session (B0) on the other. Slice 1 (easy
tasks) hits ceiling — both arms 100%. Slice 2 (harder tasks, 4 arms) shows
Smart — haiku investigate + opus synthesis — at 94.9% rubric coverage,
above Narwhal's 92.3% and B0's 89.7%, at wall-clock below B0 (52 min vs
58 min). See [`docs/benchmark.md`](docs/benchmark.md) and
[`docs/experiments.md`](docs/experiments.md) §E7–E8.

## Development

```bash
make            # list targets
make install    # build with a version stamp, replace the binary on PATH
make check      # go vet + tests under the race detector
make cover      # coverage summary
make daemon-restart   # reinstall, then restart the daemon detached
```

`make install` removes the old binary before copying the new one. macOS
will not overwrite a running Mach-O in place, and the failure mode is not
an error message — the binary is left in a state where every invocation
dies with SIGKILL.

## License

MIT
