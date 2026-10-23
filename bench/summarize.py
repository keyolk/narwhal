#!/usr/bin/env python3
"""Summarize a benchmark slice: per-arm score, cost, spread, and a CI on the
difference from the baseline.

Layout: <results>/<task>/<arm>/r<k>/ for repeated trials. A trial directory
directly under <arm>/ (the layout before repeats) is read as a single repeat.

Three rules decide what a number means here:

- A trial whose agent wrote no answer scores 0 (the judge records that as a
  verdict). An agent failure is a loss for the arm that failed, not a missing
  data point — dropping it would reward the arm that crashes on hard tasks.
- A trial the judge did not finish has no verdict and is left out, counted
  under `unjudged`. That is the judge failing, not the arm; rerun the slice
  and run_slice.sh judges it again.
- A trial served by a model other than the tier it asked for is excluded and
  counted. It measured a different comparison from the one in the table.
- The CI is a paired bootstrap over tasks. Rubrics within a task are
  correlated (one wrong reading fails several), so treating rubrics as
  independent samples would make the interval look far tighter than it is.

Usage: summarize.py <results-dir> [--baseline b0] [--iters 10000]
"""

import argparse
import json
import os
import random
import statistics

ARM_ORDER = ["b0", "fanout", "narwhal", "narwhal-nogate", "hybrid"]


def load_json(path):
    try:
        return json.load(open(path))
    except (OSError, json.JSONDecodeError):
        return None


def trial_dirs(arm_dir):
    reps = sorted(d for d in os.listdir(arm_dir)
                  if d.startswith("r") and d[1:].isdigit()
                  and os.path.isdir(os.path.join(arm_dir, d)))
    if reps:
        return [os.path.join(arm_dir, d) for d in reps]
    if os.path.exists(os.path.join(arm_dir, "run_meta.json")):
        return [arm_dir]
    return []


def read_trial(path):
    ev = load_json(os.path.join(path, "verifier", "evaluation_results.json"))
    meta = load_json(os.path.join(path, "run_meta.json")) or {}
    t = {"path": path, "meta": meta,
         "wall": meta.get("wall_clock_sec", 0.0),
         "cost": meta.get("cost_usd"),
         "bytes": meta.get("answer_bytes", 0),
         # tier_ok is None when nothing was requested or nothing was measured
         # (trials from before meta.py); only an observed mismatch excludes.
         "invalid": meta.get("tier_ok") is False}
    if ev and ev.get("num_scored"):
        t["passed"], t["scored"] = ev.get("num_passed", 0), ev["num_scored"]
        t["reward"] = ev.get("reward", 0) or 0
        t["judged"] = True
    elif ev:
        # A verdict with nothing scored: the agent wrote no usable answer.
        t["passed"], t["scored"], t["reward"], t["judged"] = 0, 0, 0, True
    else:
        t["passed"], t["scored"], t["reward"], t["judged"] = 0, 0, 0, False
    return t


def rate(t, rubric_total):
    # An unjudged trial has no num_scored of its own; score it against the
    # task's rubric count so it reads as 0/N, not as undefined.
    denom = t["scored"] or rubric_total
    return t["passed"] / denom if denom else 0.0


def mean(xs):
    return sum(xs) / len(xs) if xs else None


def bootstrap(diffs, iters, seed=0):
    rng = random.Random(seed)
    n = len(diffs)
    samples = sorted(mean([diffs[rng.randrange(n)] for _ in range(n)])
                     for _ in range(iters))
    return samples[int(0.025 * iters)], samples[int(0.975 * iters) - 1]


def fmt(x, spec):
    return "-" if x is None else format(x, spec)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--baseline", default="b0")
    ap.add_argument("--iters", type=int, default=10000)
    args = ap.parse_args()

    tasks = sorted(d for d in os.listdir(args.root)
                   if os.path.isdir(os.path.join(args.root, d)))
    seen = set()
    data = {}
    for task in tasks:
        data[task] = {}
        tdir = os.path.join(args.root, task)
        for arm in sorted(os.listdir(tdir)):
            adir = os.path.join(tdir, arm)
            if not os.path.isdir(adir):
                continue
            trials = [read_trial(p) for p in trial_dirs(adir)]
            if trials:
                data[task][arm] = trials
                seen.add(arm)
    arms = [a for a in ARM_ORDER if a in seen] + sorted(seen - set(ARM_ORDER))
    # Attempts set aside because an account limit stopped them (run_slice.sh
    # keeps them as r<k>.quota-<ts>). Not scored; a trial that ran out of
    # retries is missing from its cell, and this is where that shows.
    quota = {}
    for task in tasks:
        for arm in os.listdir(os.path.join(args.root, task)):
            adir = os.path.join(args.root, task, arm)
            if os.path.isdir(adir):
                n = sum(1 for d in os.listdir(adir) if ".quota-" in d)
                if n:
                    quota[arm] = quota.get(arm, 0) + n

    # Per task, the rubric count is whatever any judged trial reported; all
    # arms answer the same rubric.
    rubrics = {task: max((t["scored"] for ts in data[task].values() for t in ts), default=0)
               for task in tasks}

    # task -> arm -> mean over valid repeats
    cell = {}
    print(f"{'task':<14} {'arm':<8} {'n':>3} {'excl':>4} {'pass%':>6} {'sd':>5} "
          f"{'reward':>6} {'wall':>6} {'cost$':>7} {'KB':>6}")
    print("-" * 74)
    for task in tasks:
        cell[task] = {}
        for arm in arms:
            ts = data[task].get(arm, [])
            if not ts:
                continue
            unjudged = sum(1 for t in ts if not t["invalid"] and not t["judged"])
            valid = [t for t in ts if not t["invalid"] and t["judged"]]
            rates = [rate(t, rubrics[task]) for t in valid]
            costs = [t["cost"] for t in valid]
            c = {
                "n": len(valid), "excluded": sum(1 for t in ts if t["invalid"]),
                "pass": mean(rates),
                # Spread across repeats of the same arm on the same task. If
                # it is as large as the gap between arms, the gap is noise.
                "sd": statistics.stdev(rates) if len(rates) > 1 else None,
                "reward": mean([t["reward"] for t in valid]),
                "wall": mean([t["wall"] for t in valid]),
                "cost": None if not costs or None in costs else mean(costs),
                "kb": mean([t["bytes"] / 1024 for t in valid]),
                "unjudged": unjudged,
            }
            cell[task][arm] = c
            print(f"{task[-12:]:<14} {arm:<8} {c['n']:>3} {c['excluded']:>4} "
                  f"{fmt(c['pass'] and 100 * c['pass'], '.0f'):>6} "
                  f"{fmt(c['sd'] and 100 * c['sd'], '.0f'):>5} "
                  f"{fmt(c['reward'], '.2f'):>6} {fmt(c['wall'], '.0f'):>5}s "
                  f"{fmt(c['cost'], '.2f'):>7} {fmt(c['kb'], '.0f'):>6}")
        print()

    print("=" * 74)
    print(f"{'ARM':<8} {'tasks':>5} {'pass%':>6} {'reward':>6} {'mean sd':>7} "
          f"{'wall':>6} {'cost$':>7} {'excl':>4} {'unjudged':>8}")
    totals = {}
    for arm in arms:
        cs = [cell[t][arm] for t in tasks if arm in cell[t] and cell[t][arm]["n"]]
        unj = sum(cell[t][arm]["unjudged"] for t in tasks if arm in cell[t])
        sds = [c["sd"] for c in cs if c["sd"] is not None]
        costs = [c["cost"] for c in cs]
        tot = {
            "tasks": len(cs),
            "pass": mean([c["pass"] for c in cs]),
            "reward": mean([c["reward"] for c in cs]),
            "sd": mean(sds),
            "wall": mean([c["wall"] for c in cs]),
            "cost": None if not costs or None in costs else mean(costs),
            "excluded": sum(cell[t][arm]["excluded"] for t in tasks if arm in cell[t]),
            "unjudged": unj,
        }
        totals[arm] = tot
        print(f"{arm:<8} {tot['tasks']:>5} {fmt(tot['pass'] and 100 * tot['pass'], '.1f'):>6} "
              f"{fmt(tot['reward'], '.2f'):>6} {fmt(tot['sd'] and 100 * tot['sd'], '.1f'):>7} "
              f"{fmt(tot['wall'], '.0f'):>5}s {fmt(tot['cost'], '.2f'):>7} "
              f"{tot['excluded']:>4} {tot['unjudged']:>8}")

    if quota:
        print("\nattempts stopped by an account limit (not scored): " +
              ", ".join(f"{a}={n}" for a, n in sorted(quota.items())))

    base = args.baseline
    deltas = {}
    if base in arms:
        print()
        print(f"vs {base}: paired over tasks both arms have, 95% bootstrap CI")
        print(f"{'ARM':<8} {'tasks':>5} {'Δpass pp':>9} {'CI':>17} "
              f"{'Δreward':>8} {'CI':>15} {'cost x':>7}")
        for arm in arms:
            if arm == base:
                continue
            common = [t for t in tasks
                      if cell[t].get(arm, {}).get("n") and cell[t].get(base, {}).get("n")]
            if not common:
                continue
            dp = [cell[t][arm]["pass"] - cell[t][base]["pass"] for t in common]
            dr = [cell[t][arm]["reward"] - cell[t][base]["reward"] for t in common]
            lo_p, hi_p = bootstrap(dp, args.iters)
            lo_r, hi_r = bootstrap(dr, args.iters)
            ca = [cell[t][arm]["cost"] for t in common]
            cb = [cell[t][base]["cost"] for t in common]
            ratio = (sum(ca) / sum(cb)
                     if None not in ca and None not in cb and sum(cb) else None)
            deltas[arm] = {"tasks": len(common), "d_pass": mean(dp), "ci_pass": [lo_p, hi_p],
                           "d_reward": mean(dr), "ci_reward": [lo_r, hi_r], "cost_ratio": ratio}
            print(f"{arm:<8} {len(common):>5} {100 * mean(dp):>+9.1f} "
                  f"{f'[{100 * lo_p:+.1f}, {100 * hi_p:+.1f}]':>17} "
                  f"{mean(dr):>+8.2f} {f'[{lo_r:+.2f}, {hi_r:+.2f}]':>15} "
                  f"{fmt(ratio, '.2f'):>7}")
        print("\nA CI that spans 0 is not a difference. With under ~10 tasks it "
              "almost always will.")

    json.dump({"arms": arms, "cells": cell, "totals": totals, "vs_baseline": deltas},
              open(os.path.join(args.root, "summary.json"), "w"), indent=2)


if __name__ == "__main__":
    main()
