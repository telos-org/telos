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
  //skills:telos_cli_bundle

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

need() {
  if ! command -v "\$1" >/dev/null 2>&1; then
    echo "telos install: missing required command: \$1" >&2
    exit 1
  fi
}

need curl
need chmod
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
skill_target=""
skill_stage=""
skill_backup=""
cleanup() {
  if [ -n "\$skill_backup" ] && { [ -e "\$skill_backup" ] || [ -L "\$skill_backup" ]; } && \
     [ -n "\$skill_target" ] && [ ! -e "\$skill_target" ] && [ ! -L "\$skill_target" ]; then
    mv "\$skill_backup" "\$skill_target" || true
  fi
  if [ -n "\$skill_stage" ]; then
    rm -rf "\$skill_stage"
  fi
  if [ -n "\$skill_backup" ]; then
    rm -rf "\$skill_backup"
  fi
  rm -rf "\$tmp_dir"
}
trap cleanup EXIT INT TERM

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

mkdir -p "\$install_dir"
chmod 0755 "\$tmp_dir/telos"
chmod 0755 "\$tmp_dir/telosd"
mv "\$tmp_dir/telos" "\$install_dir/telos"
mv "\$tmp_dir/telosd" "\$install_dir/telosd"

mkdir -p "\$agent_skills_dir"
skill_target="\$agent_skills_dir/telos-cli"
skill_stage="\$(mktemp -d "\$agent_skills_dir/.telos-cli.XXXXXX")"
tar -xzf "\$tmp_dir/telos-cli-skill.tar.gz" -C "\$skill_stage"
if [ ! -f "\$skill_stage/SKILL.md" ]; then
  echo "telos install: telos-cli skill is missing SKILL.md" >&2
  exit 1
fi
if [ -e "\$skill_target" ] || [ -L "\$skill_target" ]; then
  skill_backup="\$skill_stage.previous"
  mv "\$skill_target" "\$skill_backup"
fi
mv "\$skill_stage" "\$skill_target"
skill_stage=""
if [ -n "\$skill_backup" ]; then
  rm -rf "\$skill_backup"
  skill_backup=""
fi

echo "installed telos \$version to \$install_dir"
echo "installed @telos/telos-cli:${skill_version} to \$agent_skills_dir/telos-cli"
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
    {"ref": "@telos/telos-cli:${skill_version}", "artifact": "telos-cli-skill.tar.gz"}
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
