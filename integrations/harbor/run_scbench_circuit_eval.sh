#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

if [[ -n "${PYTHONPATH:-}" ]]; then
  export PYTHONPATH="$repo_root:$PYTHONPATH"
else
  export PYTHONPATH="$repo_root"
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

dataset="${TELOS_HARBOR_DATASET:-gabeorlanski/slopcodebench}"
selector="${TELOS_HARBOR_SELECTOR:-*circuit*eval*}"
job_name="${TELOS_HARBOR_JOB_NAME:-telos-scbench-circuit-${timestamp}}"
jobs_dir="${TELOS_HARBOR_JOBS_DIR:-$repo_root/eval-runs/harbor}"
n_tasks="${TELOS_HARBOR_N_TASKS:-1}"
n_attempts="${TELOS_HARBOR_N_ATTEMPTS:-1}"
n_concurrent="${TELOS_HARBOR_N_CONCURRENT:-1}"
environment="${TELOS_HARBOR_ENV:-docker}"
extra_docker_compose="${TELOS_HARBOR_EXTRA_DOCKER_COMPOSE:-}"
modal_dind="${TELOS_HARBOR_MODAL_DIND:-true}"

model="${TELOS_HARBOR_MODEL:-openai-codex/gpt-5.5}"
thinking="${TELOS_HARBOR_THINKING:-high}"
until="${TELOS_HARBOR_UNTIL:-1}"
session_timeout_sec="${TELOS_HARBOR_SESSION_TIMEOUT_SEC:-7200}"
max_cost_usd="${TELOS_HARBOR_MAX_COST_USD:-10}"
skills="${TELOS_HARBOR_SKILLS:-verify-engineering*}"
install_url="${TELOS_HARBOR_TELOS_INSTALL_URL:-https://usetelos.ai/releases/latest/install.sh}"

inject_pi_models="${TELOS_HARBOR_INJECT_PI_MODELS:-true}"
pi_config_source="${TELOS_HARBOR_PI_CONFIG_SOURCE:-}"

uvx_package="${TELOS_HARBOR_UVX_PACKAGE:-harbor}"
if [[ "$environment" != "docker" ]]; then
  uvx_package="${TELOS_HARBOR_UVX_PACKAGE:-harbor[$environment]}"
fi
if [[ "${TELOS_HARBOR_USE_LOCAL:-}" == "true" ]]; then
  harbor_command=(harbor run)
  selector_arg=(--task-name "$selector")
else
  harbor_command=(uvx "$uvx_package" run)
  selector_arg=(--include-task-name "$selector")
fi

args=(
  "${harbor_command[@]}"
  -d "$dataset"
  "${selector_arg[@]}"
  --n-tasks "$n_tasks"
  --n-concurrent "$n_concurrent"
  --job-name "$job_name"
  --jobs-dir "$jobs_dir"
  --n-attempts "$n_attempts"
  --env "$environment"
  --agent-import-path integrations.harbor.telos_agent:TelosExecutableAgent
  --model "$model"
  --ak "thinking=$thinking"
  --ak "until=$until"
  --ak "session_timeout_sec=$session_timeout_sec"
  --ak "max_cost_usd=$max_cost_usd"
  --ak "skills=$skills"
  --ak "telos_install_url=$install_url"
  --ak "inject_pi_models=$inject_pi_models"
  --yes
  --debug
)

if [[ -n "$pi_config_source" && "$environment" == "docker" ]]; then
  mounts="$(
    python3 - "$pi_config_source" <<'PY'
import json
import sys

source = sys.argv[1]
print(json.dumps([
    {
        "type": "bind",
        "source": source,
        "target": "/tmp/host-pi-agent",
        "read_only": True,
        "bind": {"create_host_path": False},
    }
]))
PY
  )"
  args+=(
    --ak "pi_config_source=/tmp/host-pi-agent"
    --mounts-json "$mounts"
  )
elif [[ -n "$pi_config_source" ]]; then
  args+=(
    --ak "pi_config_source=$pi_config_source"
  )
fi

if [[ -z "$extra_docker_compose" && "$environment" == "modal" && "$modal_dind" != "false" ]]; then
  extra_docker_compose="$repo_root/integrations/harbor/modal-dind-compose.yaml"
fi
if [[ -n "$extra_docker_compose" ]]; then
  args+=(
    --extra-docker-compose "$extra_docker_compose"
  )
fi

printf 'Running Telos Harbor SCBench job: %s\n' "$job_name" >&2
printf 'Environment: %s\n' "$environment" >&2
if [[ -n "$extra_docker_compose" ]]; then
  printf 'Extra compose: %s\n' "$extra_docker_compose" >&2
fi
printf 'Results directory: %s/%s\n' "$jobs_dir" "$job_name" >&2
exec "${args[@]}"
