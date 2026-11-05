import unittest

import tbench_preflight as p

ENV = {"OPENAI_API_KEY": "sk-test", "ANTHROPIC_API_KEY": "ak-test"}


def answer(status, body=None):
    calls = []

    def send(url, headers, body_sent):
        calls.append((url, headers, body_sent))
        return status, body or {}
    return send, calls


class PreflightTest(unittest.TestCase):
    def test_a_working_key_goes_ahead_with_one_tiny_request(self):
        send, calls = answer(200)
        self.assertEqual(p.check("openai/gpt-5.5", ENV, send), (True, "ok"))
        [(url, headers, body)] = calls
        self.assertEqual(url, "https://api.openai.com/v1/responses")
        self.assertEqual(headers["Authorization"], "Bearer sk-test")
        self.assertEqual((body["model"], body["max_output_tokens"]), ("gpt-5.5", 16))

    def test_anthropic_sends_one_token_with_its_own_headers(self):
        send, calls = answer(200)
        self.assertTrue(p.check("anthropic/claude-sonnet-5-5", ENV, send)[0])
        [(url, headers, body)] = calls
        self.assertEqual(url, "https://api.anthropic.com/v1/messages")
        self.assertEqual((headers["x-api-key"], body["max_tokens"]), ("ak-test", 1))

    def test_an_exhausted_account_stops_the_run(self):
        send, _ = answer(429, {"error": {"code": "insufficient_quota", "message": "You exceeded your current quota"}})
        ok, msg = p.check("openai/gpt-5.5", ENV, send)
        self.assertFalse(ok)
        self.assertIn("out of credit", msg)
        send, _ = answer(400, {"error": {"type": "invalid_request_error", "message": "Your credit balance is too low"}})
        ok, msg = p.check("anthropic/claude-sonnet-5-5", ENV, send)
        self.assertFalse(ok)
        self.assertIn("out of credit", msg)

    def test_a_refused_key_or_unknown_model_stops_the_run(self):
        send, _ = answer(401, {"error": {"code": "invalid_api_key", "message": "Incorrect API key"}})
        self.assertIn("key was refused", p.check("openai/gpt-5.5", ENV, send)[1])
        send, _ = answer(404, {"error": {"code": "model_not_found", "message": "no such model"}})
        self.assertIn("model is unknown", p.check("openai/gpt-nope", ENV, send)[1])

    def test_a_rate_limit_only_warns(self):
        send, _ = answer(429, {"error": {"code": "rate_limit_exceeded", "message": "slow down"}})
        ok, msg = p.check("openai/gpt-5.5", ENV, send)
        self.assertTrue(ok)
        self.assertIn("warning", msg)

    def test_a_missing_key_stops_without_a_request(self):
        send, calls = answer(200)
        ok, msg = p.check("anthropic/claude-sonnet-5-5", {"OPENAI_API_KEY": "x"}, send)
        self.assertEqual((ok, msg, calls), (False, "ANTHROPIC_API_KEY is not set", []))

    def test_an_unknown_provider_goes_ahead_unchecked(self):
        send, calls = answer(500)
        self.assertTrue(p.check("ollama/qwen3", ENV, send)[0])
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
