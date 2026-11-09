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

    def test_the_plan_and_a_facts_slot_are_on(self):
        _, workspace = build("gpt-5.5", "https://api.openai.com/v1", None)
        self.assertTrue(workspace["plan"])
        self.assertEqual([s["name"] for s in workspace["slots"]], ["facts"])
        self.assertEqual(workspace["slots"][0]["mode"], "append")


class AnthropicBuildTest(unittest.TestCase):
    def test_a_claude_model_gets_a_messages_api_connection(self):
        config, _ = build("claude-sonnet-5-5", None, "high", "anthropic")
        conns = config["connections"]
        conn = conns["connections"][conns["active"]]
        self.assertEqual(conn, {"type": "anthropic", "model": "claude-sonnet-5-5",
                                "apiKeyEnv": "ANTHROPIC_API_KEY", "maxTokens": 64000})

    def test_a_base_url_is_kept_when_harbor_routes_one(self):
        config, _ = build("claude-sonnet-5-5", "https://proxy.example/anthropic", None, "anthropic")
        conns = config["connections"]
        self.assertEqual(conns["connections"][conns["active"]]["baseUrl"], "https://proxy.example/anthropic")

    def test_openai_still_defaults_its_base_url(self):
        config, _ = build("gpt-5.5", None, "medium")
        conns = config["connections"]
        self.assertEqual(conns["connections"][conns["active"]]["baseUrl"], "https://api.openai.com/v1")

    def test_an_unknown_provider_is_refused(self):
        with self.assertRaises(ValueError):
            build("llama", None, None, "ollama")


if __name__ == "__main__":
    unittest.main()
