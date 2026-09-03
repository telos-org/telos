#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "${repo_root}"

version="${1:-}"
if [[ -z "${version}" ]]; then
  version="$(TELOS_VERSION= scripts/status.sh | awk '/^STABLE_TELOS_VERSION / {print $2}')"
fi
if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.+][0-9A-Za-z.-]+)?$ ]]; then
  echo "build-release: version must look like vMAJOR.MINOR.PATCH, got ${version}" >&2
  exit 1
fi
export TELOS_VERSION="${version}"

dist="${repo_root}/dist/${version}"
rm -rf "${dist}"
mkdir -p "${dist}"
skill_version="${version#v}"
darwin_artifacts=(
  "telos-darwin-amd64"
  "telos-darwin-arm64"
  "telosd-darwin-amd64"
  "telosd-darwin-arm64"
)

bazel build \
  --stamp \
  --workspace_status_command="${repo_root}/scripts/status.sh" \
  //cmd/telos:telos_darwin_amd64 \
  //cmd/telos:telos_darwin_arm64 \
  //cmd/telos:telos_linux_amd64 \
  //cmd/telos:telos_linux_arm64 \
  //cmd/telosd:telosd_darwin_amd64 \
  //cmd/telosd:telosd_darwin_arm64 \
  //cmd/telosd:telosd_linux_amd64 \
  //cmd/telosd:telosd_linux_arm64 \
  //skills:telos_cli_bundle \
  //skills:telos_spec_bundle

copy_binary() {
  local label="$1"
  local artifact="$2"
  local output
  output="$(bazel cquery \
    --stamp \
    --workspace_status_command="${repo_root}/scripts/status.sh" \
    --output=files \
    "${label}")"
  cp "${output}" "${dist}/${artifact}"
  chmod 0755 "${dist}/${artifact}"
}

copy_binary "//cmd/telos:telos_darwin_amd64" "telos-darwin-amd64"
copy_binary "//cmd/telos:telos_darwin_arm64" "telos-darwin-arm64"
copy_binary "//cmd/telos:telos_linux_amd64" "telos-linux-amd64"
copy_binary "//cmd/telos:telos_linux_arm64" "telos-linux-arm64"
copy_binary "//cmd/telosd:telosd_darwin_amd64" "telosd-darwin-amd64"
copy_binary "//cmd/telosd:telosd_darwin_arm64" "telosd-darwin-arm64"
copy_binary "//cmd/telosd:telosd_linux_amd64" "telosd-linux-amd64"
copy_binary "//cmd/telosd:telosd_linux_arm64" "telosd-linux-arm64"
skill_bundle="$(bazel cquery --output=files //skills:telos_cli_bundle)"
cp "${skill_bundle}" "${dist}/telos-cli-skill.tar.gz"
spec_skill_bundle="$(bazel cquery --output=files //skills:telos_spec_bundle)"
cp "${spec_skill_bundle}" "${dist}/telos-spec-skill.tar.gz"

sign_darwin_artifacts() {
  local identity="${TELOS_DARWIN_CODESIGN_IDENTITY:-}"
  if [[ -z "${identity}" ]]; then
    cat >&2 <<EOF
build-release: Darwin artifacts are unsigned.
Set TELOS_DARWIN_CODESIGN_IDENTITY to a Developer ID Application identity before publishing macOS releases.
EOF
    return 0
  fi
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "build-release: Darwin signing requires running on macOS" >&2
    exit 1
  fi
  command -v codesign >/dev/null 2>&1 || {
    echo "build-release: codesign is required for Darwin signing" >&2
    exit 1
  }

  for artifact in "${darwin_artifacts[@]}"; do
    codesign --force --timestamp --options runtime --sign "${identity}" "${dist}/${artifact}"
    codesign --verify --strict --verbose=2 "${dist}/${artifact}"
  done
  touch "${dist}/.darwin-signed"
}

sign_darwin_artifacts

(
  cd "${dist}"
  shasum -a 256 telos-* telosd-* > SHA256SUMS
  cat > install.sh <<EOF
#!/usr/bin/env sh
set -eu

release_base_url="\${TELOS_RELEASE_BASE_URL:-https://usetelos.ai/releases}"
version="${version}"
install_dir="\${TELOS_INSTALL_DIR:-\$HOME/.local/bin}"
agent_skills_dir="\${TELOS_AGENT_SKILLS_DIR:-\$HOME/.agents/skills}"
claude_skills_dir="\${TELOS_CLAUDE_SKILLS_DIR:-\$HOME/.claude/skills}"
install_lock_dir="\${TELOS_INSTALL_LOCK_DIR:-\$HOME/.telos-install.lock}"
claude_install_override="\${TELOS_INSTALL_CLAUDE_SKILLS:-}"
case "\$claude_install_override" in
  ""|1) install_claude_skills=1 ;;
  0) install_claude_skills=0 ;;
  *)
    echo "telos install: TELOS_INSTALL_CLAUDE_SKILLS must be 0 or 1" >&2
    exit 1
    ;;
esac
if [ -z "\$claude_install_override" ] && \
  [ -n "\${TELOS_AGENT_SKILLS_DIR:-}" ] && \
  [ -z "\${TELOS_CLAUDE_SKILLS_DIR:-}" ]; then
  # Preserve the legacy override: a custom agent directory remains the only
  # skill destination unless a Claude directory is also supplied explicitly.
  install_claude_skills=0
fi

need() {
  if ! command -v "\$1" >/dev/null 2>&1; then
    echo "telos install: missing required command: \$1" >&2
    exit 1
  fi
}

need curl
need chmod
need cp
need diff
need grep
need readlink
need tar

os="\$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="\$(uname -m)"

case "\$os" in
  darwin|linux) ;;
  *)
    echo "telos install: unsupported OS: \$os" >&2
    exit 1
    ;;
esac

case "\$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "telos install: unsupported architecture: \$arch" >&2
    exit 1
    ;;
esac

base_url="\$release_base_url/\$version"
tmp_dir="\$(mktemp -d)"
journal_dir="\$tmp_dir/install-journal"
mkdir -p "\$journal_dir"
: >"\$tmp_dir/installed-skill-targets"
: >"\$tmp_dir/preserved-skill-links"
: >"\$tmp_dir/staged-paths"
journal_count=0
install_complete=0
lock_acquired=0

record_artifact() {
  stage="\$1"
  target="\$2"
  retain_backup="\${3:-0}"
  journal_count="\$((journal_count + 1))"
  record="\$journal_dir/\$(printf '%03d' "\$journal_count")"
  mkdir -p "\$record"
  printf '%s\n' "\$stage" >"\$record/stage"
  printf '%s\n' "\$target" >"\$record/target"
  if [ "\$retain_backup" = "1" ]; then
    : >"\$record/retain-backup"
  fi
}

register_stage() {
  printf '%s\n' "\$1" >>"\$tmp_dir/staged-paths"
}

cleanup() {
  cleanup_status="\$?"
  for record in "\$journal_dir"/*; do
    [ -d "\$record" ] || continue
    stage="\$(cat "\$record/stage")"
    target="\$(cat "\$record/target")"
    backup=""
    if [ -f "\$record/backup" ]; then
      backup="\$(cat "\$record/backup")"
    fi
    retained_container=""
    if [ -f "\$record/retained-container" ]; then
      retained_container="\$(cat "\$record/retained-container")"
    fi
    if [ -f "\$record/retained-backup" ]; then
      retained_backup="\$(cat "\$record/retained-backup")"
      if [ -e "\$retained_backup" ] || [ -L "\$retained_backup" ]; then
        backup="\$retained_backup"
      fi
    fi

    if [ "\$install_complete" != "1" ]; then
      if [ -f "\$record/activating" ]; then
        rm -rf "\$target" || true
      fi
      if [ -n "\$backup" ] && { [ -e "\$backup" ] || [ -L "\$backup" ]; }; then
        if [ -e "\$target" ] || [ -L "\$target" ]; then
          rm -rf "\$target" || true
        fi
        if mv "\$backup" "\$target"; then
          backup=""
        else
          echo "telos install: failed to restore \$target; previous install kept at \$backup" >&2
        fi
      fi
    fi

    rm -rf "\$stage" || true
    if [ "\$install_complete" = "1" ] && [ -n "\$backup" ]; then
      if [ -f "\$record/retain-backup" ]; then
        echo "telos install: previous unmarked skill retained at \$backup"
      else
        rm -rf "\$backup" || true
      fi
    elif [ -n "\$backup" ] && { [ -e "\$backup" ] || [ -L "\$backup" ]; }; then
      echo "telos install: previous install backup remains at \$backup" >&2
    fi
    if [ "\$install_complete" != "1" ] && [ -n "\$retained_container" ]; then
      rmdir "\$retained_container" 2>/dev/null || true
    fi
  done
  while IFS= read -r staged_path; do
    rm -rf "\$staged_path" || true
  done <"\$tmp_dir/staged-paths"
  rm -rf "\$tmp_dir" || true
  if [ "\$lock_acquired" = "1" ]; then
    if ! rmdir "\$install_lock_dir"; then
      echo "telos install: could not remove install lock at \$install_lock_dir" >&2
      if [ "\$cleanup_status" = "0" ]; then
        cleanup_status=1
      fi
    fi
  fi
  return "\$cleanup_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if ! mkdir "\$install_lock_dir"; then
  if [ -d "\$install_lock_dir" ]; then
    echo "telos install: another Telos installation is active at \$install_lock_dir" >&2
  else
    echo "telos install: could not create install lock at \$install_lock_dir" >&2
    echo "choose a writable path with TELOS_INSTALL_LOCK_DIR" >&2
  fi
  exit 1
fi
lock_acquired=1

curl -fsSL "\$base_url/SHA256SUMS" -o "\$tmp_dir/SHA256SUMS"

download_verified() {
  artifact="\$1"
  dest="\$2"
  curl -fsSL "\$base_url/\$artifact" -o "\$dest"
  expected="\$(awk -v file="\$artifact" '\$2 == file { print \$1 }' "\$tmp_dir/SHA256SUMS")"
  if [ -z "\$expected" ]; then
    echo "telos install: checksum missing for \$artifact" >&2
    exit 1
  fi
  if command -v shasum >/dev/null 2>&1; then
    actual="\$(shasum -a 256 "\$dest" | awk '{ print \$1 }')"
  elif command -v sha256sum >/dev/null 2>&1; then
    actual="\$(sha256sum "\$dest" | awk '{ print \$1 }')"
  else
    echo "telos install: missing shasum or sha256sum for verification" >&2
    exit 1
  fi
  if [ "\$actual" != "\$expected" ]; then
    echo "telos install: checksum verification failed for \$artifact" >&2
    exit 1
  fi
}

download_verified "telos-\$os-\$arch" "\$tmp_dir/telos"
download_verified "telosd-\$os-\$arch" "\$tmp_dir/telosd"
download_verified "telos-cli-skill.tar.gz" "\$tmp_dir/telos-cli-skill.tar.gz"
download_verified "telos-spec-skill.tar.gz" "\$tmp_dir/telos-spec-skill.tar.gz"

mkdir -p "\$install_dir"
mkdir -p "\$agent_skills_dir"
install_dir="\$(cd "\$install_dir" && pwd -P)"
agent_skills_dir="\$(cd "\$agent_skills_dir" && pwd -P)"

if [ "\$install_claude_skills" = "1" ]; then
  mkdir -p "\$claude_skills_dir"
  claude_skills_dir="\$(cd "\$claude_skills_dir" && pwd -P)"
  if [ "\$claude_skills_dir" = "\$agent_skills_dir" ]; then
    install_claude_skills=0
  fi
fi

validate_skill_dir() {
  skill_dir="\$1"
  skill_name="\$2"
  registry_ref="\$3"
  [ -f "\$skill_dir/SKILL.md" ] && \
    grep -Fxq "name: \$skill_name" "\$skill_dir/SKILL.md" && \
    grep -Fxq "  registry: \"\$registry_ref\"" "\$skill_dir/SKILL.md"
}

is_replaceable_skill_target() {
  skill_dir="\$1"
  skill_name="\$2"
  registry_ref="\$3"

  if [ -f "\$skill_dir/.telos-managed" ] && \
    [ "\$(cat "\$skill_dir/.telos-managed")" = "\$registry_ref" ]; then
    return 0
  fi

  return 1
}

resolved_link_target() {
  link_path="\$1"
  raw_target="\$(readlink "\$link_path")" || return 1
  case "\$raw_target" in
    /*) candidate="\$raw_target" ;;
    *) candidate="\$(dirname "\$link_path")/\$raw_target" ;;
  esac
  candidate_parent="\$(dirname "\$candidate")"
  candidate_name="\$(basename "\$candidate")"
  [ -d "\$candidate_parent" ] || return 1
  printf '%s/%s\n' "\$(cd "\$candidate_parent" && pwd -P)" "\$candidate_name"
}

stage_skill() {
  skills_dir="\$1"
  skill_name="\$2"
  registry_ref="\$3"
  archive="\$4"
  alias_target="\${5:-}"
  target="\$skills_dir/\$skill_name"
  retain_backup=0

  stage="\$(mktemp -d "\$skills_dir/.\$skill_name.XXXXXX")"
  register_stage "\$stage"
  if ! tar -xzf "\$archive" -C "\$stage"; then
    rm -rf "\$stage"
    echo "telos install: failed to extract \$skill_name skill" >&2
    exit 1
  fi
  if ! validate_skill_dir "\$stage" "\$skill_name" "\$registry_ref"; then
    rm -rf "\$stage"
    echo "telos install: \$skill_name archive has invalid skill metadata" >&2
    exit 1
  fi
  printf '%s\n' "\$registry_ref" >"\$stage/.telos-managed"

  if [ -L "\$target" ]; then
    resolved_target="\$(resolved_link_target "\$target" || true)"
    if [ -n "\$alias_target" ] && [ "\$resolved_target" = "\$alias_target" ] && \
      [ ! -L "\$alias_target" ]; then
      rm -rf "\$stage"
      printf '%s\n' "\$target" >>"\$tmp_dir/preserved-skill-links"
      echo "telos install: preserving linked skill managed through \$alias_target"
      return
    fi
    if ! validate_skill_dir "\$target" "\$skill_name" "\$registry_ref" || \
      ! diff -r -x .telos-managed "\$stage" "\$target" >/dev/null 2>&1; then
      rm -rf "\$stage"
      echo "telos install: refusing linked skill that does not match this release at \$target" >&2
      exit 1
    fi
    rm -rf "\$stage"
    printf '%s\n' "\$target" >>"\$tmp_dir/preserved-skill-links"
    echo "telos install: preserving linked skill already at this release at \$target"
    return
  fi
  if [ -e "\$target" ]; then
    if is_replaceable_skill_target "\$target" "\$skill_name" "\$registry_ref"; then
      :
    elif diff -r -x .telos-managed "\$stage" "\$target" >/dev/null 2>&1; then
      # Adopt an exact manual extraction without treating public metadata alone
      # as proof of installer ownership.
      :
    elif [ "\$skill_name" = "telos-cli" ] && \
      validate_skill_dir "\$target" "\$skill_name" "\$registry_ref"; then
      retain_backup=1
      echo "telos install: migrating unmarked legacy telos-cli at \$target"
    else
      rm -rf "\$stage"
      echo "telos install: refusing to replace unrecognized skill at \$target" >&2
      if [ "\$install_claude_skills" = "1" ] && \
        [ "\$skills_dir" = "\$claude_skills_dir" ]; then
        echo "rerun with TELOS_INSTALL_CLAUDE_SKILLS=0 to leave Claude skills unchanged" >&2
      fi
      exit 1
    fi
  fi

  record_artifact "\$stage" "\$target" "\$retain_backup"
  printf '%s\n' "\$target" >>"\$tmp_dir/installed-skill-targets"
}

stage_binary() {
  binary_name="\$1"
  source="\$2"
  target="\$install_dir/\$binary_name"
  stage="\$(mktemp "\$install_dir/.\$binary_name.XXXXXX")"
  register_stage "\$stage"
  cp "\$source" "\$stage"
  chmod 0755 "\$stage"
  record_artifact "\$stage" "\$target"
}

agent_cli_alias=""
agent_spec_alias=""
if [ "\$install_claude_skills" = "1" ]; then
  agent_cli_alias="\$claude_skills_dir/telos-cli"
  agent_spec_alias="\$claude_skills_dir/telos-spec"
fi
stage_skill "\$agent_skills_dir" "telos-cli" "@telos/telos-cli" \
  "\$tmp_dir/telos-cli-skill.tar.gz" "\$agent_cli_alias"
stage_skill "\$agent_skills_dir" "telos-spec" "@telos/telos-spec" \
  "\$tmp_dir/telos-spec-skill.tar.gz" "\$agent_spec_alias"
if [ "\$install_claude_skills" = "1" ]; then
  stage_skill "\$claude_skills_dir" "telos-cli" "@telos/telos-cli" \
    "\$tmp_dir/telos-cli-skill.tar.gz" "\$agent_skills_dir/telos-cli"
  stage_skill "\$claude_skills_dir" "telos-spec" "@telos/telos-spec" \
    "\$tmp_dir/telos-spec-skill.tar.gz" "\$agent_skills_dir/telos-spec"
fi
stage_binary "telos" "\$tmp_dir/telos"
stage_binary "telosd" "\$tmp_dir/telosd"

# All downloads, checksums, skill metadata, destinations, and stages are valid
# before the first installed artifact is replaced. Keep every backup until the
# complete release is active so the EXIT trap can restore the previous set.
for record in "\$journal_dir"/*; do
  [ -d "\$record" ] || continue
  stage="\$(cat "\$record/stage")"
  target="\$(cat "\$record/target")"
  backup="\$stage.previous"
  printf '%s\n' "\$backup" >"\$record/backup"
  if [ -e "\$target" ] || [ -L "\$target" ]; then
    mv "\$target" "\$backup"
  fi
  : >"\$record/activating"
  if ! mv "\$stage" "\$target"; then
    echo "telos install: failed to activate \$target" >&2
    exit 1
  fi
  : >"\$record/activated"
done

# Move retained legacy copies outside agent discovery roots before committing
# the transaction. If relocation fails, the EXIT trap restores every target.
for record in "\$journal_dir"/*; do
  [ -f "\$record/retain-backup" ] || continue
  target="\$(cat "\$record/target")"
  backup="\$(cat "\$record/backup")"
  skills_dir="\$(dirname "\$target")"
  skill_name="\$(basename "\$target")"
  retained_root="\$(dirname "\$skills_dir")/.telos-skill-backups"
  mkdir -p "\$retained_root"
  retained_container="\$(mktemp -d "\$retained_root/\$skill_name.\$version.XXXXXX")"
  retained_backup="\$retained_container/\$skill_name"
  printf '%s\n' "\$retained_container" >"\$record/retained-container"
  printf '%s\n' "\$retained_backup" >"\$record/retained-backup"
  if ! mv "\$backup" "\$retained_backup"; then
    echo "telos install: failed to retain previous \$skill_name outside agent discovery" >&2
    exit 1
  fi
done
install_complete=1

echo "installed telos \$version to \$install_dir"
echo "installed bundled Telos skills for ${skill_version}:"
while IFS= read -r target; do
  echo "  \$target"
done <"\$tmp_dir/installed-skill-targets"
if [ -s "\$tmp_dir/preserved-skill-links" ]; then
  echo "preserved verified skill links:"
  while IFS= read -r target; do
    echo "  \$target"
  done <"\$tmp_dir/preserved-skill-links"
fi
echo 'invoke \$telos-spec in Codex or /telos-spec in Claude Code to write a spec'
if ! command -v pi >/dev/null 2>&1; then
  echo "pi is required for local runs but was not found on PATH"
  echo "install pi with: npm install -g @earendil-works/pi-coding-agent"
  echo "then run pi and use /login to configure model credentials before running telos"
  echo "pi setup: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/quickstart.md"
fi
if ! command -v telos >/dev/null 2>&1; then
  echo "add \$install_dir to PATH to run telos from any shell"
fi
EOF
  chmod 0755 install.sh
  cat > manifest.json <<EOF
{
  "version": "${version}",
  "base_url": "https://usetelos.ai/releases/${version}",
  "skills": [
    {"ref": "@telos/telos-cli:${skill_version}", "artifact": "telos-cli-skill.tar.gz"},
    {"ref": "@telos/telos-spec:${skill_version}", "artifact": "telos-spec-skill.tar.gz"}
  ],
  "platforms": [
    {"os": "darwin", "arch": "amd64", "telos": "telos-darwin-amd64", "telosd": "telosd-darwin-amd64"},
    {"os": "darwin", "arch": "arm64", "telos": "telos-darwin-arm64", "telosd": "telosd-darwin-arm64"},
    {"os": "linux", "arch": "amd64", "telos": "telos-linux-amd64", "telosd": "telosd-linux-amd64"},
    {"os": "linux", "arch": "arm64", "telos": "telos-linux-arm64", "telosd": "telosd-linux-arm64"}
  ]
}
EOF
)

echo "${dist}"
