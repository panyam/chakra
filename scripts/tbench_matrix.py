"""Terminal-Bench run history as a task x setup matrix (#257).

Usage:
  tbench_matrix.py ingest <jobs-dir> [--source URL --notes TEXT --name NAME]
  tbench_matrix.py render <tasks-dir> [--out DIR]
  tbench_matrix.py stale <tasks-dir> N BUDGET

ingest reads Harbor job dirs and writes one JSON file per job to the results
dir (benchmarks/results/ in the repo, or $TB_RESULTS). It skips a job that
already has a file, so rerunning it is safe. With --source, the jobs are
published runs from someone else, and the file records where they came from.
render and stale read only the results dir and never parse Harbor output.

A setup is agent + model + effort, e.g. chakra/openai-gpt-5.5/medium. One
column per setup, one row per task in the task checkout.
"""

import argparse
import html
import json
import os
import shlex
import statistics
import subprocess
import sys
from datetime import datetime, timezone

import tbench_report

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RESULTS = os.environ.get("TB_RESULTS") or os.path.join(REPO, "benchmarks", "results")

# The agents chakra controls. Their cells age by commits since the run; the
# rest age by days, since we don't control when they change.
CHAKRA_AGENTS = {"chakra"}

# Commits that can change how chakra behaves on a task. Doc-only commits leave
# the cells as they were.
CODE_PATHS = ["*.go", "examples/terminal-bench"]

# List price in dollars per million tokens: input, cached input, output.
# OpenAI's cached rate is assumed at a tenth of input, as for earlier GPT-5 models.
PRICES = {
    "openai/gpt-5.5": (5.00, 0.50, 30.00),
    "anthropic/claude-sonnet-5-5": (2.00, 0.20, 10.00),
}


def setup_key(agent, model, effort):
    return f"{agent}/{(model or '?').replace('/', '-')}/{effort or 'default'}"


def parse_setup_key(key):
    agent, model, effort = key.split("/")
    provider, _, name = model.partition("-")
    return agent, f"{provider}/{name}", effort


def job_config(job_dir):
    try:
        with open(os.path.join(job_dir, "config.json")) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def job_finished(job_dir):
    """Harbor sets finished_at in the job's result.json once every trial is
    done. A job still running has only some trials, and ingest never rereads
    a job it has written, so it waits for this."""
    try:
        with open(os.path.join(job_dir, "result.json")) as f:
            return bool(json.load(f).get("finished_at"))
    except (OSError, ValueError):
        return False


def git(*args, cwd=REPO):
    out = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    return out.stdout.strip() if out.returncode == 0 else ""


def sha_before(when, cwd=REPO):
    """The last commit on the current branch's first-parent line before when.
    Jobs made before the adapter recorded its build sha get this as an
    estimate, and their records say so."""
    return git("rev-list", "-1", "--first-parent", f"--before={when}", "HEAD", cwd=cwd)[:12]


def read_job(job_dir, source=None, cwd=REPO):
    """One record per trial in a Harbor job dir, plus the job-level fields."""
    cfg = job_config(job_dir)
    agents = cfg.get("agents") or [{}]
    effort = (agents[0].get("kwargs") or {}).get("reasoning_effort")
    model = agents[0].get("model_name")
    job = cfg.get("job_name") or os.path.basename(job_dir.rstrip("/"))
    trials = []
    for name in sorted(os.listdir(job_dir)):
        path = os.path.join(job_dir, name, "result.json")
        if not os.path.isfile(path):
            continue
        with open(path) as f:
            r = json.load(f)
        info = r.get("agent_info") or {}
        agent = info.get("name") or "?"
        version = info.get("version")
        t = next(tbench_report.trials_from([(name, r)]))
        rec = {
            "task": t["task"],
            "setup": setup_key(agent, model or (r.get("config") or {}).get("agent", {}).get("model_name"), effort),
            "agent_version": version if version and version != "unknown" else None,
            "chakra_sha": None,
            "date": r.get("started_at"),
            "reward": t["reward"],
            "error": t["error"],
            "agent_s": t["agent_s"],
            "in": t["in"],
            "cached": t["cached"],
            "out": t["out"],
            "job": job,
        }
        if t["usage_partial"]:
            rec["usage_partial"] = True
        if agent in CHAKRA_AGENTS and source is None:
            if rec["agent_version"]:
                rec["chakra_sha"] = rec["agent_version"]
            elif rec["date"]:
                rec["chakra_sha"] = sha_before(rec["date"], cwd=cwd) or None
                rec["sha_inferred"] = True
        trials.append(rec)
    return job, trials


def ingest(jobs_dir, results=RESULTS, source=None, notes=None, name=None, cwd=REPO):
    """Writes results/<job>.json for each finished job dir under jobs_dir,
    skipping jobs already ingested. Unfinished jobs are named on stderr and
    left for a later run. With source set, jobs_dir holds published runs, and they
    all go into one file named name. Returns the paths written."""
    os.makedirs(results, exist_ok=True)
    have = {}
    for f in os.listdir(results):
        if f.endswith(".json"):
            for job in load_file(os.path.join(results, f)).get("jobs", []):
                have[job] = f
    dirs = [os.path.join(jobs_dir, d) for d in sorted(os.listdir(jobs_dir))
            if os.path.isfile(os.path.join(jobs_dir, d, "config.json"))]
    written = []
    if source:
        if not name:
            raise SystemExit("--source needs --name, e.g. ref-terminus2-claude-opus-4.6-2026-02")
        path = os.path.join(results, name + ".json")
        if os.path.exists(path):
            return written
        doc = {"source": source, "notes": notes, "jobs": [], "trials": []}
        for d in dirs:
            job, trials = read_job(d, source=source, cwd=cwd)
            doc["jobs"].append(job)
            doc["trials"] += trials
        write(path, doc)
        return [path]
    for d in dirs:
        job = job_config(d).get("job_name") or os.path.basename(d)
        if job in have:
            continue
        if not job_finished(d):
            print(f"{job}: not finished, left for a later ingest", file=sys.stderr)
            continue
        job, trials = read_job(d, cwd=cwd)
        if not trials:
            continue
        path = os.path.join(results, job + ".json")
        write(path, {"source": "local", "jobs": [job], "trials": trials})
        written.append(path)
    return written


def write(path, doc):
    with open(path, "w") as f:
        json.dump(doc, f, indent=1, sort_keys=True)
        f.write("\n")


def load_file(path):
    with open(path) as f:
        return json.load(f)


def load(results=RESULTS):
    """Every trial in the results dir, each tagged with its file's source."""
    out = []
    for f in sorted(os.listdir(results)) if os.path.isdir(results) else []:
        if not f.endswith(".json"):
            continue
        doc = load_file(os.path.join(results, f))
        for t in doc.get("trials", []):
            out.append({**t, "source": doc.get("source", "local")})
    return out


def task_names(tasks_dir):
    return sorted(d for d in os.listdir(tasks_dir)
                  if os.path.isfile(os.path.join(tasks_dir, d, "task.toml")))


def solved(t):
    return not t.get("error") and (t.get("reward") or 0) >= 1


class Cell:
    def __init__(self, trials):
        self.trials = sorted(trials, key=lambda t: t.get("date") or "")
        self.last = self.trials[-1] if self.trials else None

    @property
    def tally(self):
        return f"{sum(map(solved, self.trials))}/{len(self.trials)}"

    @property
    def date(self):
        return self.last.get("date") if self.last else None


# Errors that count against the agent: it ran out of time, or it exited with
# an error. Any other error, such as a setup timeout or a sandbox that failed
# to build, means the agent never got to try. That trial goes in no tally,
# and tbench-stale picks the cell again.
AGENT_ERRORS = {"AgentTimeoutError", "NonZeroAgentExitCodeError"}


def counted(t):
    return not t.get("error") or t["error"] in AGENT_ERRORS


def matrix(trials, tasks):
    """Returns (setups, rows, cells): cells[(task, setup)] is a Cell of the
    counted trials. Tasks absent from tasks still get rows, so a renamed task
    doesn't vanish."""
    setups = sorted({t["setup"] for t in trials}, key=lambda s: (any(
        t["source"] != "local" for t in trials if t["setup"] == s), s))
    rows = sorted(set(tasks) | {t["task"] for t in trials})
    by = {}
    for t in filter(counted, trials):
        by.setdefault((t["task"], t["setup"]), []).append(t)
    return setups, rows, {(r, s): Cell(by.get((r, s), [])) for r in rows for s in setups}


def is_chakra(setup):
    return setup.split("/")[0] in CHAKRA_AGENTS


def commits_since(sha, cwd=REPO):
    if not sha:
        return None
    sha = sha.removesuffix("-dirty")
    n = git("rev-list", "--count", f"{sha}..HEAD", "--", *CODE_PATHS, cwd=cwd)
    return int(n) if n.isdigit() else None


def short_date(iso):
    d = datetime.fromisoformat(iso.replace("Z", "+00:00"))
    return f"{d:%b} {d.day}"


def cell_text(cell, setup, cwd=REPO):
    if not cell.last:
        return ""
    text = f"{cell.tally} · {short_date(cell.date)}"
    if is_chakra(setup) and cell.last.get("source", "local") == "local":
        n = commits_since(cell.last.get("chakra_sha"), cwd=cwd)
        if n is not None:
            text += f" · +{n}c" + ("?" if cell.last.get("sha_inferred") else "")
    return text


def sources(trials):
    out = {}
    for t in trials:
        if t["source"] != "local":
            out[t["setup"]] = t["source"]
    return out


def render_md(trials, tasks, cwd=REPO):
    setups, rows, cells = matrix(trials, tasks)
    lines = ["| task | " + " | ".join(setups) + " |", "|---|" + "---|" * len(setups)]
    for r in rows:
        lines.append(f"| {r} | " + " | ".join(cell_text(cells[(r, s)], s, cwd) for s in setups) + " |")
    lines.append("")
    lines.append("| setup | solved | tasks run |")
    lines.append("|---|---|---|")
    for s in setups:
        mine = [c for (r, s2), c in cells.items() if s2 == s and c.last]
        n = sum(len(c.trials) for c in mine)
        k = sum(sum(map(solved, c.trials)) for c in mine)
        lines.append(f"| {s} | {k}/{n} | {len(mine)}/{len(rows)} |")
    refs = sources(trials)
    if refs:
        lines.append("")
        for s, src in sorted(refs.items()):
            lines.append(f"- {s}: published run, {src}")
    lines.append("")
    lines.append("Chakra cells show `+Nc`, the commits touching code since the run's build. "
                 "`?` marks a build sha inferred from the run's date.")
    return "\n".join(lines) + "\n"


PAGE = """<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Terminal-Bench matrix</title>
<style>
:root {{ --bg:#fff; --fg:#1d1d1f; --mute:#6e6e73; --line:#e5e5ea; --pass:#1f7a3a; --part:#9a6a00; --fail:#b42318; --ref:#f5f5f7; }}
@media (prefers-color-scheme: dark) {{ :root {{ --bg:#111113; --fg:#f2f2f7; --mute:#98989f; --line:#2c2c30; --pass:#5ad17f; --part:#e8b64c; --fail:#ff7b72; --ref:#1c1c1f; }} }}
body {{ background:var(--bg); color:var(--fg); font:13px/1.4 -apple-system,system-ui,sans-serif; margin:16px; }}
h1 {{ font-size:18px; margin:0 0 4px; }} p {{ color:var(--mute); margin:0 0 12px; }}
.wrap {{ overflow-x:auto; }}
table {{ border-collapse:collapse; }}
th, td {{ border-bottom:1px solid var(--line); padding:4px 10px; text-align:left; white-space:nowrap; }}
th {{ position:sticky; top:0; background:var(--bg); font-weight:600; }}
td.ref, th.ref {{ background:var(--ref); }}
.pass {{ color:var(--pass); }} .part {{ color:var(--part); }} .fail {{ color:var(--fail); }} .never {{ color:var(--mute); }}
</style></head><body>
<h1>Terminal-Bench 2: last runs</h1>
<p>{summary}</p>
<div class="wrap"><table>
<tr><th>task</th>{head}</tr>
{body}
</table></div>
<p>{foot}</p>
</body></html>
"""


def render_html(trials, tasks, cwd=REPO):
    setups, rows, cells = matrix(trials, tasks)
    refs = sources(trials)
    cls = lambda s: ' class="ref"' if s in refs else ""
    head = "".join(f"<th{cls(s)}>{html.escape(s)}</th>" for s in setups)
    body = []
    for r in rows:
        tds = []
        for s in setups:
            c = cells[(r, s)]
            if not c.last:
                tone = "never"
            else:
                k = sum(map(solved, c.trials))
                tone = "pass" if k == len(c.trials) else "fail" if k == 0 else "part"
            ref = " ref" if s in refs else ""
            tds.append(f'<td class="{tone}{ref}">{html.escape(cell_text(c, s, cwd)) or "·"}</td>')
        body.append(f"<tr><td>{html.escape(r)}</td>{''.join(tds)}</tr>")
    ran = sum(1 for c in cells.values() if c.last)
    summary = f"{len(rows)} tasks, {len(setups)} setups, {ran} of {len(cells)} cells run."
    foot = ("Chakra cells show +Nc, the commits touching code since the run's build; ? marks a sha "
            "inferred from the date. Shaded columns are published runs: "
            + "; ".join(f"{html.escape(s)} from {html.escape(u)}" for s, u in sorted(refs.items()))) if refs else (
            "Chakra cells show +Nc, the commits touching code since the run's build; ? marks a sha inferred from the date.")
    return PAGE.format(summary=summary, head=head, body="\n".join(body), foot=foot)


def trial_cost(t, price):
    pin, pcache, pout = price
    tin, tcache, tout = t.get("in") or 0, t.get("cached") or 0, t.get("out") or 0
    return ((tin - tcache) * pin + tcache * pcache + tout * pout) / 1e6


def avg(xs):
    return sum(xs) / len(xs) if xs else None


def from_references(task, ours, refs, price):
    """Dollars for task, scaled from published runs that covered it. Each
    reference setup that ran the task and shares tasks with ours gives one
    figure. That's its average cost on the task at our price, times the median
    ratio of our cost to its cost over the tasks both have run. The result is
    the mean of those figures, or None when no reference qualifies."""
    our_by = {}
    for t in ours:
        our_by.setdefault(t["task"], []).append(trial_cost(t, price))
    figures = []
    for setup in sorted({t["setup"] for t in refs}):
        ref_by = {}
        for t in refs:
            if t["setup"] == setup:
                ref_by.setdefault(t["task"], []).append(trial_cost(t, price))
        shared = [k for k in our_by if k in ref_by and avg(ref_by[k])]
        if task not in ref_by or not shared:
            continue
        ratio = statistics.median(avg(our_by[k]) / avg(ref_by[k]) for k in shared)
        figures.append(avg(ref_by[task]) * ratio)
    return avg(figures)


def estimate(task, setup, trials):
    """Dollars for one more trial of task on setup, at list price. None means
    no price is known for the setup's model. It tries, in order:

    1. the task's average on this setup;
    2. the task's cost on our other setups with the same model, scaled to this
       setup the same way as step 3, or their plain average when this setup
       shares no task with them yet;
    3. published runs of the task, scaled to our cost (from_references);
    4. the median trial on this model.

    Step 3 matters for tasks we never ran. Their cost varies by more than ten
    times, so the median alone undercounted #263's batch by about 2.5 times.
    Step 2 scales because Codex spends about twice what chakra does on the
    same task. Unscaled, it undercounted the first mission batch by half.
    """
    _, model, _ = parse_setup_key(setup)
    price = PRICES.get(model)
    if price is None:
        return None
    priced = [t for t in trials if t.get("in") is not None and t["source"] == "local"
              and parse_setup_key(t["setup"])[1] == model]
    mine = [t for t in priced if t["setup"] == setup]
    hits = [trial_cost(t, price) for t in mine if t["task"] == task]
    if hits:
        return avg(hits)
    others = [t for t in priced if t["setup"] != setup]
    if any(t["task"] == task for t in others):
        scaled = from_references(task, mine, others, price)
        if scaled is not None:
            return scaled
        return avg([trial_cost(t, price) for t in others if t["task"] == task])
    refs = [t for t in trials if t.get("in") is not None and t["source"] != "local"]
    scaled = from_references(task, priced, refs, price)
    if scaled is not None:
        return scaled
    allc = [trial_cost(t, price) for t in priced]
    return statistics.median(allc) if allc else None


def stale(trials, tasks, n, budget, setups=None):
    """Picks up to n cells, never-run first, then oldest. Cells come from
    setups, or from every local setup with a run if setups is None, and never
    from published columns. It stops before the first cell whose estimate would
    take the total past budget. Returns (picks, total), picks being (task,
    setup, estimate)."""
    have, _, cells = matrix(trials, tasks)
    refs = sources(trials)
    want = [s for s in (setups if setups is not None else have) if s not in refs]
    date = lambda r, s: cells[(r, s)].date if (r, s) in cells else None
    cands = [(date(r, s) or "", r, s) for s in want for r in tasks]
    cands.sort()
    picks, total = [], 0.0
    for _, r, s in cands:
        if len(picks) >= n:
            break
        cost = estimate(r, s, trials)
        if cost is None:
            raise SystemExit(f"no list price for {parse_setup_key(s)[1]}; add it to PRICES in {__file__}")
        if total + cost > budget:
            break
        picks.append((r, s, cost))
        total += cost
    return picks, total


def commands(picks):
    """One `make tbench` per setup, since Harbor takes one agent per job."""
    by = {}
    for r, s, _ in picks:
        by.setdefault(s, []).append(r)
    out = []
    for s, ts in by.items():
        agent, model, effort = parse_setup_key(s)
        out.append("make tbench " + " ".join(shlex.quote(a) for a in [
            f"TB_AGENTS={agent}", f"TB_MODEL={model}",
            f"TB_EFFORT={'' if effort == 'default' else effort}", f"TB_TASKS={' '.join(ts)}"]))
    return out


def read_jobs(path):
    """The job names in a file, one per line, as `make tbench` writes them to
    TB_JOBS_LOG."""
    with open(path) as f:
        return {line.strip() for line in f if line.strip()}


def report(trials, tasks, since=None, setups=None, jobs=None):
    """Markdown for the mission log after a batch. The batch is the local
    trials in jobs, a set of job names, or else every local trial started at
    or after since (an ISO time in UTC). Pass jobs when another batch might
    have run alongside, since a time window counts that batch's trials too
    (#289). It prints three things: the summary row of each setup the batch
    touched (or each of setups), every cell the batch changed as before ->
    after, and what the batch cost at list price. Trials whose usage is
    missing or partial are counted, so the cost reads as a lower bound when
    there are any."""
    if jobs is not None:
        in_batch = lambda t: t["source"] == "local" and t.get("job") in jobs
    elif since is not None:
        in_batch = lambda t: t["source"] == "local" and (t.get("date") or "") >= since
    else:
        raise ValueError("report needs since or jobs")
    new = [t for t in trials if in_batch(t)]
    want = setups or sorted({t["setup"] for t in new})
    _, rows, cells = matrix(trials, tasks)
    lines = ["| setup | solved | tasks run |", "|---|---|---|"]
    for s in want:
        mine = [c for (r, s2), c in cells.items() if s2 == s and c.last]
        n = sum(len(c.trials) for c in mine)
        k = sum(sum(map(solved, c.trials)) for c in mine)
        lines.append(f"| {s} | {k}/{n} | {len(mine)}/{len(rows)} |")
    lines += ["", "| task | setup | before | after |", "|---|---|---|---|"]
    for task, setup in sorted({(t["task"], t["setup"]) for t in new if counted(t)}):
        cell = [t for t in trials if t["task"] == task and t["setup"] == setup and counted(t)]
        old = Cell([t for t in cell if not in_batch(t)])
        after = Cell(cell)
        lines.append(f"| {task} | {setup} | {old.tally if old.last else 'never run'} | {after.tally} |")
    cost, missing, partial = 0.0, 0, 0
    skipped = sorted(f"{t['task']} on {t['setup'].split('/')[0]} ({t['error']})" for t in new if not counted(t))
    for t in new:
        price = PRICES.get(parse_setup_key(t["setup"])[1])
        if t.get("in") is None or price is None:
            missing += 1
            continue
        partial += bool(t.get("usage_partial"))
        cost += trial_cost(t, price)
    caveat = []
    if missing:
        caveat.append(f"{missing} without token counts")
    if partial:
        caveat.append(f"{partial} cut off by the time limit, counted up to the cut")
    lines += ["", f"{len(new)} trials, ${cost:.2f} at list price"
              + (f" ({', '.join(caveat)}, so this is a lower bound)" if caveat else "") + "."]
    if skipped:
        lines.append(f"Didn't run, so not counted: {'; '.join(skipped)}.")
    return "\n".join(lines) + "\n"


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    i = sub.add_parser("ingest")
    i.add_argument("jobs_dir")
    i.add_argument("--source")
    i.add_argument("--notes")
    i.add_argument("--name")
    r = sub.add_parser("render")
    r.add_argument("tasks_dir")
    r.add_argument("--out", default=os.path.join(REPO, "benchmarks", "matrix"))
    s = sub.add_parser("stale")
    s.add_argument("tasks_dir")
    s.add_argument("n", type=int)
    s.add_argument("budget", type=float)
    s.add_argument("--setups", help="space-separated setup keys to pick from; default every local setup with a run")
    rp = sub.add_parser("report")
    rp.add_argument("tasks_dir")
    rp.add_argument("since", nargs="?", help="ISO time in UTC; trials started at or after it are the batch")
    rp.add_argument("--jobs", help="file of job names, one per line; those jobs are the batch, in place of since")
    rp.add_argument("--setups", help="space-separated setup keys for the summary rows")
    a = p.parse_args(argv)

    if a.cmd == "ingest":
        written = ingest(a.jobs_dir, source=a.source, notes=a.notes, name=a.name)
        for w in written:
            print(f"wrote {os.path.relpath(w, REPO)}")
        print(f"{len(written)} new" if written else "nothing new")
    elif a.cmd == "render":
        trials, tasks = load(), task_names(a.tasks_dir)
        os.makedirs(a.out, exist_ok=True)
        md = render_md(trials, tasks)
        with open(os.path.join(a.out, "matrix.md"), "w") as f:
            f.write(md)
        with open(os.path.join(a.out, "index.html"), "w") as f:
            f.write(render_html(trials, tasks))
        print(md)
        print(f"wrote {os.path.relpath(os.path.join(a.out, 'index.html'), REPO)}")
    elif a.cmd == "report":
        if not a.since and not a.jobs:
            p.error("report needs since or --jobs")
        jobs = read_jobs(a.jobs) if a.jobs else None
        print(report(load(), task_names(a.tasks_dir), a.since, a.setups.split() if a.setups else None, jobs), end="")
    else:
        trials, tasks = load(), task_names(a.tasks_dir)
        picks, total = stale(trials, tasks, a.n, a.budget, a.setups.split() if a.setups else None)
        for r, s, c in picks:
            last = Cell([t for t in trials if t["task"] == r and t["setup"] == s]).date
            print(f"# {s} {r}: {'never run' if not last else 'last ' + short_date(last)}, ~${c:.2f}",
                  file=sys.stderr)
        print(f"# {len(picks)} cells, ~${total:.2f} at list price", file=sys.stderr)
        for c in commands(picks):
            print(c)


if __name__ == "__main__":
    main()
