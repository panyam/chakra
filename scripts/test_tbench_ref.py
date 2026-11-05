import json
import os
import tempfile
import unittest

import tbench_ref

META = """agent_url: https://example.com
agent_display_name: "Terminus 2"
agent_org_display_name: "Terminal Bench"

models:
  - model_name: "gpt-5.3-codex"
    model_provider: "openai"
    model_display_name: "GPT-5.3-Codex"
"""


def fake_hub(files):
    """A get() over a dict of submission paths, answering tree listings the
    way the Hugging Face API does."""
    def get(url):
        if url.startswith(tbench_ref.API):
            path = url[len(tbench_ref.API):]
            kids = {}
            for f in files:
                if f.startswith(path + "/"):
                    head = f[len(path) + 1:].split("/")[0]
                    full = f"{path}/{head}"
                    kids[full] = "file" if full in files else "directory"
            return json.dumps([{"type": t, "path": p} for p, t in sorted(kids.items())]).encode()
        return files[url[len(tbench_ref.RAW):]]
    return get


class FetchTest(unittest.TestCase):
    def files(self):
        base = f"{tbench_ref.ROOT}/Terminus2__GPT-5.3-Codex"
        cfg = {"agents": [{"name": "terminus-2", "model_name": "openai/gpt-5.3-codex", "kwargs": {"reasoning_effort": "xhigh"}}],
               "environment": {"type": "daytona"}, "n_attempts": 5}
        res = {"task_name": "fix-git", "agent_info": {"name": "terminus-2"}, "started_at": "2026-02-05T10:00:00Z",
               "verifier_result": {"rewards": {"reward": 1.0}}, "agent_result": {"n_input_tokens": 10}}
        return {
            f"{base}/metadata.yaml": META.encode(),
            f"{base}/2026-02-05__10-00-00/config.json": json.dumps(cfg).encode(),
            f"{base}/2026-02-05__10-00-00/fix-git__a/result.json": json.dumps(res).encode(),
            f"{base}/2026-02-05__10-00-00/fix-git__a/agent/trajectory.json": b"{}",
        }

    def test_downloads_only_what_ingest_reads_and_skips_what_it_has(self):
        with tempfile.TemporaryDirectory() as out:
            get = fake_hub(self.files())
            self.assertEqual(tbench_ref.fetch("Terminus2__GPT-5.3-Codex", out, get), 3)
            self.assertFalse(os.path.exists(os.path.join(out, "2026-02-05__10-00-00", "fix-git__a", "agent")))
            self.assertEqual(tbench_ref.fetch("Terminus2__GPT-5.3-Codex", out, get), 0)

    def test_names_and_notes_come_from_the_metadata_and_configs(self):
        with tempfile.TemporaryDirectory() as out:
            tbench_ref.fetch("Terminus2__GPT-5.3-Codex", out, fake_hub(self.files()))
            name, source, notes = tbench_ref.describe(out, "Terminus2__GPT-5.3-Codex")
            self.assertEqual(name, "ref-terminus2-gpt-5.3-codex-2026-02")
            self.assertTrue(source.endswith("/submissions/terminal-bench/2.0/Terminus2__GPT-5.3-Codex"))
            for part in ("Agent Terminus 2 (Terminal Bench)", "Model GPT-5.3-Codex", "reasoning_effort xhigh",
                         "daytona sandboxes from 2026-02-05", "1 job(s), 5 attempt(s) per task"):
                self.assertIn(part, notes)


if __name__ == "__main__":
    unittest.main()
