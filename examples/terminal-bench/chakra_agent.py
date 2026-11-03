"""Chakra as a Harbor installed agent, for Terminal-Bench (#195).

Harbor copies a static Linux build of agentchat into the task container and
runs one headless task there with `agentchat exec`, the same way its built-in
Codex agent runs `codex exec`. The container is the sandbox, so chakra runs
with approvals off and its exec sandbox unconfined, matching Codex's
--dangerously-bypass-approvals-and-sandbox.

Run it with `make tbench` from the repo root, or directly:

    PYTHONPATH=examples/terminal-bench harbor run \
        -d terminal-bench/terminal-bench-2 -a chakra_agent:ChakraAgent -m openai/gpt-5.5
"""

import json
import os
import shlex
from pathlib import Path

from harbor.agents.agent_auth_resolution import ModelConnectionSpec, ProviderAccess
from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.model_api_format import ModelApiFormat

_HERE = Path(__file__).resolve().parent

# Where things go inside the container.
_BIN = "/usr/local/bin/agentchat"
_HOME = "/tmp/chakra"
_OUTPUT = "chakra.jsonl"
_STDERR = "chakra.stderr.txt"

# Turns end on the task's time budget, as Codex's do; chakra's default cap of
# eight model calls would end most terminal tasks early.
_MAX_STEPS = 400


class ChakraAgent(BaseInstalledAgent):
    """Runs `agentchat exec` inside the task container."""

    MODEL_CONNECTION = ModelConnectionSpec(
        default_provider="openai",
        agent_provider_access=ProviderAccess(
            api_key_candidate_names=["OPENAI_API_KEY"],
            base_url_candidate_names=["OPENAI_BASE_URL"],
        ),
        supported_api_formats=[ModelApiFormat.OPENAI_CHAT_COMPLETIONS],
        default_api_format_by_provider={"default": ModelApiFormat.OPENAI_CHAT_COMPLETIONS},
    )

    @staticmethod
    def name() -> str:
        return "chakra"

    def _binary(self) -> Path:
        path = Path(os.environ.get("CHAKRA_AGENTCHAT_BIN", _HERE / "bin" / "agentchat-linux-amd64"))
        if not path.is_file():
            raise FileNotFoundError(
                f"no agentchat build at {path}; run `make tbench-build` or set CHAKRA_AGENTCHAT_BIN"
            )
        return path

    async def install(self, environment: BaseEnvironment) -> None:
        await environment.upload_file(self._binary(), _BIN)
        await self.exec_as_root(environment, command=f"chmod 755 {_BIN}")

    async def configure(self, environment: BaseEnvironment) -> None:
        conn = self.model_connection
        config = {
            "model": {
                "baseUrl": conn.execution_base_url or "https://api.openai.com/v1",
                "model": conn.model_id or self.model_name,
                "apiKeyEnv": "OPENAI_API_KEY",
            },
            "instructions": (
                "You are a coding agent working in a terminal environment. "
                "Complete the task fully, then say briefly what you did."
            ),
            "maxSteps": _MAX_STEPS,
            "approval": {"mode": "allow"},
        }
        workspace = {
            "exec": {"sandbox": "unconfined", "shell": {"timeout": "10m", "rules": []}},
            "skills": {"dirs": []},
        }
        await self.exec_as_agent(environment, command=f"mkdir -p {_HOME}")
        await self._upload_config_text(
            environment, content=json.dumps(config), remote_path=f"{_HOME}/config.json", filename="config.json"
        )
        await self._upload_config_text(
            environment, content=json.dumps(workspace), remote_path=f"{_HOME}/workspace.json", filename="workspace.json"
        )

    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        conn = self.model_connection
        env = dict(conn.env)
        if conn.api_key:
            env["OPENAI_API_KEY"] = conn.api_key
        logs = self.environment_logs_dir.as_posix()
        await self.exec_as_agent(
            environment,
            command=(
                f"mkdir -p {logs} && "
                f"{_BIN} exec --config {_HOME}/config.json "
                f'--workspace "$PWD" --workspace-config {_HOME}/workspace.json '
                f"--checkpoint-store {_HOME}/checkpoints --approve allow --json "
                f"-p {shlex.quote(instruction)} "
                f"</dev/null >{logs}/{_OUTPUT} 2>{logs}/{_STDERR}"
            ),
            env=env,
        )

    def populate_context_post_run(self, context: AgentContext) -> None:
        """Reads the turn's usage from the JSON lines exec wrote."""
        path = self.logs_dir / _OUTPUT
        if not path.is_file():
            return
        usage, steps, tools, denied = {}, 0, 0, 0
        for line in path.read_text().splitlines():
            try:
                frame = json.loads(line)
            except json.JSONDecodeError:
                continue
            kind, payload = frame.get("kind"), frame.get("payload") or {}
            if kind == "turn-done":
                result = payload.get("Result") or {}
                usage = result.get("usage") or {}
                steps = result.get("steps") or 0
            elif kind == "runner-event" and (payload.get("RunnerEvent") or {}).get("kind") == "tool-begin":
                tools += 1
            elif kind == "ask-denied":
                denied += 1
        context.n_input_tokens = usage.get("inputTokens")
        context.n_cache_tokens = usage.get("cachedInputTokens")
        context.n_output_tokens = usage.get("outputTokens")
        context.metadata = {**(context.metadata or {}), "steps": steps, "tool_calls": tools, "asks_denied": denied}
