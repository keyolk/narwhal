#!/usr/bin/env python3
"""Measure what a benchmark trial consumed, from Claude transcripts.

Both arms are read the same way — by session id, from the transcript Claude
Code writes under ~/.claude/projects — so a cost difference between them is a
difference in what ran, not in how it was counted. This mirrors
internal/usage (dedup by message.id, skip <synthetic> turns) and adds two
things the Go side does not need:

- Subagent transcripts. A B0 session may delegate through the Agent tool, and
  those turns are filed under <session-id>/subagents/, not in the parent
  transcript. Leaving them out would make B0 look cheaper than it was.
- A dollar estimate. Token classes are not interchangeable (cache reads
  dominate every run and cost a tenth of fresh input), so comparing arms on
  raw token totals would mostly compare cache hit rates.

Usage:
  usage.py session <session-id>...     tally named sessions
  usage.py narwhal <narwhal.json>      tally a narwhal run (planner + workers)
"""

import glob
import json
import os
import sys

# $ per million tokens: (input, output, cache_read). Cache writes are billed
# at 1.25x input (5-minute TTL), which is what Claude Code uses. Source: the
# claude-api skill's model table, cached 2026-10-06. Where that table gives no
# cache-read price, the standard 0.1x input is assumed.
#
# Keyed by prefix so a dated id (claude-opus-4-5-20251101) still matches.
# Longest prefix wins, so claude-opus-5-5 is not priced as claude-opus-5.
PRICES = {
    "claude-fable-5-1": (10.00, 50.00, 0.25),
    "claude-fable-5": (10.00, 50.00, 1.00),
    "claude-opus-5-5": (4.00, 20.00, 0.20),
    "claude-opus-5": (5.00, 25.00, 0.50),
    "claude-opus-4": (5.00, 25.00, 0.50),
    "claude-sonnet-5-5": (2.00, 10.00, 0.20),
    "claude-sonnet-5": (2.00, 10.00, 0.20),
    "claude-sonnet-4-6": (3.00, 15.00, 0.30),
    "claude-haiku-5-5": (0.10, 0.50, 0.01),
    "claude-haiku-4-5": (1.00, 5.00, 0.10),
}

SYNTHETIC = "<synthetic>"


def price_for(model):
    best = None
    for prefix in PRICES:
        if model.startswith(prefix) and (best is None or len(prefix) > len(best)):
            best = prefix
    return PRICES.get(best)


def transcript_paths(session_id):
    """The session's own transcript plus any subagent transcripts under it.

    A session id is a UUID, so globbing across every project directory
    cannot pick up another session's file.
    """
    root = os.path.expanduser("~/.claude/projects")
    own = glob.glob(os.path.join(root, "*", session_id + ".jsonl"))
    subs = glob.glob(os.path.join(root, "*", session_id, "subagents", "*.jsonl"))
    return own, subs


def empty():
    return {"input_tokens": 0, "output_tokens": 0, "cache_creation_tokens": 0,
            "cache_read_tokens": 0, "turns": 0}


def tally_files(paths, seen, by_model):
    for path in paths:
        try:
            f = open(path, errors="replace")
        except OSError:
            continue
        with f:
            for line in f:
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    # The last line of a live transcript can be half-flushed.
                    continue
                if rec.get("type") != "assistant":
                    continue
                msg = rec.get("message") or {}
                usage, model, mid = msg.get("usage"), msg.get("model", ""), msg.get("id", "")
                if not usage or model == SYNTHETIC or not mid or mid in seen:
                    continue
                # One response is written once per content block with the
                # usage repeated, so counting lines would overstate ~2x.
                seen.add(mid)
                t = by_model.setdefault(model, empty())
                t["input_tokens"] += usage.get("input_tokens", 0) or 0
                t["output_tokens"] += usage.get("output_tokens", 0) or 0
                t["cache_creation_tokens"] += usage.get("cache_creation_input_tokens", 0) or 0
                t["cache_read_tokens"] += usage.get("cache_read_input_tokens", 0) or 0
                t["turns"] += 1


def cost(by_model):
    """Estimated $; None when any model that ran has no known price.

    An unpriced model (a ccproxy backend outside the Claude family) makes the
    total unknown rather than lower — summing only the priced part would read
    as a cheap run.
    """
    total = 0.0
    for model, t in by_model.items():
        p = price_for(model)
        if p is None:
            return None
        inp, out, cread = p
        total += (t["input_tokens"] * inp
                  + t["cache_creation_tokens"] * inp * 1.25
                  + t["cache_read_tokens"] * cread
                  + t["output_tokens"] * out) / 1e6
    return round(total, 4)


def summarize(by_model, sessions, missing):
    total = empty()
    for t in by_model.values():
        for k in total:
            total[k] += t[k]
    served = sorted(by_model, key=lambda m: -by_model[m]["turns"])
    # Any session without a transcript makes the total unknown: a trial that
    # never started would otherwise read as a free one.
    total_cost = None if missing else cost(by_model)
    return {"total": total, "by_model": by_model, "served_models": served,
            "cost_usd": total_cost, "sessions": sessions,
            # A session whose transcript is gone is unmeasured, not free.
            "missing_sessions": missing}


def tally_sessions(session_ids):
    seen, by_model, missing = set(), {}, []
    for sid in session_ids:
        own, subs = transcript_paths(sid)
        if not own:
            missing.append(sid)
        tally_files(own + subs, seen, by_model)
    return summarize(by_model, list(session_ids), missing)


def tier_matches(requested, served_models):
    """Whether every served model is of the requested tier.

    The tier is a family name (opus, haiku) or a full id; ccproxy maps a tier
    to whatever account has quota, so this is the check that the comparison
    is between the models it claims to be between.
    """
    if not requested:
        return None
    req = requested.lower()
    return all(req in m.lower() for m in served_models) if served_models else None


def read_sid(path):
    try:
        return open(path).read().strip() or None
    except OSError:
        return None


def tally_narwhal(narwhal_json, planner_tier="", worker_tier="", synthesis_tier=""):
    """Tally a narwhal run, checking each agent against the tier it was given.

    The benchmark's run-level tiers override the planner's model field. The
    task field is used only when the harness did not fix that role's tier.
    Without the fallback, a fixed-tier run would pass the check vacuously.
    """
    data = json.load(open(narwhal_json))
    sdir = data.get("session_dir")
    if not sdir:
        raise SystemExit(f"{narwhal_json}: no session_dir")
    requested = {t["id"]: t.get("model", "")
                 for t in (data.get("snapshot") or {}).get("tasks", [])}

    agents, sids = [], []
    planner = read_sid(os.path.join(sdir, "planner-session-id"))
    if planner:
        sids.append(planner)
        agents.append({"agent": "planner", "session": planner, "requested": planner_tier})
    for d in sorted(glob.glob(os.path.join(sdir, "agents", "*"))):
        sid = read_sid(os.path.join(d, "claude-session-id"))
        if not sid:
            continue
        name = os.path.basename(d)
        task = name[len("worker-"):] if name.startswith("worker-") else name
        fixed = synthesis_tier if task == "synthesis" else worker_tier
        tier = fixed or requested.get(task, "")
        sids.append(sid)
        agents.append({"agent": task, "session": sid, "requested": tier})

    for a in agents:
        one = tally_sessions([a["session"]])
        a["served_models"] = one["served_models"]
        a["cost_usd"] = one["cost_usd"]
        a["output_tokens"] = one["total"]["output_tokens"]
        a["tier_ok"] = tier_matches(a.get("requested", ""), one["served_models"])

    out = tally_sessions(sids)
    out["agents"] = agents
    out["tier_mismatch"] = [a["agent"] for a in agents if a.get("tier_ok") is False]
    return out


def main():
    if len(sys.argv) < 3 or sys.argv[1] not in ("session", "narwhal"):
        raise SystemExit(__doc__)
    if sys.argv[1] == "session":
        out = tally_sessions(sys.argv[2:])
    else:
        out = tally_narwhal(sys.argv[2])
    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
