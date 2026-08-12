#!/usr/bin/env python3
"""Build the arm-specific prompt for a SWE-Atlas QnA task.

The published instruction.md assumes the container layout (`/app`,
`/logs/agent/answer.txt`). Neither path exists on the host, so both arms get
the same remapped text — remapping only one arm would compare plumbing, not
approach.

Usage: make_prompt.py <task-dir> <repo-path> <answer-path> [--arm b0|narwhal]
"""

import argparse
import os
import re
import sys

# The instruction's step 5 hands the agent a container-specific shell
# recipe. Replacing the whole block is cleaner than patching the paths
# inside it, since the heredoc form is what leads agents to /logs.
STEP5_RE = re.compile(
    r"5\. When you are confident.*", re.S)


def remap(text, repo, answer_path):
    text = STEP5_RE.sub(
        "5. When you are confident in your answer, write your complete final\n"
        "answer to {ans} wrapped in <<FINAL_ANSWER>> tags. Do not only print\n"
        "the answer in chat output; the answer file is required for scoring.\n"
        "Use this exact format:\n\n"
        "```bash\n"
        "mkdir -p {ansdir}\n"
        "cat <<'ANSWER_EOF' > {ans}\n"
        "<<FINAL_ANSWER>>\n"
        "Your comprehensive answer here, including all relevant findings,\n"
        "code references, and explanations.\n"
        "<<FINAL_ANSWER>>\n"
        "ANSWER_EOF\n"
        "```\n".format(ans=answer_path, ansdir=os.path.dirname(answer_path)),
        text)
    text = text.replace("/logs/agent/answer.txt", answer_path)
    text = text.replace("/app", repo)
    return text


NARWHAL_TAIL = """

## Coordination

You are one of several workers on this question. Others are working on
different parts of it at the same time.

- Radio traffic is how you learn what they found. Read it, and post what you
  find — a peer working from a wrong assumption cannot correct it unless you
  say so.
- Do not duplicate a peer's area if the radio already shows it covered; go
  deeper on yours instead.
- Report concrete evidence: file paths, function names, line numbers, exact
  values. The rubric rewards specifics, not summaries.

The final answer file is written once, by the synthesis task. If you are not
that task, do not write it — post your findings to the radio instead.
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("task_dir")
    ap.add_argument("repo")
    ap.add_argument("answer_path")
    ap.add_argument("--arm", default="b0", choices=["b0", "narwhal"])
    args = ap.parse_args()

    text = open(os.path.join(args.task_dir, "instruction.md")).read()
    out = remap(text, args.repo, args.answer_path)
    if args.arm == "narwhal":
        out += NARWHAL_TAIL
    sys.stdout.write(out)


if __name__ == "__main__":
    main()
