#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
dist="${1:?usage: scripts/test-release-installer.sh DIST VERSION}"
version="${2:?usage: scripts/test-release-installer.sh DIST VERSION}"

dist="$(cd "${dist}" && pwd -P)"
if [[ "$(basename "${dist}")" != "${version}" ]]; then
  echo "test-release-installer: dist does not match version ${version}" >&2
  exit 1
fi

release_base_url="file://$(dirname "${dist}")"
test_root="$(mktemp -d)"
cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

run_installer_from() {
  local base_url="$1"
  local test_home="$2"
  local install_dir="$3"
  local log_file="$4"
  shift 4

  env \
    HOME="${test_home}" \
    TELOS_RELEASE_BASE_URL="${base_url}" \
    TELOS_INSTALL_DIR="${install_dir}" \
    TELOS_AGENT_SKILLS_DIR= \
    TELOS_CLAUDE_SKILLS_DIR= \
    TELOS_INSTALL_CLAUDE_SKILLS= \
    TELOS_INSTALL_LOCK_DIR= \
    "$@" \
    sh "${dist}/install.sh" >"${log_file}" 2>&1
}

run_installer() {
  run_installer_from "${release_base_url}" "$@"
}

assert_skill() {
  local skill_name="$1"
  local installed_dir="$2"
  local registry_ref="@telos/${skill_name}"

  diff -ru -x .telos-managed \
    "${repo_root}/skills/${skill_name}" "${installed_dir}"
  test "$(cat "${installed_dir}/.telos-managed")" = "${registry_ref}"
  test -z "$(find "${installed_dir}" -type f ! -perm 0644 -print -quit)"
}

assert_no_stages() {
  local root="$1"

  test -z "$(find "${root}" -name '.telos-cli.*' -o -name '.telos-spec.*' \
    -o -name '.telos.*' -o -name '.telosd.*' 2>/dev/null | head -n 1)"
}

# A default install makes both skills available to Codex and Claude Code.
default_home="${test_root}/default-home"
default_bin="${test_root}/default-bin"
mkdir -p "${default_home}"
run_installer "${default_home}" "${default_bin}" "${test_root}/default.log"
assert_skill "telos-cli" "${default_home}/.agents/skills/telos-cli"
assert_skill "telos-spec" "${default_home}/.agents/skills/telos-spec"
assert_skill "telos-cli" "${default_home}/.claude/skills/telos-cli"
assert_skill "telos-spec" "${default_home}/.claude/skills/telos-spec"
test -x "${default_bin}/telos"
test -x "${default_bin}/telosd"
assert_no_stages "${default_home}"
assert_no_stages "${default_bin}"

# Reinstallation replaces a complete managed target and leaves no old files.
touch "${default_home}/.agents/skills/telos-cli/old-sentinel"
touch "${default_home}/.agents/skills/telos-spec/old-sentinel"
touch "${default_home}/.claude/skills/telos-cli/old-sentinel"
touch "${default_home}/.claude/skills/telos-spec/old-sentinel"
run_installer "${default_home}" "${default_bin}" "${test_root}/reinstall.log"
test ! -e "${default_home}/.agents/skills/telos-cli/old-sentinel"
test ! -e "${default_home}/.agents/skills/telos-spec/old-sentinel"
test ! -e "${default_home}/.claude/skills/telos-cli/old-sentinel"
test ! -e "${default_home}/.claude/skills/telos-spec/old-sentinel"
assert_no_stages "${default_home}"
assert_no_stages "${default_bin}"

# The legacy override remains the sole destination and supports spaces.
custom_home="${test_root}/custom-home"
custom_bin="${test_root}/custom-bin"
custom_skills="${test_root}/custom skill destination"
mkdir -p "${custom_home}"
run_installer \
  "${custom_home}" \
  "${custom_bin}" \
  "${test_root}/custom.log" \
  "TELOS_AGENT_SKILLS_DIR=${custom_skills}"
assert_skill "telos-cli" "${custom_skills}/telos-cli"
assert_skill "telos-spec" "${custom_skills}/telos-spec"
test ! -e "${custom_home}/.agents"
test ! -e "${custom_home}/.claude"

# A byte-identical manual extraction is safe to adopt and gains the installer
# marker; public metadata without exact tree parity remains insufficient.
manual_home="${test_root}/manual-home"
manual_bin="${test_root}/manual-bin"
manual_skills="${test_root}/manual-skills"
mkdir -p "${manual_home}" "${manual_skills}/telos-spec"
tar -xzf "${dist}/telos-spec-skill.tar.gz" -C "${manual_skills}/telos-spec"
run_installer \
  "${manual_home}" \
  "${manual_bin}" \
  "${test_root}/manual.log" \
  "TELOS_AGENT_SKILLS_DIR=${manual_skills}"
assert_skill "telos-cli" "${manual_skills}/telos-cli"
assert_skill "telos-spec" "${manual_skills}/telos-spec"

# Custom destinations can use an explicit lock when HOME is read-only. The
# failure without that override distinguishes permissions from contention.
readonly_home="${test_root}/readonly-home"
readonly_bin="${test_root}/readonly-bin"
readonly_skills="${test_root}/readonly-skills"
readonly_lock="${test_root}/readonly-install.lock"
mkdir -p "${readonly_home}" "${readonly_skills}"
chmod 0555 "${readonly_home}"
if run_installer \
  "${readonly_home}" \
  "${readonly_bin}" \
  "${test_root}/readonly-refusal.log" \
  "TELOS_AGENT_SKILLS_DIR=${readonly_skills}"; then
  echo "test-release-installer: read-only lock location was accepted" >&2
  exit 1
fi
grep -Fq "could not create install lock" "${test_root}/readonly-refusal.log"
grep -Fq "TELOS_INSTALL_LOCK_DIR" "${test_root}/readonly-refusal.log"
run_installer \
  "${readonly_home}" \
  "${readonly_bin}" \
  "${test_root}/readonly.log" \
  "TELOS_AGENT_SKILLS_DIR=${readonly_skills}" \
  "TELOS_INSTALL_LOCK_DIR=${readonly_lock}"
chmod 0755 "${readonly_home}"
assert_skill "telos-cli" "${readonly_skills}/telos-cli"
assert_skill "telos-spec" "${readonly_skills}/telos-spec"

# A Codex-only user can explicitly skip Claude installation, so an unrelated
# Claude skill never blocks binary and agent-root updates.
codex_only_home="${test_root}/codex-only-home"
codex_only_bin="${test_root}/codex-only-bin"
mkdir -p "${codex_only_home}/.claude/skills/telos-spec"
printf '%s\n' "user-owned" \
  >"${codex_only_home}/.claude/skills/telos-spec/README.md"
if run_installer \
  "${codex_only_home}" \
  "${codex_only_bin}" \
  "${test_root}/codex-only-refusal.log"; then
  echo "test-release-installer: unmanaged Claude skill was overwritten" >&2
  exit 1
fi
grep -Fq "TELOS_INSTALL_CLAUDE_SKILLS=0" \
  "${test_root}/codex-only-refusal.log"
test ! -e "${codex_only_home}/.agents/skills/telos-cli"
test ! -e "${codex_only_bin}/telos"
run_installer \
  "${codex_only_home}" \
  "${codex_only_bin}" \
  "${test_root}/codex-only.log" \
  "TELOS_INSTALL_CLAUDE_SKILLS=0"
assert_skill "telos-cli" "${codex_only_home}/.agents/skills/telos-cli"
assert_skill "telos-spec" "${codex_only_home}/.agents/skills/telos-spec"
test -f "${codex_only_home}/.claude/skills/telos-spec/README.md"
test -x "${codex_only_bin}/telos"
test -x "${codex_only_bin}/telosd"

# Physically identical default roots are deduplicated without replacing the
# user's root-level symlink.
shared_home="${test_root}/shared-home"
shared_bin="${test_root}/shared-bin"
mkdir -p "${shared_home}/.agents/skills" "${shared_home}/.claude"
ln -s "../.agents/skills" "${shared_home}/.claude/skills"
run_installer "${shared_home}" "${shared_bin}" "${test_root}/shared.log"
test -L "${shared_home}/.claude/skills"
assert_skill "telos-cli" "${shared_home}/.agents/skills/telos-cli"
assert_skill "telos-spec" "${shared_home}/.agents/skills/telos-spec"

# A recognized per-skill symlink remains under the user's control.
linked_home="${test_root}/linked-home"
linked_bin="${test_root}/linked-bin"
linked_skill="${test_root}/linked-telos-cli"
mkdir -p "${linked_home}/.agents/skills" "${linked_skill}"
tar -xzf "${dist}/telos-cli-skill.tar.gz" -C "${linked_skill}"
ln -s "${linked_skill}" "${linked_home}/.agents/skills/telos-cli"
run_installer "${linked_home}" "${linked_bin}" "${test_root}/linked.log"
test -L "${linked_home}/.agents/skills/telos-cli"
grep -Fq "preserving linked skill" "${test_root}/linked.log"
assert_skill "telos-spec" "${linked_home}/.agents/skills/telos-spec"

# A per-skill link to the other configured root may begin dangling; the linked
# destination is populated later in the same all-or-nothing installation.
alias_home="${test_root}/alias-home"
alias_bin="${test_root}/alias-bin"
mkdir -p "${alias_home}/.agents/skills" "${alias_home}/.claude/skills"
ln -s "../../.claude/skills/telos-spec" \
  "${alias_home}/.agents/skills/telos-spec"
run_installer "${alias_home}" "${alias_bin}" "${test_root}/alias.log"
test -L "${alias_home}/.agents/skills/telos-spec"
assert_skill "telos-spec" "${alias_home}/.agents/skills/telos-spec"
assert_skill "telos-spec" "${alias_home}/.claude/skills/telos-spec"

# A stale external linked skill is never silently left on an older release.
stale_home="${test_root}/stale-home"
stale_bin="${test_root}/stale-bin"
stale_skill="${test_root}/stale-telos-cli"
mkdir -p "${stale_home}/.agents/skills" "${stale_skill}"
tar -xzf "${dist}/telos-cli-skill.tar.gz" -C "${stale_skill}"
printf '%s\n' "stale" >"${stale_skill}/old-release-sentinel"
ln -s "${stale_skill}" "${stale_home}/.agents/skills/telos-cli"
if run_installer "${stale_home}" "${stale_bin}" "${test_root}/stale.log"; then
  echo "test-release-installer: stale linked skill was accepted" >&2
  exit 1
fi
grep -Fq "refusing linked skill that does not match this release" \
  "${test_root}/stale.log"
test -L "${stale_home}/.agents/skills/telos-cli"
test -f "${stale_skill}/old-release-sentinel"
test ! -e "${stale_bin}/telos"

# An unknown directory at a reserved skill path fails before any artifact is
# activated, leaving existing binaries untouched.
unknown_home="${test_root}/unknown-home"
unknown_bin="${test_root}/unknown-bin"
mkdir -p "${unknown_home}/.agents/skills/telos-spec" "${unknown_bin}"
printf '%s\n' "user-owned" >"${unknown_home}/.agents/skills/telos-spec/README.md"
printf '%s\n' "old-telos" >"${unknown_bin}/telos"
printf '%s\n' "old-telosd" >"${unknown_bin}/telosd"
if run_installer "${unknown_home}" "${unknown_bin}" "${test_root}/unknown.log"; then
  echo "test-release-installer: unknown skill target was overwritten" >&2
  exit 1
fi
grep -Fq "refusing to replace unrecognized skill" "${test_root}/unknown.log"
test "$(cat "${unknown_bin}/telos")" = "old-telos"
test "$(cat "${unknown_bin}/telosd")" = "old-telosd"
test -f "${unknown_home}/.agents/skills/telos-spec/README.md"
test ! -e "${unknown_home}/.agents/skills/telos-cli"

# Exact public metadata without the installer ownership marker cannot spoof Telos
# ownership of a user skill.
spoof_home="${test_root}/spoof-home"
spoof_bin="${test_root}/spoof-bin"
spoof_skill="${spoof_home}/.agents/skills/telos-spec"
mkdir -p "${spoof_skill}" "${spoof_bin}"
printf '%s\n' \
  '---' \
  'name: telos-spec' \
  'metadata:' \
  '  registry: "@telos/telos-spec"' \
  '---' \
  '# User skill' >"${spoof_skill}/SKILL.md"
if run_installer "${spoof_home}" "${spoof_bin}" "${test_root}/spoof.log"; then
  echo "test-release-installer: spoofed ownership metadata was accepted" >&2
  exit 1
fi
grep -Fq "refusing to replace unrecognized skill" "${test_root}/spoof.log"
grep -Fxq '  registry: "@telos/telos-spec"' "${spoof_skill}/SKILL.md"
test ! -e "${spoof_home}/.agents/skills/telos-cli"

# A telos-cli installation from before managed markers upgrades normally,
# gains a marker, and retains its prior directory outside agent discovery.
legacy_home="${test_root}/legacy-home"
legacy_bin="${test_root}/legacy-bin"
legacy_skills="${legacy_home}/.agents/skills"
mkdir -p "${legacy_skills}/telos-cli"
tar -xzf "${dist}/telos-cli-skill.tar.gz" -C "${legacy_skills}/telos-cli"
touch "${legacy_skills}/telos-cli/local-customization"
test ! -e "${legacy_skills}/telos-cli/.telos-managed"
run_installer \
  "${legacy_home}" \
  "${legacy_bin}" \
  "${test_root}/legacy.log"
assert_skill "telos-cli" "${legacy_skills}/telos-cli"
assert_skill "telos-spec" "${legacy_skills}/telos-spec"
assert_skill "telos-cli" "${legacy_home}/.claude/skills/telos-cli"
assert_skill "telos-spec" "${legacy_home}/.claude/skills/telos-spec"
test -z "$(find "${legacy_skills}" -maxdepth 1 -type d \
  -name '.telos-cli.*.previous' -print -quit)"
legacy_backup="$(find "${legacy_home}/.agents/.telos-skill-backups" \
  -mindepth 2 -maxdepth 2 -type d -name telos-cli -print -quit)"
test -n "${legacy_backup}"
test -f "${legacy_backup}/local-customization"
grep -Fq "previous unmarked skill retained" "${test_root}/legacy.log"

# A binary staging failure cleans every pre-journal stage and leaves no target
# active, even though both skill archives were already extracted.
copy_failure_home="${test_root}/copy-failure-home"
copy_failure_bin="${test_root}/copy-failure-bin"
copy_wrapper_bin="${test_root}/copy-wrapper-bin"
mkdir -p "${copy_failure_home}" "${copy_wrapper_bin}"
printf '%s\n' '#!/usr/bin/env sh' 'exit 1' >"${copy_wrapper_bin}/cp"
chmod 0755 "${copy_wrapper_bin}/cp"
if run_installer \
  "${copy_failure_home}" \
  "${copy_failure_bin}" \
  "${test_root}/copy-failure.log" \
  "PATH=${copy_wrapper_bin}:${PATH}"; then
  echo "test-release-installer: injected copy failure succeeded" >&2
  exit 1
fi
test ! -e "${copy_failure_home}/.agents/skills/telos-cli"
test ! -e "${copy_failure_home}/.agents/skills/telos-spec"
test ! -e "${copy_failure_bin}/telos"
test ! -e "${copy_failure_bin}/telosd"
assert_no_stages "${copy_failure_home}"
assert_no_stages "${copy_failure_bin}"

# A checksum-valid but invalid second skill archive fails before the already
# installed release is touched.
broken_versions="${test_root}/broken-releases"
broken_dist="${broken_versions}/${version}"
malformed_skill="${test_root}/malformed-skill"
mkdir -p "${broken_dist}" "${malformed_skill}"
cp -R "${dist}/." "${broken_dist}/"
printf '%s\n' "invalid skill" >"${malformed_skill}/SKILL.md"
chmod u+w "${broken_dist}/telos-spec-skill.tar.gz"
tar -czf "${broken_dist}/telos-spec-skill.tar.gz" -C "${malformed_skill}" .
(
  cd "${broken_dist}"
  shasum -a 256 telos-* telosd-* >SHA256SUMS
)

failure_home="${test_root}/failure-home"
failure_bin="${test_root}/failure-bin"
mkdir -p "${failure_home}"
run_installer "${failure_home}" "${failure_bin}" "${test_root}/failure-seed.log"
touch "${failure_home}/.agents/skills/telos-cli/old-release-sentinel"
touch "${failure_home}/.agents/skills/telos-spec/old-release-sentinel"
telos_before="$(shasum -a 256 "${failure_bin}/telos" | awk '{ print $1 }')"
if run_installer_from \
  "file://${broken_versions}" \
  "${failure_home}" \
  "${failure_bin}" \
  "${test_root}/failure.log"; then
  echo "test-release-installer: malformed second skill was installed" >&2
  exit 1
fi
grep -Fq "telos-spec archive has invalid skill metadata" "${test_root}/failure.log"
test -f "${failure_home}/.agents/skills/telos-cli/old-release-sentinel"
test -f "${failure_home}/.agents/skills/telos-spec/old-release-sentinel"
test "$(shasum -a 256 "${failure_bin}/telos" | awk '{ print $1 }')" = "${telos_before}"
assert_no_stages "${failure_home}"
assert_no_stages "${failure_bin}"

# A failure after earlier targets have activated rolls the whole release back.
rollback_home="${test_root}/rollback-home"
rollback_bin="${test_root}/rollback-bin"
wrapper_bin="${test_root}/wrapper-bin"
mv_count="${test_root}/mv-count"
real_mv="$(command -v mv)"
mkdir -p "${rollback_home}" "${wrapper_bin}"
printf '%s\n' \
  '#!/usr/bin/env sh' \
  'count=0' \
  'if [ -f "$MV_COUNT_FILE" ]; then count="$(cat "$MV_COUNT_FILE")"; fi' \
  'count="$((count + 1))"' \
  'printf "%s\n" "$count" >"$MV_COUNT_FILE"' \
  'if [ -n "${MV_FAIL_FROM:-}" ] && [ "$count" -ge "$MV_FAIL_FROM" ]; then exit 1; fi' \
  'if [ -n "${MV_FAIL_AT:-}" ] && [ "$count" = "$MV_FAIL_AT" ]; then exit 1; fi' \
  'exec "$MV_REAL" "$@"' >"${wrapper_bin}/mv"
chmod 0755 "${wrapper_bin}/mv"
if run_installer \
  "${rollback_home}" \
  "${rollback_bin}" \
  "${test_root}/rollback.log" \
  "PATH=${wrapper_bin}:${PATH}" \
  "MV_COUNT_FILE=${mv_count}" \
  "MV_FAIL_AT=3" \
  "MV_REAL=${real_mv}"; then
  echo "test-release-installer: injected activation failure succeeded" >&2
  exit 1
fi
grep -Fq "failed to activate" "${test_root}/rollback.log"
test ! -e "${rollback_home}/.agents/skills/telos-cli"
test ! -e "${rollback_home}/.agents/skills/telos-spec"
test ! -e "${rollback_home}/.claude/skills/telos-cli"
test ! -e "${rollback_home}/.claude/skills/telos-spec"
test ! -e "${rollback_bin}/telos"
test ! -e "${rollback_bin}/telosd"
assert_no_stages "${rollback_home}"
assert_no_stages "${rollback_bin}"

# Rollback restores replaced targets, including user-visible files from the
# previously installed release.
restore_home="${test_root}/restore-home"
restore_bin="${test_root}/restore-bin"
restore_count="${test_root}/restore-mv-count"
mkdir -p "${restore_home}"
run_installer "${restore_home}" "${restore_bin}" "${test_root}/restore-seed.log"
touch "${restore_home}/.agents/skills/telos-cli/previous-release-sentinel"
touch "${restore_home}/.agents/skills/telos-spec/previous-release-sentinel"
printf '%s\n' "previous-telos" >"${restore_bin}/telos"
printf '%s\n' "previous-telosd" >"${restore_bin}/telosd"
if run_installer \
  "${restore_home}" \
  "${restore_bin}" \
  "${test_root}/restore.log" \
  "PATH=${wrapper_bin}:${PATH}" \
  "MV_COUNT_FILE=${restore_count}" \
  "MV_FAIL_AT=4" \
  "MV_REAL=${real_mv}"; then
  echo "test-release-installer: injected replacement failure succeeded" >&2
  exit 1
fi
test -f "${restore_home}/.agents/skills/telos-cli/previous-release-sentinel"
test -f "${restore_home}/.agents/skills/telos-spec/previous-release-sentinel"
test "$(cat "${restore_bin}/telos")" = "previous-telos"
test "$(cat "${restore_bin}/telosd")" = "previous-telosd"
assert_no_stages "${restore_home}"
assert_no_stages "${restore_bin}"

# If the filesystem keeps rejecting restoration moves, the installer reports
# and retains the previous version's backup instead of deleting the only copy.
backup_home="${test_root}/backup-home"
backup_bin="${test_root}/backup-bin"
backup_count="${test_root}/backup-mv-count"
mkdir -p "${backup_home}"
run_installer "${backup_home}" "${backup_bin}" "${test_root}/backup-seed.log"
touch "${backup_home}/.agents/skills/telos-cli/previous-release-sentinel"
if run_installer \
  "${backup_home}" \
  "${backup_bin}" \
  "${test_root}/backup.log" \
  "PATH=${wrapper_bin}:${PATH}" \
  "MV_COUNT_FILE=${backup_count}" \
  "MV_FAIL_FROM=4" \
  "MV_REAL=${real_mv}"; then
  echo "test-release-installer: persistent restoration failure succeeded" >&2
  exit 1
fi
grep -Fq "previous install backup remains" "${test_root}/backup.log"
preserved_backup="$(find "${backup_home}" -type d \
  -name '.telos-cli.*.previous' -print -quit)"
test -n "${preserved_backup}"
test -f "${preserved_backup}/previous-release-sentinel"

# One lock spans download, validation, and activation. A concurrent installer
# fails without disturbing the release that already owns the lock.
concurrent_home="${test_root}/concurrent-home"
concurrent_bin="${test_root}/concurrent-bin"
curl_wrapper_bin="${test_root}/curl-wrapper-bin"
curl_delay_marker="${test_root}/curl-delay-marker"
real_curl="$(command -v curl)"
mkdir -p "${concurrent_home}" "${curl_wrapper_bin}"
printf '%s\n' \
  '#!/usr/bin/env sh' \
  'if [ ! -e "$CURL_DELAY_MARKER" ]; then' \
  '  : >"$CURL_DELAY_MARKER"' \
  '  sleep 1' \
  'fi' \
  'exec "$CURL_REAL" "$@"' >"${curl_wrapper_bin}/curl"
chmod 0755 "${curl_wrapper_bin}/curl"
run_installer \
  "${concurrent_home}" \
  "${concurrent_bin}" \
  "${test_root}/concurrent-first.log" \
  "PATH=${curl_wrapper_bin}:${PATH}" \
  "CURL_DELAY_MARKER=${curl_delay_marker}" \
  "CURL_REAL=${real_curl}" &
first_installer_pid="$!"
lock_attempts=0
while [[ "${lock_attempts}" -lt 30 ]]; do
  if [[ -d "${concurrent_home}/.telos-install.lock" ]]; then
    break
  fi
  sleep 0.1
  lock_attempts="$((lock_attempts + 1))"
done
if [[ ! -d "${concurrent_home}/.telos-install.lock" ]]; then
  echo "test-release-installer: first installer never acquired its lock" >&2
  wait "${first_installer_pid}" || true
  exit 1
fi
if run_installer \
  "${concurrent_home}" \
  "${concurrent_bin}" \
  "${test_root}/concurrent-second.log"; then
  echo "test-release-installer: concurrent installer bypassed the lock" >&2
  wait "${first_installer_pid}" || true
  exit 1
fi
grep -Fq "another Telos installation is active" \
  "${test_root}/concurrent-second.log"
if ! wait "${first_installer_pid}"; then
  cat "${test_root}/concurrent-first.log" >&2
  exit 1
fi
test ! -e "${concurrent_home}/.telos-install.lock"
assert_skill "telos-cli" "${concurrent_home}/.agents/skills/telos-cli"
assert_skill "telos-spec" "${concurrent_home}/.agents/skills/telos-spec"
assert_skill "telos-cli" "${concurrent_home}/.claude/skills/telos-cli"
assert_skill "telos-spec" "${concurrent_home}/.claude/skills/telos-spec"
test -x "${concurrent_bin}/telos"
test -x "${concurrent_bin}/telosd"

echo "release installer checks passed"
