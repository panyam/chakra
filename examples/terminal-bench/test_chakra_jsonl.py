import json
import unittest

from chakra_jsonl import summarize


def frame(kind, payload):
    return json.dumps({"kind": kind, "payload": payload})


def event(kind, **kw):
    return frame("runner-event", {"Kind": "runner-event", "RunnerEvent": {"kind": kind, **kw}})


def step(n, inp, cached, out):
    return event("step", step=n, usage={"inputTokens": inp, "cachedInputTokens": cached, "outputTokens": out})


LOG = [
    frame("user-input", {"Text": "fix it"}),
    event("turn-begin"),
    step(1, 1000, 800, 50),
    event("tool-begin", toolCall={"name": "shell"}),
    event("tool-end"),
    frame("ask-denied", {"message": "rm -rf"}),
    step(2, 1500, 1200, 70),
    event("tool-begin", toolCall={"name": "apply_patch"}),
]


class SummarizeTest(unittest.TestCase):
    def test_turn_done_wins_when_the_turn_finished(self):
        done = frame("turn-done", {"Result": {"steps": 3, "usage": {
            "inputTokens": 4000, "cachedInputTokens": 3000, "outputTokens": 200}}})
        got = summarize(LOG + [event("tool-end"), step(3, 1500, 1000, 80), done])
        self.assertEqual(got, {"steps": 3, "input": 4000, "cached": 3000, "output": 200,
                               "tool_calls": 2, "asks_denied": 1, "usage_partial": False})

    def test_a_killed_run_sums_its_finished_steps(self):
        got = summarize(LOG)
        self.assertEqual(got, {"steps": 2, "input": 2500, "cached": 2000, "output": 120,
                               "tool_calls": 2, "asks_denied": 1, "usage_partial": True})

    def test_a_log_without_steps_reports_no_usage(self):
        got = summarize([event("turn-begin"), event("thinking-delta", text="hm"), "not json"])
        self.assertEqual(got, {"steps": 0, "input": None, "cached": None, "output": None,
                               "tool_calls": 0, "asks_denied": 0, "usage_partial": False})


if __name__ == "__main__":
    unittest.main()
