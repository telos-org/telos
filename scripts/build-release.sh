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
  sed -e "s/@TELOS_VERSION@/${version}/g" \
    -e "s/@TELOS_SKILL_VERSION@/${skill_version}/g" \
    "${repo_root}/scripts/install-release.sh" > install.sh
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
