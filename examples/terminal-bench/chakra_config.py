"""The agentchat config chakra_agent.py uploads into a task container (#195).

It lives apart from chakra_agent.py so it can be tested without Harbor
installed. For an OpenAI model the connection speaks the Responses API, as
Codex does, so the two agents send the same kind of request. For a Claude
model it speaks the Messages API, as Claude Code does (#264).
"""

INSTRUCTIONS = (
    "You are a coding agent working in a terminal environment. "
    "Complete the task fully, then say briefly what you did."
)

# Turns end on the task's time budget, as Codex's do. Chakra's default cap of
# eight model calls would end most terminal tasks early.
MAX_STEPS = 400

# The Messages API needs a cap on every reply. Chakra's default of 4096 cuts
# a long file write short. Claude's current models allow 128K, and a
# streaming client can ask for much more than 4096.
ANTHROPIC_MAX_TOKENS = 64000


def build(model, base_url, effort, provider="openai"):
    """Returns (config, workspace) for `agentchat exec`.

    provider is "openai" or "anthropic". For OpenAI, effort is the Responses
    API's reasoning.effort ("low", "medium", "high"), or None to leave the
    field out and take the API's default. Chakra's Anthropic provider sends no
    thinking or effort fields, so for Anthropic effort is not sent. A current
    Claude model then thinks adaptively at the API's default effort, which is
    "high" on Sonnet 5.5.
    """
    if provider == "anthropic":
        conn = {"type": "anthropic", "model": model, "apiKeyEnv": "ANTHROPIC_API_KEY",
                "maxTokens": ANTHROPIC_MAX_TOKENS}
        if base_url:
            conn["baseUrl"] = base_url
    elif provider == "openai":
        conn = {"type": "openai-responses", "baseUrl": base_url or "https://api.openai.com/v1", "model": model,
                "apiKeyEnv": "OPENAI_API_KEY"}
        if effort:
            conn["reasoningEffort"] = effort
    else:
        raise ValueError(f"chakra's Terminal-Bench adapter has no connection for provider {provider!r}")
    config = {
        "connections": {"active": provider, "connections": {provider: conn}},
        "instructions": INSTRUCTIONS,
        "maxSteps": MAX_STEPS,
        "approval": {"mode": "allow"},
    }
    workspace = {
        "exec": {"sandbox": "unconfined", "shell": {"timeout": "10m", "rules": [], "processes": {"detach": True}}},
        "skills": {"dirs": []},
    }
    return config, workspace
