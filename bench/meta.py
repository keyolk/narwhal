#!/usr/bin/env python3
"""Write a trial's run_meta.json: outcome, wall clock, and what it cost.

Every arm goes through here so the fields summarize.py compares — cost,
served models, whether the tier asked for is the tier that ran — are computed
one way. They used to be written by a heredoc per arm, and only the Narwhal
arm recorded anything beyond wall clock.

Usage:
  meta.py <trial> <arm> <start> <end> <rc> --tier opus --sessions <sid>...
  meta.py <trial> narwhal <start> <end> <rc> --narwhal <narwhal.json>
          [--planner-tier opus] [--worker-tier haiku] [--synthesis-tier opus]
"""

import argparse
import glob
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import usage  # noqa: E402


def narwhal_fields(path):
    data = json.load(open(path))
    snap = data.get("snapshot", {})
    res = data.get("result", {})
    # coordinator.Result carries no json tags, so Go marshals the Go field
    # names verbatim.
    return {
        "tasks": len(snap.get("tasks", [])),
        "messages": len(snap.get("messages", [])),
        "session_dir": data.get("session_dir"),
        "completed": len(res.get("Completed") or []),
        "failed": len(res.get("Failed") or []),
        "unreached": len(res.get("Unreached") or []),
        "timed_out": bool(res.get("TimedOut")),
    }


# What Claude Code prints, as the whole of a session's output, when the
# account cannot serve it: out of quota, or the organization's managed
# settings failed to load (a network or auth blip; the CLI refuses to start
# without them). Either way the session stopped before the arm did anything.
# Matched on the full phrase: an agent's own answer can discuss rate
# limiting in the code it read.
QUOTA_RE = re.compile(
    r"You've hit your [a-z ]*limit"
    r"|requires remote managed settings to load, but they could not be loaded"
)


def quota_hit(trial, session_dir):
    """Whether any session of the trial stopped on an account limit.

    A pinned ccproxy does not wait out a throttled account: it exits at once
    with the limit message. Such a trial measured the account, not the arm,
    and must not be scored as the arm writing no answer.
    """
    paths = glob.glob(os.path.join(trial, "agent", "stdout*.txt"))
    if session_dir:
        paths += glob.glob(os.path.join(session_dir, "agents", "*", "claude-output.txt"))
        paths.append(os.path.join(session_dir, "planner-output.txt"))
    for p in paths:
        try:
            with open(p, errors="replace") as f:
                if QUOTA_RE.search(f.read(200_000)):
                    return True
        except OSError:
            continue
    return False


def session_dir_from_log(trial):
    """The narwhal session directory, read from narwhal.log.

    narwhal.json carries session_dir, but `narwhal plan` writes it only when
    the run gets as far as a result. A planner that stops on its first call —
    an account limit, most often — leaves narwhal.json empty, and the limit
    message sits in that session's planner-output.txt. The log names the run
    from its first line on.
    """
    try:
        with open(os.path.join(trial, "agent", "narwhal.log"), errors="replace") as f:
            for line in f:
                m = re.search(r"\[narwhal\] plan (\S+) started", line)
                if m:
                    return os.path.expanduser(os.path.join("~", ".narwhal", "sessions", m.group(1)))
    except OSError:
        pass
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("trial")
    ap.add_argument("arm")
    ap.add_argument("start", type=float)
    ap.add_argument("end", type=float)
    ap.add_argument("rc", type=int)
    ap.add_argument("--tier", default="")
    ap.add_argument("--sessions", nargs="*", default=[])
    ap.add_argument("--narwhal")
    ap.add_argument("--planner-tier", default="")
    ap.add_argument("--worker-tier", default="")
    ap.add_argument("--synthesis-tier", default="")
    args = ap.parse_args()

    meta = {"arm": args.arm, "exit_code": args.rc,
            "wall_clock_sec": round(args.end - args.start, 1)}
    u = None
    try:
        if args.narwhal:
            meta.update(narwhal_fields(args.narwhal))
            u = usage.tally_narwhal(args.narwhal, args.planner_tier,
                                    args.worker_tier, args.synthesis_tier)
            # Narwhal's tiers are per task; a run is valid only if every task
            # that named one got it.
            meta["tier_ok"] = not u["tier_mismatch"]
        else:
            u = usage.tally_sessions(args.sessions)
            meta["tier_ok"] = usage.tier_matches(args.tier, u["served_models"])
            meta["tier"] = args.tier
    except Exception as e:  # noqa: BLE001 — a crashed run still needs its meta
        meta["parse_error"] = str(e)

    if u is not None:
        json.dump(u, open(os.path.join(args.trial, "usage.json"), "w"), indent=2)
        meta["cost_usd"] = u["cost_usd"]
        meta["output_tokens"] = u["total"]["output_tokens"]
        meta["served_models"] = u["served_models"]
        meta["missing_sessions"] = len(u["missing_sessions"])

    ans = os.path.join(args.trial, "agent", "answer.txt")
    # Rubric judges reward length; recording it lets a win be checked
    # against "it just wrote more".
    meta["answer_bytes"] = os.path.getsize(ans) if os.path.exists(ans) else 0
    sdir = meta.get("session_dir") or session_dir_from_log(args.trial)
    meta["quota_hit"] = quota_hit(args.trial, sdir)

    json.dump(meta, open(os.path.join(args.trial, "run_meta.json"), "w"), indent=2)
    shown = {k: v for k, v in meta.items() if k not in ("session_dir", "served_models")}
    print(f"{args.arm} done: " + " ".join(f"{k}={v}" for k, v in shown.items()))


if __name__ == "__main__":
    main()
