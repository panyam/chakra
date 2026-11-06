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

from harbor.agents.agent_auth_resolution import ModelConnectionSpec
from harbor.agents.capabilities import AgentCapabilities
from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.agents.options import InstalledAgentOptions
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.model_api_format import ModelApiFormat

from chakra_config import build
from chakra_jsonl import summarize

_HERE = Path(__file__).resolve().parent

# Where things go inside the container.
_BIN = "/usr/local/bin/agentchat"
_HOME = "/tmp/chakra"
_OUTPUT = "chakra.jsonl"
_STDERR = "chakra.stderr.txt"

# The env var chakra's config names for each provider's key (chakra_config).
_KEY_ENV = {"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY"}


class ChakraAgent(BaseInstalledAgent):
    """Runs `agentchat exec` inside the task container.

    `--ak reasoning_effort=medium` pins the effort, the same option Harbor's
    Codex agent takes, so `make tbench` can pin both sides with one flag.
    """

    options_model = InstalledAgentOptions
    capabilities = AgentCapabilities(reasoning_effort=True)

    MODEL_CONNECTION = ModelConnectionSpec(
        default_provider="openai",
        supported_api_formats=[ModelApiFormat.OPENAI_RESPONSES, ModelApiFormat.ANTHROPIC_MESSAGES],
        default_api_format_by_provider={
            "default": ModelApiFormat.OPENAI_RESPONSES,
            "anthropic": ModelApiFormat.ANTHROPIC_MESSAGES,
        },
    )

    @staticmethod
    def name() -> str:
        return "chakra"

    def version(self) -> str | None:
        """The chakra commit the agentchat build came from, as `make tbench-build`
        recorded it, so each trial's result names the code it ran."""
        stamp = self._binary().parent / "VERSION"
        try:
            return stamp.read_text().strip() or None
        except OSError:
            return None

    def _provider(self) -> str:
        return "anthropic" if self.model_connection.provider == "anthropic" else "openai"

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
        effort = self.options.reasoning_effort if self.options is not None else None
        config, workspace = build(conn.model_id or self.model_name, conn.execution_base_url, effort, self._provider())
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
            env[_KEY_ENV[self._provider()]] = conn.api_key
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
        """Reads the turn's usage from the JSON lines exec wrote. Harbor calls
        this after a timeout too, and then the usage is a partial sum."""
        path = self.logs_dir / _OUTPUT
        if not path.is_file():
            return
        s = summarize(path.read_text().splitlines())
        context.n_input_tokens = s["input"]
        context.n_cache_tokens = s["cached"]
        context.n_output_tokens = s["output"]
        context.metadata = {
            **(context.metadata or {}),
            "steps": s["steps"],
            "tool_calls": s["tool_calls"],
            "asks_denied": s["asks_denied"],
            "usage_partial": s["usage_partial"],
        }
