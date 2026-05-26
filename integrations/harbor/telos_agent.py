"""Harbor installed-agent shim for the Go Telos runtime.

Harbor's extension boundary is Python, but the agent being evaluated here is
the `telos` executable. This module installs/fetches the Telos binaries inside
the task container, renders Harbor's task instruction into a local Telos
SPEC.md, and runs `telos run` against the benchmark workspace.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import tempfile
from pathlib import Path
from typing import Any

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

DEFAULT_HARBOR_WORKDIR = "/app"
DEFAULT_AGENT_TIMEOUT_SEC = 0
DEFAULT_POLL_INTERVAL_SEC = 5
DEFAULT_SESSION_TIMEOUT_SEC = 7200
DEFAULT_UNTIL = 3
DEFAULT_TELOS_INSTALL_URL = "https://usetelos.ai/releases/latest/install.sh"
DEFAULT_SPEC_NAME = "harbor-task"
DEFAULT_SKILLS = ("verify-engineering*",)

MODEL_ENV_KEYS = (
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_OAUTH_TOKEN",
    "OPENAI_API_KEY",
    "GEMINI_API_KEY",
    "GOOGLE_API_KEY",
    "GOOGLE_GENERATIVE_AI_API_KEY",
    "OPENROUTER_API_KEY",
    "GITHUB_TOKEN",
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_BEARER_TOKEN_BEDROCK",
    "AWS_REGION",
    "SAIL_API_KEY",
    "SILARES_API_KEY",
)


def render_harbor_spec(
    instruction: str,
    *,
    workdir: str,
    name: str = DEFAULT_SPEC_NAME,
    skills: tuple[str, ...] = DEFAULT_SKILLS,
    extends: str | None = None,
) -> str:
    frontmatter = [
        "---",
        "version: v0",
        f"name: {sanitize_spec_name(name)}",
        "platform: local",
    ]
    if extends:
        frontmatter.append(f"extends: {extends}")
    if skills:
        frontmatter.append("skills:")
        for skill in skills:
            frontmatter.append(f"  - {skill}")
    frontmatter.append("---")

    return (
        "\n".join(frontmatter)
        + f"""

# Spec

Complete this Harbor benchmark task inside the current container environment.

## Runtime

- Harbor's task working directory is `{workdir}`.
- Treat relative paths in the instruction as relative to `{workdir}`.
- The delivered artifact is the task workspace and its runtime behavior.
- The official Harbor benchmark verifier is the final scoreboard. Before
  considering the task complete, run the relevant checks available in the task
  environment.
- Keep scratch/probe files outside the delivered workspace when possible. Use
  `/tmp/telos-scratch` for temporary investigation.

## Task

{instruction.strip()}
"""
    )


def sanitize_spec_name(raw: str) -> str:
    name = re.sub(r"[^a-z0-9-]+", "-", raw.strip().lower())
    name = re.sub(r"-+", "-", name).strip("-")
    if not name or not name[0].isalpha():
        name = f"task-{name}" if name else DEFAULT_SPEC_NAME
    return name[:63].rstrip("-") or DEFAULT_SPEC_NAME


def split_skills(value: str | tuple[str, ...] | list[str] | None) -> tuple[str, ...]:
    if value is None:
        return ()
    if isinstance(value, tuple):
        return value
    if isinstance(value, list):
        return tuple(str(item).strip() for item in value if str(item).strip())
    return tuple(item.strip() for item in re.split(r"[\n,]", value) if item.strip())


def as_bool(value: bool | str) -> bool:
    if isinstance(value, bool):
        return value
    return value.strip().lower() not in {"0", "false", "no", "off"}


def model_env() -> dict[str, str]:
    return {key: os.environ[key] for key in MODEL_ENV_KEYS if os.environ.get(key)}


def parse_marked_json(stdout: str) -> dict[str, Any]:
    body = parse_marked_text(stdout, "TELOS_HARBOR_RESULT_BEGIN", "TELOS_HARBOR_RESULT_END")
    if not body:
        return {}
    return json.loads(body)


def parse_marked_text(stdout: str, start: str, end: str) -> str:
    if start not in stdout or end not in stdout:
        return ""
    body = stdout.split(start, 1)[1].split(end, 1)[0].strip()
    return body


class TelosExecutableAgent(BaseInstalledAgent):
    """Harbor agent that runs Go Telos as the benchmark agent executable."""

    SUPPORTS_WINDOWS = False

    @staticmethod
    def name() -> str:
        return "telos"

    def __init__(
        self,
        *args: Any,
        thinking: str = "medium",
        until: int | str = DEFAULT_UNTIL,
        max_cost_usd: float | str | None = 20.0,
        agent_timeout_sec: int | str = DEFAULT_AGENT_TIMEOUT_SEC,
        session_timeout_sec: int | str = DEFAULT_SESSION_TIMEOUT_SEC,
        poll_interval_sec: int | str = DEFAULT_POLL_INTERVAL_SEC,
        workdir: str | None = None,
        spec_name: str = DEFAULT_SPEC_NAME,
        skills: str | tuple[str, ...] | list[str] | None = DEFAULT_SKILLS,
        install_deps: bool | str = True,
        install_telos: bool | str = True,
        install_pi: bool | str = True,
        inject_pi_models: bool | str = True,
        pi_config_source: str | None = None,
        telos_install_url: str = DEFAULT_TELOS_INSTALL_URL,
        **kwargs: Any,
    ) -> None:
        super().__init__(*args, **kwargs)
        self.thinking = thinking
        self.until = int(until)
        self.max_cost_usd = (
            None if max_cost_usd in (None, "") else float(max_cost_usd)
        )
        self.agent_timeout_sec = int(agent_timeout_sec)
        self.session_timeout_sec = int(session_timeout_sec)
        self.poll_interval_sec = int(poll_interval_sec)
        self.workdir = workdir
        self.spec_name = sanitize_spec_name(spec_name)
        self.skills = split_skills(skills)
        self.install_deps = as_bool(install_deps)
        self.install_telos = as_bool(install_telos)
        self.install_pi = as_bool(install_pi)
        self.inject_pi_models = as_bool(inject_pi_models)
        self.pi_config_source = pi_config_source
        self.telos_install_url = telos_install_url
        self._last_metadata: dict[str, Any] = {}

    def get_version_command(self) -> str | None:
        return "bash -lc 'export PATH=\"$HOME/.local/bin:$PATH\"; telos --version'"

    async def install(self, environment: BaseEnvironment) -> None:
        if self.install_deps:
            await self._install_system_deps(environment)
        if self.install_telos:
            await self._install_telos(environment)
        if self.install_pi:
            await self._install_pi(environment)
        if self.pi_config_source:
            await self._copy_pi_config(environment)
        if self.inject_pi_models:
            await self._inject_pi_models_json(environment)

    async def _install_system_deps(self, environment: BaseEnvironment) -> None:
        command = """
if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y bash ca-certificates curl coreutils git
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache bash ca-certificates curl coreutils git
fi
"""
        await self.exec_as_root(environment, f"bash -lc {shlex.quote(command)}", timeout_sec=900)

    async def _install_telos(self, environment: BaseEnvironment) -> None:
        command = f"""
{self._shell_prologue()}
if command -v telos >/dev/null 2>&1 && command -v telosd >/dev/null 2>&1; then
  telos --version
  telosd --version
  exit 0
fi
mkdir -p "$HOME/.local/bin"
TELOS_INSTALL_DIR="$HOME/.local/bin" curl -fsSL {shlex.quote(self.telos_install_url)} | sh
telos --version
telosd --version
"""
        await self.exec_as_agent(
            environment,
            f"bash -lc {shlex.quote(command)}",
            env=model_env(),
            timeout_sec=900,
        )

    async def _install_pi(self, environment: BaseEnvironment) -> None:
        command = f"""
{self._shell_prologue()}
if command -v pi >/dev/null 2>&1; then
  pi --version
  exit 0
fi
if ! command -v npm >/dev/null 2>&1; then
  curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.2/install.sh | bash
  export NVM_DIR="${{NVM_DIR:-$HOME/.nvm}}"
  . "$NVM_DIR/nvm.sh"
  nvm install 22
fi
npm install -g @mariozechner/pi-coding-agent
pi --version
"""
        await self.exec_as_agent(
            environment,
            f"bash -lc {shlex.quote(command)}",
            env=model_env(),
            timeout_sec=1200,
        )

    async def _inject_pi_models_json(self, environment: BaseEnvironment) -> None:
        host_models = Path.home() / ".pi" / "agent" / "models.json"
        if not host_models.exists():
            return
        payload = host_models.read_text()
        command = f"""
mkdir -p "$HOME/.pi/agent"
cat > "$HOME/.pi/agent/models.json" <<'PIMODELS'
{payload}
PIMODELS
"""
        await self.exec_as_agent(
            environment,
            f"bash -lc {shlex.quote(command)}",
            env=model_env(),
            timeout_sec=30,
        )

    async def _copy_pi_config(self, environment: BaseEnvironment) -> None:
        host_source = Path(self.pi_config_source or "").expanduser()
        if host_source.exists():
            home_result = await self.exec_as_agent(
                environment,
                "bash -lc 'printf %s \"$HOME\"'",
                env=model_env(),
                timeout_sec=30,
            )
            home = (home_result.stdout or "").strip() or "/tmp/agent_home"
            remote_dir = f"{home.rstrip('/')}/.pi/agent"
            await self.exec_as_agent(
                environment,
                f"bash -lc {shlex.quote(f'mkdir -p {shlex.quote(remote_dir)}')}",
                env=model_env(),
                timeout_sec=30,
            )
            for name in ("auth.json", "models.json", "settings.json"):
                source_file = host_source / name
                if not source_file.exists():
                    continue
                with tempfile.NamedTemporaryFile() as tmp:
                    tmp_path = Path(tmp.name)
                    tmp_path.write_bytes(source_file.read_bytes())
                    await environment.upload_file(
                        tmp_path,
                        f"{remote_dir}/{name}",
                    )
            await self.exec_as_agent(
                environment,
                f"bash -lc {shlex.quote(f'chmod 0600 {shlex.quote(remote_dir)}/* 2>/dev/null || true')}",
                env=model_env(),
                timeout_sec=30,
            )
            return

        source = shlex.quote(self.pi_config_source or "")
        command = f"""
mkdir -p "$HOME/.pi/agent"
for file in auth.json models.json settings.json; do
  if [ -f {source}/"$file" ]; then
    cp {source}/"$file" "$HOME/.pi/agent/$file"
    chmod 0600 "$HOME/.pi/agent/$file" || true
  fi
done
"""
        await self.exec_as_agent(
            environment,
            f"bash -lc {shlex.quote(command)}",
            env=model_env(),
            timeout_sec=30,
        )

    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        self.logs_dir.mkdir(parents=True, exist_ok=True)
        workdir = self._workdir(environment)
        spec_markdown = render_harbor_spec(
            instruction,
            workdir=workdir,
            name=self.spec_name,
            skills=self.skills,
        )
        (self.logs_dir / "telos-harbor-spec.md").write_text(spec_markdown)

        result = await environment.exec(
            command=f"bash -lc {shlex.quote(self._run_script(spec_markdown, workdir))}",
            env=model_env(),
            timeout_sec=self.session_timeout_sec + 120,
        )
        (self.logs_dir / "telos-harbor-stdout.log").write_text(result.stdout or "")
        (self.logs_dir / "telos-harbor-stderr.log").write_text(result.stderr or "")

        metadata = {
            "telos_return_code": result.return_code,
            "telos_stdout_log": str(self.logs_dir / "telos-harbor-stdout.log"),
            "telos_stderr_log": str(self.logs_dir / "telos-harbor-stderr.log"),
        }
        final_session = parse_marked_json(result.stdout or "")
        transcript = parse_marked_text(
            result.stdout or "",
            "TELOS_HARBOR_TRANSCRIPT_BEGIN",
            "TELOS_HARBOR_TRANSCRIPT_END",
        )
        if transcript:
            transcript_path = self.logs_dir / "telos-harbor-transcript.md"
            transcript_path.write_text(transcript)
            metadata["telos_transcript_log"] = str(transcript_path)
        if final_session:
            metadata["telos_session"] = final_session
            context.n_input_tokens = _sum_spec_metric(final_session, "total_input_tokens")
            context.n_output_tokens = _sum_spec_metric(final_session, "total_output_tokens")
            context.n_cache_tokens = _sum_spec_metric(final_session, "total_cache_read_tokens")
            context.cost_usd = _sum_spec_metric(final_session, "total_cost_usd")
        context.metadata = metadata
        self._last_metadata = metadata

        if result.return_code != 0 and not is_completed_telos_session(final_session):
            raise RuntimeError(
                "telos executable agent failed: "
                f"exit={result.return_code}; stderr={(result.stderr or '')[-2000:]}"
            )

    def populate_context_post_run(self, context: AgentContext) -> None:
        if self._last_metadata and not context.metadata:
            context.metadata = self._last_metadata

    def _workdir(self, environment: BaseEnvironment) -> str:
        configured = getattr(environment.task_env_config, "workdir", None)
        return self.workdir or configured or DEFAULT_HARBOR_WORKDIR

    def _shell_prologue(self) -> str:
        return """
set -euo pipefail
export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"
if [ -z "${NVM_DIR:-}" ]; then
  if [ -d "$HOME/.nvm" ]; then
    export NVM_DIR="$HOME/.nvm"
  elif [ -d "/usr/local/nvm" ]; then
    export NVM_DIR="/usr/local/nvm"
  fi
fi
if [ -n "${NVM_DIR:-}" ] && [ -s "$NVM_DIR/nvm.sh" ]; then
  . "$NVM_DIR/nvm.sh"
fi
"""

    def _run_script(self, spec_markdown: str, workdir: str) -> str:
        model = self.model_name or os.environ.get("TELOS_MODEL") or "claude-opus-4-6"
        max_cost = "" if self.max_cost_usd is None else str(self.max_cost_usd)
        max_cost_flag = (
            "" if self.max_cost_usd is None else f" --max-cost-usd {shlex.quote(max_cost)}"
        )
        return f"""
{self._shell_prologue()}
mkdir -p /tmp/telos-harbor /tmp/telos-scratch
json_field() {{
  python3 - "$1" "$2" <<'PY'
import json
import sys

path = sys.argv[1]
field = sys.argv[2]
with open(path, encoding="utf-8") as handle:
    value = json.load(handle)
for part in field.split("."):
    value = value[part]
if value is None:
    raise SystemExit(1)
print(value)
PY
}}
cat > /tmp/telos-harbor/SPEC.md <<'TELOS_SPEC'
{spec_markdown}
TELOS_SPEC
cd {shlex.quote(workdir)}
export TELOS_SESSION_DIR=/tmp/telos-harbor/sessions
run_json="$(telos run /tmp/telos-harbor/SPEC.md --workspace {shlex.quote(workdir)} --model {shlex.quote(model)} --thinking {shlex.quote(self.thinking)} --until {self.until}{max_cost_flag} --agent-timeout-sec {self.agent_timeout_sec} --json)"
printf '%s\n' "$run_json" > /tmp/telos-harbor/run.json
session_id="$(json_field /tmp/telos-harbor/run.json session_id || true)"
if [ -z "$session_id" ]; then
  echo "telos run did not return a session_id" >&2
  cat /tmp/telos-harbor/run.json >&2
  exit 2
fi
deadline="$(($(date +%s) + {self.session_timeout_sec}))"
while :; do
  telos describe "$session_id" --json > /tmp/telos-harbor/describe.json || true
  status="$(json_field /tmp/telos-harbor/describe.json status || true)"
  case "$status" in
    completed|failed|stopped|stale)
      telos logs "$session_id" --raw > /tmp/telos-harbor/transcript.md || true
      printf 'TELOS_HARBOR_TRANSCRIPT_BEGIN\n'
      cat /tmp/telos-harbor/transcript.md
      printf '\nTELOS_HARBOR_TRANSCRIPT_END\n'
      printf 'TELOS_HARBOR_RESULT_BEGIN\n'
      cat /tmp/telos-harbor/describe.json
      printf '\nTELOS_HARBOR_RESULT_END\n'
      if [ "$status" != completed ]; then
        exit 1
      fi
      exit 0
      ;;
  esac
  if [ "$(date +%s)" -ge "$deadline" ]; then
    telos stop "$session_id" >/dev/null 2>&1 || true
    echo "timed out waiting for Telos session $session_id" >&2
    exit 124
  fi
  sleep {self.poll_interval_sec}
done
"""


class TelosAgent(TelosExecutableAgent):
    """Backward-compatible import name for Harbor configs."""


def _sum_spec_metric(session: dict[str, Any], key: str) -> int | float | None:
    values: list[int | float] = []
    for spec in session.get("specs", []):
        value = spec.get(key)
        if isinstance(value, int | float):
            values.append(value)
    if not values:
        return None
    return sum(values)


def is_completed_telos_session(session: dict[str, Any]) -> bool:
    return session.get("status") == "completed"
