"""Side-by-side report of Harbor Terminal-Bench jobs (#195).

Usage: tbench_report.py <jobs-dir> [job ...]

With only a jobs directory, it reports the newest job of each agent there
(jobs are named tbench-<agent>-<timestamp> by `make tbench`). Otherwise it
reports the jobs named. Prints markdown: a summary per agent, then one row per
task with each agent's reward, agent time and tokens.
"""

import json
import os
import sys
from datetime import datetime


def seconds(span):
    if not span or not span.get("started_at") or not span.get("finished_at"):
        return None
    parse = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
    return (parse(span["finished_at"]) - parse(span["started_at"])).total_seconds()


def trials(job_dir):
    def results():
        for name in sorted(os.listdir(job_dir)):
            path = os.path.join(job_dir, name, "result.json")
            if os.path.isfile(path):
                with open(path) as f:
                    yield name, json.load(f)
    yield from trials_from(results())


def trials_from(results):
    """One summary per trial from (trial dir name, parsed result.json) pairs."""
    for name, r in results:
        agent = (r.get("agent_info") or {}).get("name") or "?"
        res = r.get("agent_result") or {}
        rewards = (r.get("verifier_result") or {}).get("rewards") or {}
        exc = r.get("exception_info") or {}
        yield {
            "task": (r.get("task_name") or name).split("/")[-1],
            "agent": agent,
            "reward": rewards.get("reward"),
            "error": exc.get("exception_type") if exc else None,
            "agent_s": seconds(r.get("agent_execution")),
            "in": res.get("n_input_tokens"),
            "cached": res.get("n_cache_tokens"),
            "out": res.get("n_output_tokens"),
            "usage_partial": bool((res.get("metadata") or {}).get("usage_partial")),
        }


def newest_jobs(jobs_dir):
    newest = {}
    for name in sorted(os.listdir(jobs_dir)):
        if not name.startswith("tbench-") or not os.path.isdir(os.path.join(jobs_dir, name)):
            continue
        agent = name[len("tbench-"):].rsplit("-", 2)[0]
        newest[agent] = os.path.join(jobs_dir, name)  # sorted, so the last is newest
    return list(newest.values())


def fmt_tokens(n):
    if n is None:
        return "-"
    return f"{n / 1000:.0f}k" if n >= 1000 else str(n)


def setup(job_dir):
    """Names the agent, model and reasoning effort a job ran with, from its
    config.json. A job without reasoning_effort ran at the agent's default."""
    try:
        with open(os.path.join(job_dir, "config.json")) as f:
            agents = json.load(f).get("agents") or []
    except (OSError, ValueError):
        return f"{os.path.basename(job_dir)}: setup unknown"
    parts = []
    for a in agents:
        effort = (a.get("kwargs") or {}).get("reasoning_effort") or "default"
        parts.append(f"{a.get('name', '?').split(':')[-1].removesuffix('Agent').lower()}: {a.get('model_name', '?')}, effort {effort}")
    return "; ".join(parts) or f"{os.path.basename(job_dir)}: setup unknown"


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    jobs = sys.argv[2:] or newest_jobs(sys.argv[1])
    if not jobs:
        sys.exit(f"no tbench-* jobs in {sys.argv[1]}")
    report(jobs)


def report(jobs):
    rows = [t for j in jobs for t in trials(j)]
    agents = sorted({r["agent"] for r in rows})
    tasks = sorted({r["task"] for r in rows})
    by = {}
    for r in rows:
        by.setdefault((r["task"], r["agent"]), []).append(r)

    print("| agent | solved | errors | median agent time | input (cached) | output |")
    print("|---|---|---|---|---|---|")
    for a in agents:
        mine = [r for r in rows if r["agent"] == a]
        solved = sum(1 for r in mine if (r["reward"] or 0) >= 1)
        errors = sum(1 for r in mine if r["error"])
        times = sorted(r["agent_s"] for r in mine if r["agent_s"] is not None)
        median = f"{times[len(times) // 2]:.0f}s" if times else "-"
        tin = sum(r["in"] or 0 for r in mine)
        tca = sum(r["cached"] or 0 for r in mine)
        tout = sum(r["out"] or 0 for r in mine)
        print(f"| {a} | {solved}/{len(mine)} | {errors} | {median} | {fmt_tokens(tin)} ({fmt_tokens(tca)}) | {fmt_tokens(tout)} |")

    print()
    print("| task | " + " | ".join(agents) + " |")
    print("|---|" + "---|" * len(agents))
    for t in tasks:
        cells = []
        for a in agents:
            runs = by.get((t, a), [])
            if not runs:
                cells.append("")
                continue
            parts = []
            for r in runs:
                if r["error"]:
                    parts.append(f"error ({r['error']})")
                else:
                    mark = "pass" if (r["reward"] or 0) >= 1 else "fail"
                    t_s = f"{r['agent_s']:.0f}s" if r["agent_s"] is not None else "-"
                    parts.append(f"{mark} {t_s} {fmt_tokens(r['in'])}")
            cells.append(", ".join(parts))
        print(f"| {t} | " + " | ".join(cells) + " |")
    print()
    print("Setup: " + " | ".join(setup(j) for j in jobs))
    print()
    print("Jobs: " + ", ".join(os.path.basename(j) for j in jobs))


if __name__ == "__main__":
    main()
