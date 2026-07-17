#!/usr/bin/env bash
# Run one SCBench (SlopCodeBench) problem through the Telos Harbor agent.
#
# Usage:
#   run_scbench_eval.sh <problem> [trial-tag]
#
# Examples:
#   run_scbench_eval.sh file_backup smoke
#   TELOS_HARBOR_UNTIL=5 run_scbench_eval.sh code_search t1
#
# This is a problem-parameterized front end for run_scbench_circuit_eval.sh,
# which stays the single env-driven engine. Any TELOS_HARBOR_* variable set by
# the caller wins over the defaults below.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

problem="${1:?usage: run_scbench_eval.sh <problem> [trial-tag]}"
tag="${2:-t1}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

# OpenAI API-key auth: the harbor process env is forwarded into task
# containers per-exec (telos_agent.model_env), so exporting here is
# sufficient for both telos install and agent turns.
if [[ -z "${OPENAI_API_KEY:-}" && -n "${OPENAI_API_KEY_TELOS:-}" ]]; then
  export OPENAI_API_KEY="$OPENAI_API_KEY_TELOS"
fi

# Fuzzy by default (file_backup -> *file*backup*), which can over-match
# problems sharing tokens; set TELOS_HARBOR_SELECTOR for an exact pattern.
export TELOS_HARBOR_SELECTOR="${TELOS_HARBOR_SELECTOR:-*${problem//_/*}*}"
export TELOS_HARBOR_JOB_NAME="${TELOS_HARBOR_JOB_NAME:-telos-scbench-${problem//_/-}-${tag}-${timestamp}}"

export TELOS_HARBOR_ENV="${TELOS_HARBOR_ENV:-modal}"
export TELOS_HARBOR_MODAL_DIND="${TELOS_HARBOR_MODAL_DIND:-true}"
export TELOS_HARBOR_MODEL="${TELOS_HARBOR_MODEL:-openai/gpt-5.5}"
export TELOS_HARBOR_UNTIL="${TELOS_HARBOR_UNTIL:-5}"
export TELOS_HARBOR_N_ATTEMPTS="${TELOS_HARBOR_N_ATTEMPTS:-1}"
export TELOS_HARBOR_N_CONCURRENT="${TELOS_HARBOR_N_CONCURRENT:-1}"
export TELOS_HARBOR_MAX_COST_USD="${TELOS_HARBOR_MAX_COST_USD:-10}"
export TELOS_HARBOR_SESSION_TIMEOUT_SEC="${TELOS_HARBOR_SESSION_TIMEOUT_SEC:-14400}"
export TELOS_HARBOR_SKILLS="${TELOS_HARBOR_SKILLS:-verify-engineering*,verify-quality*}"

# API-key auth needs no codex OAuth config and no host models.json.
export TELOS_HARBOR_INJECT_PI_MODELS="${TELOS_HARBOR_INJECT_PI_MODELS:-false}"

exec "$script_dir/run_scbench_circuit_eval.sh"
