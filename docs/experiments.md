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

## Implications for the design

1. Workers must be directly executed processes, not Workflow subagents.
2. `--permission-mode bypassPermissions` is mandatory for headless workers;
   without it the agent workspace is unreachable.
3. `drain` remains useful as a belt-and-braces check at natural boundaries,
   covering messages that land between watcher cycles.
4. The "exactly one watcher" invariant from AgentRadio carries over: the
   worker must restart its watcher as soon as one resolves.

## Reproducing

```bash
go build -o narwhal ./cmd/narwhal
./narwhal experiment --cwd /tmp --timeout 6m
```

Artifacts land under `~/.narwhal/sessions/<run-id>/`.
