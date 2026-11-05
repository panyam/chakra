"""Imports a published Terminal-Bench 2 leaderboard submission as a reference
column (#267).

Usage: tbench_ref.py <submission> [--out DIR]

<submission> is a folder name under submissions/terminal-bench/2.0/ in the
leaderboard dataset, e.g. Terminus2__Claude-Opus-4.6. It downloads the
submission's metadata.yaml, each job's config.json and each trial's
result.json into DIR/<submission>, which is all ingest reads. Then it ingests
them into benchmarks/results/ as ref-<submission>-<yyyy-mm>.json, with notes
built from the metadata and the job configs.
"""

import argparse
import json
import os
import sys
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor

import tbench_matrix

DATASET = "harborframework/terminal-bench-2-leaderboard"
ROOT = "submissions/terminal-bench/2.0"
API = f"https://huggingface.co/api/datasets/{DATASET}/tree/main/"
RAW = f"https://huggingface.co/datasets/{DATASET}/resolve/main/"
PAGE = f"https://huggingface.co/datasets/{DATASET}/tree/main/"


def http_get(url):
    with urllib.request.urlopen(url, timeout=60) as r:
        return r.read()


def fetch(sub, out, get=http_get):
    """Downloads what ingest needs into out. Skips files already there, so a
    rerun after a dropped connection picks up where it stopped. Returns the
    number of files written."""
    base = f"{ROOT}/{sub}"
    ls = lambda path: json.loads(get(API + urllib.parse.quote(path)))
    want = [f"{base}/metadata.yaml"]
    for job in ls(base):
        if job["type"] != "directory":
            continue
        want.append(job["path"] + "/config.json")
        want += [t["path"] + "/result.json" for t in ls(job["path"]) if t["type"] == "directory"]

    def one(path):
        dst = os.path.join(out, os.path.relpath(path, base))
        if os.path.exists(dst):
            return 0
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        data = get(RAW + urllib.parse.quote(path))
        with open(dst, "wb") as f:
            f.write(data)
        return 1

    with ThreadPoolExecutor(16) as ex:
        return sum(ex.map(one, want))


def read_metadata(path):
    """The few fields we use from metadata.yaml, without a YAML dependency:
    the top-level scalars, and each model's display name."""
    meta, models = {}, []
    with open(path) as f:
        for line in f:
            key, sep, value = line.strip().lstrip("- ").partition(":")
            value = value.strip().strip('"')
            if not sep or not value:
                continue
            if key == "model_display_name":
                models.append(value)
            elif not line.startswith(" "):
                meta[key] = value
    meta["models"] = models
    return meta


def describe(folder, sub):
    """Returns (name, source, notes) for an ingest of the downloaded folder."""
    meta = read_metadata(os.path.join(folder, "metadata.yaml"))
    jobs = sorted(d for d in os.listdir(folder) if os.path.isfile(os.path.join(folder, d, "config.json")))
    efforts, envs, attempts, starts = set(), set(), 0, []
    for j in jobs:
        with open(os.path.join(folder, j, "config.json")) as f:
            cfg = json.load(f)
        for a in cfg.get("agents") or []:
            efforts.add((a.get("kwargs") or {}).get("reasoning_effort") or "default")
        envs.add((cfg.get("environment") or {}).get("type") or "?")
        attempts += cfg.get("n_attempts") or 1
        for t in os.listdir(os.path.join(folder, j)):
            r = os.path.join(folder, j, t, "result.json")
            if os.path.isfile(r):
                with open(r) as f:
                    if s := json.load(f).get("started_at"):
                        starts.append(s)
    first = min(starts) if starts else "unknown"
    name = f"ref-{sub.lower().replace('__', '-').replace('_', '-')}-{first[:7]}"
    agent = meta.get("agent_display_name") or sub.split("__")[0]
    org = meta.get("agent_org_display_name")
    notes = (f"Published Terminal-Bench 2.0 leaderboard submission. Agent {agent}"
             + (f" ({org})" if org else "")
             + f". Model {', '.join(meta['models']) or '?'}, reasoning_effort {', '.join(sorted(efforts))}."
             + f" Run on {', '.join(sorted(envs))} sandboxes from {first[:10]}."
             + f" {len(jobs)} job(s), {attempts} attempt(s) per task."
             + " Imported with config.json and each trial's result.json only.")
    return name, PAGE + f"{ROOT}/{sub}", notes


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("submission")
    p.add_argument("--out", default=os.path.join(tbench_matrix.REPO, "..", "benchmarks", "refs"))
    a = p.parse_args(argv)
    folder = os.path.join(a.out, a.submission)
    n = fetch(a.submission, folder)
    print(f"fetched {n} new file(s) into {folder}", file=sys.stderr)
    name, source, notes = describe(folder, a.submission)
    written = tbench_matrix.ingest(folder, source=source, notes=notes, name=name)
    print(f"wrote {name}.json" if written else f"{name}.json is already there, nothing new", file=sys.stderr)


if __name__ == "__main__":
    main()
