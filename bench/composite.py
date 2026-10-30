#!/usr/bin/env python3
"""Composite tasks: several SWE-Atlas QnA questions answered as one request.

A single QnA question fits one Opus session, and on those B0 is at ceiling
(pilot-1, hard-1). A composite asks for several questions about different
repositories in one request, so the work is longer than one session's
comfortable context and splits into independent parts. Each part keeps its
own published rubric, so a part's score in a composite compares directly
with the same task scored alone.

Usage:
  composite.py prompt <answer-path> --arm b0|narwhal <task-dir>=<repo> ...
  composite.py split <trial-dir> <task-dir> ...
  composite.py merge <trial-dir> <task-dir> ...
"""

import argparse
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import make_prompt  # noqa: E402

# Each part's own step 5 points at an answer file. A composite has one file
# with a section per part, so the step is rewritten to name the section.
PART_STEP5 = ("5. Put your complete answer to this part in section {marker} of "
              "the answer file described at the end of this request.\n")


def marker(k):
    return f"<<PART {k}>>"


def part_text(k, task_dir, repo):
    text = open(os.path.join(task_dir, "instruction.md")).read()
    text = make_prompt.STEP5_RE.sub(PART_STEP5.format(marker=marker(k)), text)
    text = text.replace("/logs/agent/answer.txt", f"section {marker(k)}")
    text = text.replace("/app", repo)
    image = make_prompt.image_of(task_dir)
    if image:
        text += make_prompt.ENV_NOTE.format(image=image, repo=repo)
    return text


NARWHAL_TAIL = """

## Coordination

You are one of several workers on this request. Others are working on other
parts of it at the same time.

- Radio traffic is how you learn what they found. Post what you find with the
  part number it belongs to.
- Report concrete evidence: file paths, function names, line numbers, exact
  values, captured output. The rubric rewards specifics, not summaries.

The final answer file is written once, by the synthesis task. If you are not
that task, do not write it — post your findings to the radio instead.
"""


def cmd_prompt(args):
    parts = [p.split("=", 1) for p in args.parts]
    n = len(parts)
    out = [f"This request has {n} independent parts, each about a different "
           f"code repository. Answer every part.\n"]
    for k, (task_dir, repo) in enumerate(parts, 1):
        out.append(f"\n# Part {k} of {n}\n\n" + part_text(k, task_dir, repo))
    sections = "\n".join(f"{marker(k)}\n<your complete answer to part {k}>"
                         for k in range(1, n + 1))
    ansdir = os.path.dirname(args.answer_path)
    out.append(f"""

# Answer file

Write the complete answer to every part to {args.answer_path}, one section
per part, each starting with its marker line exactly as shown. A part with
no section is scored as unanswered. Do not only print the answer in chat
output; the answer file is required for scoring.

```bash
mkdir -p {ansdir}
cat <<'ANSWER_EOF' > {args.answer_path}
{sections}
ANSWER_EOF
```
""")
    if args.arm == "narwhal":
        out.append(NARWHAL_TAIL)
    sys.stdout.write("".join(out))


def sections(text, n):
    """Split an answer into its parts by marker; a missing part is ''."""
    found = {}
    marks = [(m.start(), m.end(), int(m.group(1)))
             for m in re.finditer(r"<<PART (\d+)>>", text)]
    for i, (_, end, k) in enumerate(marks):
        stop = marks[i + 1][0] if i + 1 < len(marks) else len(text)
        # A repeated marker keeps the longer section: an agent that restates
        # the template at the end should not erase the real answer.
        body = text[end:stop].strip()
        if len(body) > len(found.get(k, "")):
            found[k] = body
    return [found.get(k, "") for k in range(1, n + 1)]


def cmd_split(args):
    src = os.path.join(args.trial, "agent", "answer.txt")
    text = open(src).read() if os.path.exists(src) else ""
    for k, body in enumerate(sections(text, len(args.tasks)), 1):
        d = os.path.join(args.trial, "parts", f"p{k}", "agent")
        os.makedirs(d, exist_ok=True)
        path = os.path.join(d, "answer.txt")
        if body:
            open(path, "w").write(f"<<FINAL_ANSWER>>\n{body}\n<<FINAL_ANSWER>>\n")
        elif os.path.exists(path):
            os.remove(path)


def cmd_merge(args):
    """One verdict for the composite from the parts' verdicts.

    Rubric counts add up, so summarize.py's pass rate is the share of all
    rubrics passed; reward is 1 only if every part passed. A part the judge
    did not finish leaves the composite unjudged, as judge.py does.
    """
    parts, passed, scored, rewards = [], 0, 0, []
    for k, task_dir in enumerate(args.tasks, 1):
        p = os.path.join(args.trial, "parts", f"p{k}", "verifier",
                         "evaluation_results.json")
        if not os.path.exists(p):
            print(f"part {k} unjudged; composite left unjudged", file=sys.stderr)
            sys.exit(2)
        ev = json.load(open(p))
        n_rubrics = len(json.load(open(os.path.join(task_dir, "tests", "rubrics.json"))))
        # An unanswered part has num_scored 0; count its rubrics as failed so
        # skipping a part cannot raise the pass rate.
        s = ev.get("num_scored") or n_rubrics
        passed += ev.get("num_passed", 0)
        scored += s
        rewards.append(ev.get("reward", 0) or 0)
        parts.append({"task": os.path.basename(task_dir), "passed": ev.get("num_passed", 0),
                      "scored": s, "reward": ev.get("reward", 0)})
    vdir = os.path.join(args.trial, "verifier")
    os.makedirs(vdir, exist_ok=True)
    reward = 1 if all(rewards) else 0
    json.dump({"reward": reward, "pass": bool(reward), "agg_score": passed / scored,
               "num_scored": scored, "num_passed": passed, "parts": parts},
              open(os.path.join(vdir, "evaluation_results.json"), "w"), indent=2)
    open(os.path.join(vdir, "reward.txt"), "w").write(f"{reward}\n")
    print("Composite: " + " ".join(f"{p['task'][-5:]}={p['passed']}/{p['scored']}"
                                   for p in parts) + f" total={passed}/{scored}")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("prompt")
    p.add_argument("answer_path")
    p.add_argument("--arm", default="b0", choices=["b0", "narwhal"])
    p.add_argument("parts", nargs="+")
    for name in ("split", "merge"):
        q = sub.add_parser(name)
        q.add_argument("trial")
        q.add_argument("tasks", nargs="+")
    args = ap.parse_args()
    {"prompt": cmd_prompt, "split": cmd_split, "merge": cmd_merge}[args.cmd](args)


if __name__ == "__main__":
    main()
