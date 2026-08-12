#!/usr/bin/env python3
"""Summarize a benchmark slice.

With n in the single digits the strict all-must-have reward is almost always
0 for both arms, so per-rubric pass rate is the primary metric and reward is
reported alongside it rather than instead of it.

Usage: summarize.py <results-dir>
"""

import json
import os
import sys

ARMS = ["b0", "narwhal", "hybrid"]


def load(trial):
    out = {}
    for name, key in (("verifier/evaluation_results.json", "eval"),
                      ("run_meta.json", "meta")):
        path = os.path.join(trial, name)
        if os.path.exists(path):
            try:
                out[key] = json.load(open(path))
            except json.JSONDecodeError:
                pass
    return out


def main():
    root = sys.argv[1]
    tasks = sorted(d for d in os.listdir(root)
                   if os.path.isdir(os.path.join(root, d)))

    rows = []
    for task in tasks:
        row = {"task": task}
        for arm in ARMS:
            row[arm] = load(os.path.join(root, task, arm))
        rows.append(row)

    print(f"{'task':<34} {'arm':<8} {'rubrics':>9} {'pass%':>6} "
          f"{'reward':>6} {'wall':>7} {'tasks':>6} {'msgs':>5}")
    print("-" * 88)
    totals = {a: {"passed": 0, "scored": 0, "reward": 0, "wall": 0.0, "n": 0}
              for a in ARMS}

    for row in rows:
        for arm in ARMS:
            d = row[arm]
            ev, mt = d.get("eval"), d.get("meta", {})
            if ev:
                p, s = ev.get("num_passed", 0), ev.get("num_scored", 0)
                pct = f"{100*p/s:.0f}" if s else "-"
                rw = ev.get("reward", 0)
                totals[arm]["passed"] += p
                totals[arm]["scored"] += s
                totals[arm]["reward"] += rw
            else:
                p = s = 0
                pct, rw = "-", "-"
            wall = mt.get("wall_clock_sec", 0.0)
            totals[arm]["wall"] += wall
            totals[arm]["n"] += 1
            print(f"{row['task'][-12:]:<34} {arm:<8} {f'{p}/{s}':>9} {pct:>6} "
                  f"{str(rw):>6} {wall:>6.0f}s {str(mt.get('tasks','-')):>6} "
                  f"{str(mt.get('messages','-')):>5}")
        print()

    print("=" * 88)
    print(f"{'TOTAL':<34} {'arm':<8} {'rubrics':>9} {'pass%':>6} {'reward':>6} {'wall':>7}")
    for arm in ARMS:
        t = totals[arm]
        pct = f"{100*t['passed']/t['scored']:.1f}" if t["scored"] else "-"
        ratio = "{}/{}".format(t["passed"], t["scored"])
        print(f"{'':<34} {arm:<8} {ratio:>9} "
              f"{pct:>6} {t['reward']:>6} {t['wall']:>6.0f}s")

    json.dump({"totals": totals, "rows": rows},
              open(os.path.join(root, "summary.json"), "w"), indent=2)


if __name__ == "__main__":
    main()
