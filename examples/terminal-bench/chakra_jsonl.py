"""Reads the JSON lines `agentchat exec --json` writes into a trial's numbers.

Kept apart from chakra_agent.py so it can be tested without Harbor installed.
"""

import json


def summarize(lines):
    """Token usage, steps, tool calls and denied asks from one exec log.

    A finished turn's turn-done frame holds the turn's usage, and that wins.
    A run that Harbor killed at its time limit never writes turn-done. Its
    usage is then the sum of the step events, one per finished model call
    (#262), and usage_partial is True. The call in flight when the run was
    killed is not in the sum, so a partial figure is a lower bound.
    """
    done, steps_usage, steps, tools, denied = None, [], 0, 0, 0
    for line in lines:
        try:
            frame = json.loads(line)
        except json.JSONDecodeError:
            continue
        kind, payload = frame.get("kind"), frame.get("payload") or {}
        if kind == "turn-done":
            done = payload.get("Result") or {}
        elif kind == "runner-event":
            event = payload.get("RunnerEvent") or {}
            if event.get("kind") == "tool-begin":
                tools += 1
            elif event.get("kind") == "step":
                steps += 1
                steps_usage.append(event.get("usage") or {})
        elif kind == "ask-denied":
            denied += 1

    out = {"tool_calls": tools, "asks_denied": denied, "usage_partial": False}
    if done is not None:
        usage = done.get("usage") or {}
        out.update(steps=done.get("steps") or 0, input=usage.get("inputTokens"),
                   cached=usage.get("cachedInputTokens"), output=usage.get("outputTokens"))
    elif steps_usage:
        total = lambda k: sum(u.get(k) or 0 for u in steps_usage)
        out.update(steps=steps, input=total("inputTokens"), cached=total("cachedInputTokens"),
                   output=total("outputTokens"), usage_partial=True)
    else:
        out.update(steps=0, input=None, cached=None, output=None)
    return out
