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
import json
import os
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

    json.dump(meta, open(os.path.join(args.trial, "run_meta.json"), "w"), indent=2)
    shown = {k: v for k, v in meta.items() if k not in ("session_dir", "served_models")}
    print(f"{args.arm} done: " + " ".join(f"{k}={v}" for k, v in shown.items()))


if __name__ == "__main__":
    main()
