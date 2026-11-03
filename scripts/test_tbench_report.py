import io
import json
import os
import tempfile
import unittest
from contextlib import redirect_stdout

import tbench_report


def job(root, name, agent, kwargs):
    d = os.path.join(root, name)
    os.makedirs(os.path.join(d, "fix-git__a"))
    with open(os.path.join(d, "config.json"), "w") as f:
        json.dump({"agents": [{"name": agent, "model_name": "openai/gpt-5.5", "kwargs": kwargs}]}, f)
    with open(os.path.join(d, "fix-git__a", "result.json"), "w") as f:
        json.dump({"task_name": "fix-git", "agent_info": {"name": agent}, "verifier_result": {"rewards": {"reward": 1.0}}}, f)
    return d


class SetupLineTest(unittest.TestCase):
    def test_names_each_jobs_effort(self):
        with tempfile.TemporaryDirectory() as root:
            a = job(root, "tbench-chakra-20261103-100000", "chakra", {"reasoning_effort": "medium"})
            b = job(root, "tbench-codex-20261103-100000", "codex", {})
            out = io.StringIO()
            with redirect_stdout(out):
                tbench_report.report([a, b])
            text = out.getvalue()
            self.assertIn("chakra: openai/gpt-5.5, effort medium", text)
            self.assertIn("codex: openai/gpt-5.5, effort default", text)


if __name__ == "__main__":
    unittest.main()
