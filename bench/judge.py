#!/usr/bin/env python3
"""Rubric judge for the SWE-Atlas QnA slice.

A port of AgentRadio's verify_local.py that talks to `claude --print`
instead of an OpenAI-compatible endpoint. The scoring logic (status/score
canonicalization, negative-rubric flip, must-have aggregation) is kept
identical so the numbers stay comparable to the published harness.

The judge model is pinned and checked. By default the judge calls `claude`
directly rather than through ccproxy, which rotates accounts and can hand a
turn to a non-Claude backend. When the direct account is out of quota,
JUDGE_CMD routes it through ccproxy restricted to one account, e.g.
JUDGE_CMD="ccproxy --accounts claude-personal claude". Either way every call
runs under a pinned session id and its transcript is checked: a verdict from
a model other than JUDGE_MODEL is treated as a failed call, not a score.

A trial the judge could not finish — any rubric left unscored by transport
errors or model drift — gets no evaluation_results.json, only
verifier/judge_error.json, and exits 2. The judge failing is not the arm
failing: the trial stays unjudged, run_slice.sh judges it again on the next
pass without re-running the agent, and summarize.py leaves it out rather than
scoring it 0.

Usage: judge.py <task-dir> <trial-dir>
"""

import json
import os
import re
import shlex
import subprocess
import sys
import time
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import usage  # noqa: E402

MAX_RETRIES = 4
JUDGE_MODEL = os.environ.get("JUDGE_MODEL", "opus")
JUDGE_CMD = shlex.split(os.environ.get("JUDGE_CMD", "claude"))


def _normalize_status(value):
    if value is None:
        return None
    status = str(value).strip().upper()
    if status in {"YES", "Y", "TRUE", "1"}:
        return "YES"
    if status in {"NO", "N", "FALSE", "0"}:
        return "NO"
    return None


def _normalize_score(value):
    if value is None:
        return None
    score = str(value).strip()
    if score in {"1", "1.0"}:
        return "1"
    if score in {"0", "0.0"}:
        return "0"
    lowered = score.lower()
    if lowered in {"yes", "true"}:
        return "1"
    if lowered in {"no", "false"}:
        return "0"
    return None


def _score_from_status(status):
    if status == "YES":
        return "1"
    if status == "NO":
        return "0"
    return None


def _apply_negative_flip(raw_score, rubric_type):
    if raw_score not in {"0", "1"}:
        return None, False
    if "negative" in (rubric_type or "").lower():
        return ("0" if raw_score == "1" else "1"), True
    return raw_score, False


def _canonicalize(parsed, rubric_type):
    if not isinstance(parsed, dict):
        return None
    normalized_status = _normalize_status(parsed.get("status"))
    normalized_score = _normalize_score(parsed.get("score"))
    status_score = _score_from_status(normalized_status)
    canonical = status_score if status_score is not None else normalized_score
    effective, flipped = _apply_negative_flip(canonical, rubric_type)
    if effective in {"0", "1"}:
        eff_status = "YES" if effective == "1" else "NO"
    elif canonical in {"0", "1"}:
        eff_status = "YES" if canonical == "1" else "NO"
    else:
        eff_status = normalized_status
    return {
        "rubric_statement": parsed.get("rubric_statement"),
        "status": eff_status,
        "score": effective,
        "justification": parsed.get("justification"),
        "judge_score_canonical": canonical,
        "was_flipped": flipped,
        "rubric_type": rubric_type,
    }


def _is_scored(obj):
    return isinstance(obj, dict) and str(obj.get("score")) in {"0", "1"}


def _parse_response(text):
    if not text:
        return None
    text = text.strip()
    if "```json" in text:
        after = text[text.find("```json") + 7:]
        end = after.find("```")
        if end != -1:
            text = after[:end].strip()
    if not text.startswith("{"):
        start = text.find('{"ratings"')
        if start == -1:
            start = text.find('{ "ratings"')
        if start != -1:
            text = text[start:]
            depth = 0
            for i, ch in enumerate(text):
                if ch == "{":
                    depth += 1
                elif ch == "}":
                    depth -= 1
                if depth == 0:
                    text = text[: i + 1]
                    break
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError:
        return None
    ratings = parsed.get("ratings") if isinstance(parsed, dict) else None
    if isinstance(ratings, list) and ratings:
        r = ratings[0]
        return {
            "rubric_statement": r.get("rubric_statement"),
            "status": r.get("status"),
            "score": r.get("score"),
            "justification": r.get("justification"),
        }
    return None


def ask_judge(system_prompt, user_content, served):
    sid = str(uuid.uuid4())
    proc = subprocess.run(
        JUDGE_CMD + ["--print", "--model", JUDGE_MODEL, "--session-id", sid,
                     "--append-system-prompt", system_prompt],
        input=user_content, capture_output=True, text=True, timeout=600,
    )
    if proc.returncode != 0:
        # claude prints quota and auth failures on stdout, not stderr.
        msg = (proc.stderr.strip() or proc.stdout.strip())[:300]
        raise RuntimeError(msg or "claude exited nonzero")
    models = usage.tally_sessions([sid])["served_models"]
    served.update(models)
    if usage.tier_matches(JUDGE_MODEL, models) is False:
        raise RuntimeError(f"judge served by {models}, not {JUDGE_MODEL}")
    return proc.stdout


def main():
    task_dir, trial_dir = sys.argv[1], sys.argv[2]
    tests = os.path.join(task_dir, "tests")
    answer_path = os.path.join(trial_dir, "agent", "answer.txt")
    verifier_dir = os.path.join(trial_dir, "verifier")
    os.makedirs(verifier_dir, exist_ok=True)
    reward_path = os.path.join(verifier_dir, "reward.txt")
    results_path = os.path.join(verifier_dir, "evaluation_results.json")

    if not os.path.exists(answer_path):
        print(f"No answer file at {answer_path}, scoring 0", file=sys.stderr)
        open(reward_path, "w").write("0\n")
        json.dump({"reward": 0, "pass": False, "agg_score": 0.0,
                   "num_scored": 0, "num_passed": 0, "no_answer": True},
                  open(results_path, "w"), indent=2)
        return

    answer = open(answer_path).read().strip()
    if "<<FINAL_ANSWER>>" in answer:
        parts = answer.split("<<FINAL_ANSWER>>")
        answer = parts[1].strip() if len(parts) >= 2 else answer
    if not answer:
        print("Empty answer, scoring 0", file=sys.stderr)
        open(reward_path, "w").write("0\n")
        json.dump({"reward": 0, "pass": False, "agg_score": 0.0,
                   "num_scored": 0, "num_passed": 0, "empty_answer": True},
                  open(results_path, "w"), indent=2)
        return

    system_prompt = open(os.path.join(tests, "system_prompt.txt")).read()
    template = open(os.path.join(tests, "user_prompt_template.txt")).read()
    rubrics = json.load(open(os.path.join(tests, "rubrics.json")))
    prompt_file = os.path.join(tests, "prompt.txt")
    problem = open(prompt_file).read().strip() if os.path.exists(prompt_file) else ""

    results = []
    served = set()
    errors = []
    for rubric in rubrics:
        title = re.sub(r"^\d+(\.\d+)*:\s*", "", rubric["title"])
        user_content = template.format(
            problem_statement=problem, model_answer=answer,
            title=json.dumps(title))
        judged = None
        for attempt in range(MAX_RETRIES):
            try:
                parsed = _parse_response(ask_judge(system_prompt, user_content, served))
                s = _score_from_status(_normalize_status(parsed.get("status"))) if parsed else None
                p = _normalize_score(parsed.get("score")) if parsed else None
                if parsed and (s in {"0", "1"} or p in {"0", "1"}):
                    judged = parsed
                    break
            except Exception as e:  # noqa: BLE001 — retry any transport failure
                wait = min(2 ** (attempt + 1), 30)
                print(f"  retry {attempt+1}/{MAX_RETRIES}: {e}", file=sys.stderr)
                if attempt == MAX_RETRIES - 1:
                    errors.append({"rubric": rubric["id"], "error": str(e)})
                time.sleep(wait)
        rubric_type = str(rubric.get("annotations", {}).get("type", ""))
        importance = rubric.get("importance") or \
            rubric.get("annotations", {}).get("importance", "must have")
        result = _canonicalize(judged, rubric_type) if judged else None
        results.append({"id": rubric["id"], "title": rubric["title"],
                        "importance": importance, "score": result})
        mark = result["score"] if _is_scored(result) else "UNSCORED"
        print(f"  rubric {rubric['id'][:8]}: {mark}")

    if errors:
        # Leave no verdict behind: see the module docstring.
        for stale in (results_path, reward_path):
            if os.path.exists(stale):
                os.remove(stale)
        json.dump({"errors": errors, "judge_cmd": JUDGE_CMD,
                   "judge_model": JUDGE_MODEL},
                  open(os.path.join(verifier_dir, "judge_error.json"), "w"), indent=2)
        print(f"Judge incomplete: {len(errors)}/{len(rubrics)} rubrics failed; "
              f"first error: {errors[0]['error']}", file=sys.stderr)
        sys.exit(2)
    stale_err = os.path.join(verifier_dir, "judge_error.json")
    if os.path.exists(stale_err):
        os.remove(stale_err)

    must = [r for r in results if r["importance"] == "must have"]
    scored_must = [r for r in must if _is_scored(r["score"])]
    all_pass = bool(scored_must) and all(str(r["score"]["score"]) == "1" for r in scored_must)
    scored = [r for r in results if _is_scored(r["score"])]
    passed = sum(1 for r in scored if str(r["score"]["score"]) == "1")
    agg = passed / len(scored) if scored else 0.0
    reward = 1 if all_pass else 0

    open(reward_path, "w").write(f"{reward}\n")
    json.dump({"reward": reward, "pass": all_pass, "agg_score": agg,
               "judge_model": JUDGE_MODEL, "judge_served": sorted(served),
               "num_rubrics": len(rubrics), "num_scored": len(scored),
               "num_passed": passed, "rubric_scores": results},
              open(results_path, "w"), indent=2)
    print(f"Result: reward={reward}, agg_score={agg:.3f}, pass={all_pass} "
          f"({passed}/{len(scored)} rubrics)")


if __name__ == "__main__":
    main()
