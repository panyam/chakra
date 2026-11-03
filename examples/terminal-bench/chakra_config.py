"""The agentchat config chakra_agent.py uploads into a task container (#195).

It lives apart from chakra_agent.py so it can be tested without Harbor
installed. The connection speaks the Responses API, as Codex does, so the two
agents send the same kind of request.
"""

INSTRUCTIONS = (
    "You are a coding agent working in a terminal environment. "
    "Complete the task fully, then say briefly what you did."
)

# Turns end on the task's time budget, as Codex's do. Chakra's default cap of
# eight model calls would end most terminal tasks early.
MAX_STEPS = 400


def build(model, base_url, effort):
    """Returns (config, workspace) for `agentchat exec`.

    effort is the Responses API's reasoning.effort ("low", "medium", "high"),
    or None to leave the field out and take the API's default.
    """
    conn = {"type": "openai-responses", "baseUrl": base_url, "model": model, "apiKeyEnv": "OPENAI_API_KEY"}
    if effort:
        conn["reasoningEffort"] = effort
    config = {
        "connections": {"active": "openai", "connections": {"openai": conn}},
        "instructions": INSTRUCTIONS,
        "maxSteps": MAX_STEPS,
        "approval": {"mode": "allow"},
    }
    workspace = {
        "exec": {"sandbox": "unconfined", "shell": {"timeout": "10m", "rules": [], "processes": {"detach": True}}},
        "skills": {"dirs": []},
    }
    return config, workspace
