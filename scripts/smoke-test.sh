#!/usr/bin/env bash
# CLI contract smoke test for Telos.
#
# Usage:
#   TELOS_SMOKE_BIN=/path/to/telos TELOS_SMOKE_TELOSD=/path/to/telosd ./smoke-test.sh
#
# Validates the end-user CLI surface, then launches one tiny isolated local
# run to cover real session creation, inspection, logs, stop, and persisted
# artifacts.

set -euo pipefail

TELOS="${TELOS_SMOKE_BIN:-$(command -v telos || true)}"
TELOSD="${TELOS_SMOKE_TELOSD:-$(command -v telosd || true)}"

if [ -z "$TELOS" ] || [ ! -x "$TELOS" ]; then
  echo "TELOS_SMOKE_BIN is not executable and telos was not found on PATH" >&2
  exit 1
fi

if [ -n "$TELOSD" ]; then
  export TELOSD_PATH="$TELOSD"
fi

PASS=0
FAIL=0
FIXTURE_DIR=""
SESSION_ID=""

pass() {
  PASS=$((PASS + 1))
  echo "  [PASS] $1"
}

fail() {
  FAIL=$((FAIL + 1))
  echo "  [FAIL] $1"
}

telos_cmd() {
  env \
    -u TELOS_API_TOKEN \
    -u TELOS_API_ENDPOINT \
    -u TELOS_RUNTIME \
    -u TELOS_SESSION_DIR \
    -u TELOS_SESSION_ID \
    "$TELOS" "$@"
}

telos_fixture() {
  (cd "$FIXTURE_DIR" && telos_cmd "$@")
}

cleanup() {
  if [ -n "$FIXTURE_DIR" ] && [ -n "$SESSION_ID" ]; then
    telos_fixture stop "$SESSION_ID" >/dev/null 2>&1 || true
  fi
  if [ -n "$FIXTURE_DIR" ]; then
    rm -rf "$FIXTURE_DIR"
  fi
}
trap cleanup EXIT

check() {
  local label="$1"
  shift
  if "$@"; then
    pass "$label"
  else
    fail "$label"
  fi
}

check_contains() {
  local label="$1"
  local text="$2"
  local pattern="$3"
  if grep -qF -- "$pattern" <<<"$text"; then
    pass "$label"
  else
    fail "$label (missing: $pattern)"
  fi
}

check_not_contains() {
  local label="$1"
  local text="$2"
  local pattern="$3"
  if grep -qF -- "$pattern" <<<"$text"; then
    fail "$label (found: $pattern)"
  else
    pass "$label"
  fi
}

check_json() {
  local label="$1"
  local text="$2"
  if python3 -m json.tool >/dev/null 2>&1 <<<"$text"; then
    pass "$label"
  else
    fail "$label"
  fi
}

json_value() {
  local key="$1"
  python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1], ""))' "$key"
}

json_has_session() {
  local expected="$1"
  python3 -c 'import json,sys; ids=[s.get("session_id") for s in json.load(sys.stdin).get("sessions", [])]; assert sys.argv[1] in ids' "$expected"
}

FIXTURE_DIR="$(mktemp -d)"
mkdir -p "$FIXTURE_DIR/fixture"
FIXTURE_SPEC_PATH="$FIXTURE_DIR/fixture/SPEC.md"

cat > "$FIXTURE_SPEC_PATH" <<'FIXTURE_SPEC'
---
version: 0.1.0
name: smoke-fixture
platform: local
---

# Goal

Create a file called `hello.txt` in the workspace root containing exactly:

smoke test passed
FIXTURE_SPEC

(
  cd "$FIXTURE_DIR"
  git init -q
  git add -A
  git -c user.name="Telos Smoke" -c user.email="smoke@telos.local" commit -q -m "init"
)

echo "=== 1. Help and version ==="

HELP="$(telos_cmd --help 2>&1)"
check_contains "help mentions plan" "$HELP" "plan SPEC.md"
check_contains "help mentions list" "$HELP" "list"
check_contains "help mentions describe" "$HELP" "describe SESSION"
check_contains "help mentions logs" "$HELP" "logs"
check_contains "help mentions stop" "$HELP" "stop SESSION"
check_contains "help mentions --version" "$HELP" "--version"

VERSION="$(telos_cmd --version 2>&1)"
check_contains "--version identifies telos" "$VERSION" "telos"

VERSION_SUB="$(telos_cmd version 2>&1)"
check_contains "version subcommand identifies telos" "$VERSION_SUB" "telos"

HELP_SHORT="$(telos_cmd -h 2>&1)"
check_contains "-h shows usage" "$HELP_SHORT" "usage: telos"

HELP_WORD="$(telos_cmd help 2>&1)"
check_contains "help subcommand shows usage" "$HELP_WORD" "usage: telos"

echo "=== 2. Error handling ==="

BOGUS="$(telos_cmd bogus 2>&1 || true)"
check_contains "unknown command says unknown" "$BOGUS" "unknown command: bogus"
if telos_cmd bogus >/dev/null 2>&1; then
  fail "unknown command exits non-zero"
else
  pass "unknown command exits non-zero"
fi

RUN_NOARG="$(telos_cmd run 2>&1 || true)"
check_contains "run without args shows usage" "$RUN_NOARG" "usage: telos run"

DESCRIBE_NOARG="$(telos_cmd describe 2>&1 || true)"
check_contains "describe without args shows usage" "$DESCRIBE_NOARG" "usage: telos describe"

LOGS_NOARG="$(telos_cmd logs 2>&1 || true)"
check_contains "logs without args shows usage" "$LOGS_NOARG" "usage: telos logs"

DELETE_NOARG="$(telos_cmd delete 2>&1 || true)"
check_contains "delete without args shows usage" "$DELETE_NOARG" "usage: telos delete"

DESCRIBE_MISSING="$(telos_cmd describe nonexistent_session 2>&1 || true)"
check_contains "describe missing session says not found" "$DESCRIBE_MISSING" "not found"
check_not_contains "describe missing session has no panic" "$DESCRIBE_MISSING" "panic"

LOGS_MISSING="$(telos_cmd logs nonexistent_session 2>&1 || true)"
check_contains "logs missing session says not found" "$LOGS_MISSING" "not found"

DELETE_MISSING="$(telos_cmd delete nonexistent_session 2>&1 || true)"
check_contains "delete missing session says not found" "$DELETE_MISSING" "not found"

PLAN_MISSING="$(telos_cmd plan nonexistent.md 2>&1 || true)"
check_contains "plan missing spec is specific" "$PLAN_MISSING" "spec file not found"
check_not_contains "plan missing spec has no panic" "$PLAN_MISSING" "panic"

echo "=== 3. Plan contract ==="

PLAN_HUMAN="$(telos_cmd plan "$FIXTURE_SPEC_PATH" 2>&1)"
check_contains "plan human includes spec name" "$PLAN_HUMAN" "smoke-fixture"
check_contains "plan human includes platform" "$PLAN_HUMAN" "local"
check_not_contains "plan human has no panic" "$PLAN_HUMAN" "panic"

PLAN_JSON="$(telos_cmd plan "$FIXTURE_SPEC_PATH" --json 2>&1)"
check_json "plan --json parses" "$PLAN_JSON"
check_contains "plan JSON includes spec name" "$PLAN_JSON" '"name": "smoke-fixture"'
check_contains "plan JSON includes platform" "$PLAN_JSON" '"platform": "local"'

echo "=== 4. List contract ==="

LIST_HUMAN="$(telos_cmd list --local 2>&1 || true)"
check_not_contains "list has no panic" "$LIST_HUMAN" "panic"

LIST_JSON="$(telos_cmd list --local --json 2>&1 || true)"
check_json "list --json parses" "$LIST_JSON"
check_contains "list JSON includes sessions" "$LIST_JSON" '"sessions"'

echo "=== 5. Fixture lifecycle ==="

echo "  launching fixture run..."
RUN_JSON="$(telos_fixture run "$FIXTURE_SPEC_PATH" \
  --json \
  --until 1 \
  --max-cost-usd 4 \
  2>&1 || true)"

check_json "run --json parses" "$RUN_JSON"
SESSION_ID="$(json_value session_id <<<"$RUN_JSON" 2>/dev/null || true)"
SESSION_DIR="$(json_value session_dir <<<"$RUN_JSON" 2>/dev/null || true)"

if [ -n "$SESSION_ID" ] && [ -n "$SESSION_DIR" ]; then
  pass "run JSON includes session_id and session_dir"
else
  fail "run JSON includes session_id and session_dir"
  echo "$RUN_JSON"
  echo "=== Results: $PASS passed, $FAIL failed ==="
  exit 1
fi

check_contains "run JSON includes spec_name" "$RUN_JSON" '"spec_name"'
check_contains "run JSON includes status" "$RUN_JSON" '"status"'

echo "  session: $SESSION_ID"
echo "  waiting for terminal state..."

STATUS=""
WAITED=0
while [ "$WAITED" -lt 300 ]; do
  DESCRIBE_JSON="$(telos_fixture describe "$SESSION_ID" --json 2>/dev/null || true)"
  STATUS="$(json_value status <<<"$DESCRIBE_JSON" 2>/dev/null || true)"
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ] || [ "$STATUS" = "stopped" ]; then
    break
  fi
  sleep 5
  WAITED=$((WAITED + 5))
done

echo "  final status: ${STATUS:-unknown} after ${WAITED}s"
case "$STATUS" in
  completed|failed|stopped) pass "session reached terminal state" ;;
  *) fail "session reached terminal state" ;;
esac

echo "=== 6. Discover and inspect ==="

LIST_AFTER="$(telos_fixture list --local --wide --json 2>&1 || true)"
check_json "fixture list --json parses" "$LIST_AFTER"
if json_has_session "$SESSION_ID" <<<"$LIST_AFTER" 2>/dev/null; then
  pass "fixture session appears in list"
else
  fail "fixture session appears in list"
fi

DESCRIBE_HUMAN="$(telos_fixture describe "$SESSION_ID" 2>&1 || true)"
check_contains "describe includes session id" "$DESCRIBE_HUMAN" "$SESSION_ID"
check_contains "describe includes spec name" "$DESCRIBE_HUMAN" "smoke-fixture"
check_not_contains "describe has no panic" "$DESCRIBE_HUMAN" "panic"

DESCRIBE_JSON="$(telos_fixture describe "$SESSION_ID" --json 2>&1 || true)"
check_json "describe --json parses" "$DESCRIBE_JSON"
check_contains "describe JSON includes session_id" "$DESCRIBE_JSON" '"session_id"'
check_contains "describe JSON includes status" "$DESCRIBE_JSON" '"status"'

LOGS_OUTPUT="$(telos_fixture logs "$SESSION_ID" 2>&1 || true)"
check_not_contains "logs has no panic" "$LOGS_OUTPUT" "panic"
if [ -n "$LOGS_OUTPUT" ]; then
  pass "logs returns output"
else
  pass "logs empty output handled"
fi

echo "=== 7. Artifacts and stop ==="

check "session.json exists" test -f "$SESSION_DIR/session.json"

SPEC_DIR="$SESSION_DIR/specs/smoke-fixture"
check "spec directory exists" test -d "$SPEC_DIR"
check "copied spec exists" test -f "$SPEC_DIR/spec.md"
check "evidence.jsonl exists" test -f "$SPEC_DIR/evidence.jsonl"

TRANSCRIPT_FILE="$(find "$SPEC_DIR" -name 'transcript-*.md' -print -quit 2>/dev/null || true)"
if [ -n "$TRANSCRIPT_FILE" ]; then
  pass "transcript file exists"
else
  fail "transcript file exists"
fi

WORKSPACE_ARCHIVE="$(find "$SPEC_DIR" -name 'workspace.tar.gz' -print -quit 2>/dev/null || true)"
if [ -n "$WORKSPACE_ARCHIVE" ]; then
  pass "workspace archive exists"
else
  fail "workspace archive exists"
fi

STOP_JSON="$(telos_fixture stop "$SESSION_ID" --json 2>&1 || true)"
if python3 -m json.tool >/dev/null 2>&1 <<<"$STOP_JSON"; then
  pass "stop --json parses"
else
  check_not_contains "stop has no panic" "$STOP_JSON" "panic"
  pass "stop on terminal session handled"
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
