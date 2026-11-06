import json
import os
import subprocess
import tempfile
import unittest

import tbench_matrix as m


def trial(job_dir, task, agent, reward=1.0, version=None, started="2026-11-03T10:00:00Z", tokens=(100_000, 50_000, 10_000)):
    d = os.path.join(job_dir, f"{task}__{len(os.listdir(job_dir))}")
    os.makedirs(d)
    tin, cached, out = tokens
    with open(os.path.join(d, "result.json"), "w") as f:
        json.dump({
            "task_name": f"terminal-bench/{task}",
            "agent_info": {"name": agent, "version": version or "unknown"},
            "verifier_result": {"rewards": {"reward": reward}},
            "agent_result": {"n_input_tokens": tin, "n_cache_tokens": cached, "n_output_tokens": out},
            "agent_execution": {"started_at": started, "finished_at": started.replace("10:00:00", "10:01:00")},
            "started_at": started,
        }, f)


def job(root, name, agent="chakra", effort="medium", model="openai/gpt-5.5"):
    d = os.path.join(root, name)
    os.makedirs(d)
    kwargs = {"reasoning_effort": effort} if effort else {}
    with open(os.path.join(d, "config.json"), "w") as f:
        json.dump({"job_name": name, "agents": [{"name": agent, "model_name": model, "kwargs": kwargs}]}, f)
    return d


def tasks_dir(root, names):
    d = os.path.join(root, "tasks")
    for n in names:
        os.makedirs(os.path.join(d, n))
        open(os.path.join(d, n, "task.toml"), "w").close()
    return d


def git_repo(root):
    """A repo with one .go commit, then one .go and one doc commit after it.
    Returns (repo, sha of the first commit)."""
    repo = os.path.join(root, "repo")
    os.makedirs(repo)
    run = lambda *a: subprocess.run(["git", *a], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()
    run("init", "-q")
    run("config", "user.email", "t@example.com")
    run("config", "user.name", "t")
    for name, date in [("a.go", "2026-11-01T00:00:00Z"), ("b.go", "2026-11-05T00:00:00Z"), ("README.md", "2026-11-06T00:00:00Z")]:
        with open(os.path.join(repo, name), "w") as f:
            f.write("x\n")
        run("add", name)
        subprocess.run(["git", "commit", "-q", "-m", name], cwd=repo, check=True,
                       env={**os.environ, "GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": date})
    first = run("rev-list", "--max-parents=0", "HEAD")
    return repo, first


class SetupKeyTest(unittest.TestCase):
    def test_missing_effort_reads_as_default(self):
        self.assertEqual(m.setup_key("codex", "openai/gpt-5.5", None), "codex/openai-gpt-5.5/default")
        self.assertEqual(m.setup_key("chakra", "openai/gpt-5.5", "medium"), "chakra/openai-gpt-5.5/medium")

    def test_round_trips_to_make_variables(self):
        self.assertEqual(m.parse_setup_key("chakra/openai-gpt-5.5/medium"), ("chakra", "openai/gpt-5.5", "medium"))
        self.assertEqual(m.parse_setup_key("terminus-2/anthropic-claude-opus-4-6/default"),
                         ("terminus-2", "anthropic/claude-opus-4-6", "default"))


class IngestTest(unittest.TestCase):
    def test_one_record_per_trial_and_rerun_writes_nothing(self):
        with tempfile.TemporaryDirectory() as root:
            jobs, results = os.path.join(root, "jobs"), os.path.join(root, "results")
            j = job(jobs, "tbench-chakra-20261103-100000")
            trial(j, "fix-git", "chakra", version="abc123")
            trial(j, "fix-git", "chakra", reward=0.0, version="abc123")
            trial(j, "kv-store-grpc", "chakra", version="abc123")
            written = m.ingest(jobs, results=results)
            self.assertEqual(len(written), 1)
            with open(written[0]) as f:
                first = f.read()
            doc = json.loads(first)
            self.assertEqual(doc["source"], "local")
            self.assertEqual(len(doc["trials"]), 3)
            t = doc["trials"][0]
            self.assertEqual(t["task"], "fix-git")
            self.assertEqual(t["setup"], "chakra/openai-gpt-5.5/medium")
            self.assertEqual(t["chakra_sha"], "abc123")
            self.assertEqual(t["job"], "tbench-chakra-20261103-100000")
            self.assertEqual(t["date"], "2026-11-03T10:00:00Z")
            self.assertEqual((t["in"], t["cached"], t["out"], t["agent_s"]), (100_000, 50_000, 10_000, 60.0))

            self.assertEqual(m.ingest(jobs, results=results), [])
            with open(written[0]) as f:
                self.assertEqual(f.read(), first)

    def test_a_timed_out_trial_keeps_its_partial_usage_flag(self):
        with tempfile.TemporaryDirectory() as root:
            jobs, results = os.path.join(root, "jobs"), os.path.join(root, "results")
            j = job(jobs, "tbench-chakra-1")
            trial(j, "fix-git", "chakra", version="abc123")
            trial(j, "kv-store-grpc", "chakra", version="abc123")
            path = os.path.join(j, sorted(os.listdir(j))[-1], "result.json")
            with open(path) as f:
                r = json.load(f)
            r["agent_result"]["metadata"] = {"usage_partial": True}
            r["exception_info"] = {"exception_type": "AgentTimeoutError"}
            with open(path, "w") as f:
                json.dump(r, f)
            m.ingest(jobs, results=results)
            by = {t["task"]: t for t in m.load(results)}
            self.assertNotIn("usage_partial", by["fix-git"])
            self.assertTrue(by["kv-store-grpc"]["usage_partial"])
            self.assertEqual(by["kv-store-grpc"]["in"], 100_000)

    def test_infers_sha_from_date_when_the_build_was_not_stamped(self):
        with tempfile.TemporaryDirectory() as root:
            repo, first = git_repo(root)
            jobs, results = os.path.join(root, "jobs"), os.path.join(root, "results")
            trial(job(jobs, "tbench-chakra-1"), "fix-git", "chakra", started="2026-11-03T10:00:00Z")
            trial(job(jobs, "tbench-codex-1", agent="codex"), "fix-git", "codex", version="0.166.0")
            m.ingest(jobs, results=results, cwd=repo)
            by = {t["setup"].split("/")[0]: t for t in m.load(results)}
            self.assertEqual(by["chakra"]["chakra_sha"], first[:12])
            self.assertTrue(by["chakra"]["sha_inferred"])
            self.assertIsNone(by["codex"]["chakra_sha"])
            self.assertEqual(by["codex"]["agent_version"], "0.166.0")

    def test_published_jobs_go_into_one_named_file_with_their_source(self):
        with tempfile.TemporaryDirectory() as root:
            jobs, results = os.path.join(root, "ref"), os.path.join(root, "results")
            trial(job(jobs, "2026-02-05__16-08-28", agent="terminus-2", effort=None, model="anthropic/claude-opus-4-6"),
                  "fix-git", "terminus-2")
            trial(job(jobs, "2026-02-05__17-41-47", agent="terminus-2", effort=None, model="anthropic/claude-opus-4-6"),
                  "fix-git", "terminus-2", reward=0.0)
            with self.assertRaises(SystemExit):
                m.ingest(jobs, results=results, source="https://example.com/sub")
            [path] = m.ingest(jobs, results=results, source="https://example.com/sub", notes="as published", name="ref-x")
            self.assertEqual(os.path.basename(path), "ref-x.json")
            doc = m.load_file(path)
            self.assertEqual((doc["source"], doc["notes"], len(doc["jobs"]), len(doc["trials"])),
                             ("https://example.com/sub", "as published", 2, 2))
            self.assertIsNone(doc["trials"][0]["chakra_sha"])
            self.assertEqual(m.ingest(jobs, results=results, source="https://example.com/sub", name="ref-x"), [])


class RenderTest(unittest.TestCase):
    def fixture(self, root, repo_sha):
        jobs, results = os.path.join(root, "jobs"), os.path.join(root, "results")
        j = job(jobs, "tbench-chakra-1")
        for reward in (1.0, 1.0, 0.0):
            trial(j, "fix-git", "chakra", reward=reward, version=repo_sha, started="2026-11-03T10:00:00Z")
        trial(job(jobs, "tbench-codex-1", agent="codex"), "fix-git", "codex", started="2026-11-02T10:00:00Z")
        m.ingest(jobs, results=results)
        return m.load(results), ["fix-git", "never-ran"]

    def test_cells_show_tally_date_and_commits_only_for_chakra(self):
        with tempfile.TemporaryDirectory() as root:
            repo, first = git_repo(root)
            trials, tasks = self.fixture(root, first)
            md = m.render_md(trials, tasks, cwd=repo)
            self.assertIn("| fix-git | 2/3 · Nov 3 · +1c | 1/1 · Nov 2 |", md)
            self.assertIn("| never-ran |  |  |", md)
            self.assertIn("| chakra/openai-gpt-5.5/medium | 2/3 | 1/2 |", md)

    def test_a_dirty_build_counts_from_its_base_commit(self):
        with tempfile.TemporaryDirectory() as root:
            repo, first = git_repo(root)
            self.assertEqual(m.commits_since(first + "-dirty", cwd=repo), 1)

    def test_html_marks_never_run_cells_and_shades_published_columns(self):
        with tempfile.TemporaryDirectory() as root:
            repo, first = git_repo(root)
            trials, tasks = self.fixture(root, first)
            trials.append({"task": "fix-git", "setup": "terminus-2/anthropic-claude-opus-4-6/default", "date": "2026-02-05T00:00:00Z",
                           "reward": 1.0, "error": None, "source": "https://example.com/sub"})
            page = m.render_html(trials, tasks, cwd=repo)
            self.assertIn('<td class="part">2/3 · Nov 3 · +1c</td>', page)
            self.assertIn('<td class="never">·</td>', page)
            self.assertIn('<td class="pass ref">1/1 · Feb 5</td>', page)
            self.assertIn("from https://example.com/sub", page)


def rec(task, setup, date=None, tokens=(100_000, 0, 0), source="local"):
    tin, cached, out = tokens
    return {"task": task, "setup": setup, "date": date, "reward": 1.0, "error": None,
            "in": tin, "cached": cached, "out": out, "source": source}


CHAKRA = "chakra/openai-gpt-5.5/medium"
CODEX = "codex/openai-gpt-5.5/medium"


class ReportTest(unittest.TestCase):
    def test_shows_each_changed_cell_before_and_after_and_the_batch_cost(self):
        since = "2026-11-05T00:00:00Z"
        trials = [
            {**rec("a", CHAKRA, "2026-11-03T00:00:00Z"), "reward": 0.0},
            rec("a", CHAKRA, "2026-11-05T01:00:00Z", tokens=(100_000, 0, 0)),
            rec("b", CODEX, "2026-11-05T02:00:00Z", tokens=(200_000, 0, 10_000)),
            rec("c", CODEX, "2026-11-01T00:00:00Z"),
            rec("a", "terminus-2/openai-gpt-5.3-codex/xhigh", "2026-11-06T00:00:00Z", source="https://x"),
        ]
        out = m.report(trials, ["a", "b", "c"], since, [CHAKRA, CODEX])
        self.assertIn(f"| {CHAKRA} | 1/2 | 1/3 |", out)
        self.assertIn(f"| {CODEX} | 2/2 | 2/3 |", out)
        self.assertIn(f"| a | {CHAKRA} | 0/1 | 1/2 |", out)
        self.assertIn(f"| b | {CODEX} | never run | 1/1 |", out)
        self.assertNotIn("| c |", out)
        self.assertNotIn("terminus", out)
        # $0.50 for a, $1.00 + $0.30 for b.
        self.assertIn("2 trials, $1.80 at list price.", out)

    def test_says_the_cost_is_a_lower_bound_when_usage_is_missing_or_partial(self):
        since = "2026-11-05T00:00:00Z"
        trials = [
            {**rec("a", CHAKRA, "2026-11-05T01:00:00Z"), "in": None, "error": "AgentTimeoutError"},
            {**rec("b", CHAKRA, "2026-11-05T01:00:00Z"), "usage_partial": True},
        ]
        out = m.report(trials, ["a", "b"], since)
        self.assertIn("2 trials, $0.50 at list price (1 without token counts, "
                      "1 cut off by the time limit, counted up to the cut, so this is a lower bound).", out)


class StaleTest(unittest.TestCase):
    def test_never_run_first_then_oldest_and_no_published_columns(self):
        trials = [
            rec("a", CHAKRA, "2026-11-03T00:00:00Z"),
            rec("b", CHAKRA, "2026-11-01T00:00:00Z"),
            rec("a", "terminus-2/anthropic-claude-opus-4-6/default", "2026-01-01T00:00:00Z", source="https://x"),
        ]
        picks, _ = m.stale(trials, ["a", "b", "c"], 10, 100)
        self.assertEqual([(t, s) for t, s, _ in picks], [("c", CHAKRA), ("b", CHAKRA), ("a", CHAKRA)])

    def test_setups_filter_takes_a_column_with_no_runs(self):
        trials = [rec("a", CHAKRA, "2026-11-03T00:00:00Z")]
        picks, _ = m.stale(trials, ["a"], 10, 100, setups=[CHAKRA, CODEX])
        self.assertEqual([(t, s) for t, s, _ in picks], [("a", CODEX), ("a", CHAKRA)])

    def test_n_caps_the_picks(self):
        picks, _ = m.stale([rec("a", CHAKRA, "2026-11-03T00:00:00Z")], ["a", "b", "c"], 2, 100)
        self.assertEqual(len(picks), 2)

    def test_stops_before_the_cell_that_passes_the_budget(self):
        # 100k uncached input at $5/M is $0.50 a trial.
        trials = [rec(t, CHAKRA, "2026-11-0%dT00:00:00Z" % i) for i, t in enumerate("abc", 1)]
        picks, total = m.stale(trials, ["a", "b", "c"], 10, 1.2)
        self.assertEqual([t for t, _, _ in picks], ["a", "b"])
        self.assertAlmostEqual(total, 1.0)

    def test_estimate_prefers_this_setup_then_any_setup_then_the_median(self):
        trials = [
            rec("a", CHAKRA, tokens=(200_000, 0, 0)),
            rec("a", CODEX, tokens=(400_000, 0, 0)),
            rec("b", CODEX, tokens=(600_000, 0, 0)),
            rec("z", CODEX, tokens=(1_000_000, 1_000_000, 100_000), source="https://x"),
        ]
        self.assertAlmostEqual(m.estimate("a", CHAKRA, trials), 1.0)
        # Chakra spends half what Codex does on a, so Codex's $3.00 on b
        # scales to $1.50.
        self.assertAlmostEqual(m.estimate("b", CHAKRA, trials), 1.5)
        self.assertAlmostEqual(m.estimate("never", CHAKRA, trials), 2.0)

    def test_a_never_run_task_heavy_in_a_reference_costs_more_than_the_median(self):
        ref = "terminus-2/openai-gpt-5.3-codex/xhigh"
        trials = [
            rec("a", CHAKRA, tokens=(100_000, 0, 0)),
            rec("b", CHAKRA, tokens=(200_000, 0, 0)),
            rec("a", ref, tokens=(200_000, 0, 0), source="https://x"),
            rec("b", ref, tokens=(400_000, 0, 0), source="https://x"),
            rec("heavy", ref, tokens=(4_000_000, 0, 0), source="https://x"),
        ]
        # We spend half what the reference does on shared tasks, so heavy is
        # 4M x $5/M x 0.5.
        self.assertAlmostEqual(m.estimate("heavy", CHAKRA, trials), 10.0)
        self.assertGreater(m.estimate("heavy", CHAKRA, trials), m.estimate("never", CHAKRA, trials))

    def test_reference_scaling_uses_only_tasks_both_have_run(self):
        ref = "terminus-2/openai-gpt-5.3-codex/xhigh"
        trials = [
            rec("a", CHAKRA, tokens=(100_000, 0, 0)),
            rec("ours-only", CHAKRA, tokens=(9_000_000, 0, 0)),
            rec("a", ref, tokens=(100_000, 0, 0), source="https://x"),
            rec("heavy", ref, tokens=(1_000_000, 0, 0), source="https://x"),
        ]
        self.assertAlmostEqual(m.estimate("heavy", CHAKRA, trials), 5.0)

    def test_a_reference_with_no_shared_task_is_not_used(self):
        trials = [
            rec("a", CHAKRA, tokens=(100_000, 0, 0)),
            rec("heavy", "terminus-2/openai-gpt-5.3-codex/xhigh", tokens=(4_000_000, 0, 0), source="https://x"),
        ]
        self.assertAlmostEqual(m.estimate("heavy", CHAKRA, trials), 0.5)

    def test_another_setup_counts_unscaled_until_the_two_share_a_task(self):
        trials = [rec("a", CHAKRA, tokens=(200_000, 0, 0)), rec("b", CODEX, tokens=(600_000, 0, 0))]
        self.assertAlmostEqual(m.estimate("b", CHAKRA, trials), 3.0)

    def test_cached_and_output_tokens_use_their_own_prices(self):
        t = rec("a", CHAKRA, tokens=(1_000_000, 400_000, 100_000))
        self.assertAlmostEqual(m.trial_cost(t, m.PRICES["openai/gpt-5.5"]), 0.6 * 5 + 0.4 * 0.5 + 0.1 * 30)

    def test_unpriced_model_is_an_error_not_free(self):
        with self.assertRaises(SystemExit):
            m.stale([rec("a", "chakra/openai-gpt-9/medium")], ["a", "b"], 10, 100)

    def test_one_command_per_column(self):
        picks = [("a", CHAKRA, 1), ("b", CODEX, 1), ("c", CHAKRA, 1), ("d", "codex/openai-gpt-5.5/default", 1)]
        self.assertEqual(m.commands(picks), [
            "make tbench TB_AGENTS=chakra TB_MODEL=openai/gpt-5.5 TB_EFFORT=medium 'TB_TASKS=a c'",
            "make tbench TB_AGENTS=codex TB_MODEL=openai/gpt-5.5 TB_EFFORT=medium TB_TASKS=b",
            "make tbench TB_AGENTS=codex TB_MODEL=openai/gpt-5.5 TB_EFFORT= TB_TASKS=d",
        ])


if __name__ == "__main__":
    unittest.main()
