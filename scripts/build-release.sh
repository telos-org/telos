#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "${repo_root}"

version="${1:-}"
if [[ -z "${version}" ]]; then
  version="$(TELOS_VERSION= scripts/status.sh | awk '/^STABLE_TELOS_VERSION / {print $2}')"
fi
export TELOS_VERSION="${version}"

dist="${repo_root}/dist/${version}"
rm -rf "${dist}"
mkdir -p "${dist}"

bazel build \
  --stamp \
  --workspace_status_command="${repo_root}/scripts/status.sh" \
  //cmd/telos:telos_darwin_amd64 \
  //cmd/telos:telos_darwin_arm64 \
  //cmd/telos:telos_linux_amd64 \
  //cmd/telos:telos_linux_arm64

copy_binary() {
  local target="$1"
  local artifact="$2"
  local output
  output="$(bazel cquery \
    --stamp \
    --workspace_status_command="${repo_root}/scripts/status.sh" \
    --output=files \
    "//cmd/telos:${target}")"
  cp "${output}" "${dist}/${artifact}"
  chmod 0755 "${dist}/${artifact}"
}

copy_binary "telos_darwin_amd64" "telos-darwin-amd64"
copy_binary "telos_darwin_arm64" "telos-darwin-arm64"
copy_binary "telos_linux_amd64" "telos-linux-amd64"
copy_binary "telos_linux_arm64" "telos-linux-arm64"

(
  cd "${dist}"
  shasum -a 256 telos-* > SHA256SUMS
  cat > manifest.json <<EOF
{
  "version": "${version}",
  "base_url": "https://storage.googleapis.com/telos-runtime-artifacts/releases/${version}",
  "artifacts": [
    {"os": "darwin", "arch": "amd64", "name": "telos-darwin-amd64"},
    {"os": "darwin", "arch": "arm64", "name": "telos-darwin-arm64"},
    {"os": "linux", "arch": "amd64", "name": "telos-linux-amd64"},
    {"os": "linux", "arch": "arm64", "name": "telos-linux-arm64"}
  ]
}
EOF
)

echo "${dist}"
