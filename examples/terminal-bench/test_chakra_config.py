import unittest

from chakra_config import build


class BuildTest(unittest.TestCase):
    def test_uses_the_responses_api(self):
        config, _ = build("gpt-5.5", "https://api.openai.com/v1", "medium")
        conns = config["connections"]
        self.assertNotIn("model", config)
        conn = conns["connections"][conns["active"]]
        self.assertEqual(conn["type"], "openai-responses")
        self.assertEqual(conn["model"], "gpt-5.5")
        self.assertEqual(conn["baseUrl"], "https://api.openai.com/v1")

    def test_effort_reaches_the_connection(self):
        config, _ = build("gpt-5.5", "https://api.openai.com/v1", "high")
        conns = config["connections"]
        self.assertEqual(conns["connections"][conns["active"]]["reasoningEffort"], "high")

    def test_no_effort_leaves_the_default(self):
        config, _ = build("gpt-5.5", "https://api.openai.com/v1", None)
        conns = config["connections"]
        self.assertNotIn("reasoningEffort", conns["connections"][conns["active"]])

    def test_detach_stays_on(self):
        _, workspace = build("gpt-5.5", "https://api.openai.com/v1", None)
        self.assertEqual(workspace["exec"]["shell"]["processes"], {"detach": True})


if __name__ == "__main__":
    unittest.main()
