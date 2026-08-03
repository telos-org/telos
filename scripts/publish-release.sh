#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"
project="${TELOS_GCP_PROJECT:?set TELOS_GCP_PROJECT to the GCP project that owns the release bucket}"

if [[ -z "${version}" ]]; then
  dist="$("${repo_root}/scripts/build-release.sh")"
  version="$(basename "${dist}")"
else
  dist="$("${repo_root}/scripts/build-release.sh" "${version}")"
fi

if [[ ! -f "${dist}/.darwin-signed" && "${TELOS_ALLOW_UNSIGNED_DARWIN:-}" != "1" ]]; then
  cat >&2 <<EOF
publish-release: refusing to publish unsigned Darwin artifacts.
Set TELOS_DARWIN_CODESIGN_IDENTITY to a Developer ID Application identity and rebuild.
For an explicit unsigned-publication override, set TELOS_ALLOW_UNSIGNED_DARWIN=1.
EOF
  exit 1
fi

remote="gs://${bucket}/releases/${version}"
verify_dir="$(mktemp -d)"
trap 'rm -rf "${verify_dir}"' EXIT

publish_immutable() {
  local artifact="$1"
  if gcloud storage objects describe "${remote}/${artifact}" >/dev/null 2>&1; then
    gcloud storage cp "${remote}/${artifact}" "${verify_dir}/${artifact}" >/dev/null
    cmp "${dist}/${artifact}" "${verify_dir}/${artifact}" >/dev/null || {
      echo "publish-release: ${version}/${artifact} already differs" >&2
      exit 1
    }
    return
  fi
  gcloud storage cp "${dist}/${artifact}" "${remote}/${artifact}" \
    --cache-control="public,max-age=31536000,immutable" \
    --if-generation-match=0
}

# The manifest is the release commit point. Upload it only after every artifact
# exists and matches, making a partially failed publication safe to retry.
for artifact in \
  telos-darwin-amd64 \
  telos-darwin-arm64 \
  telos-linux-amd64 \
  telos-linux-arm64 \
  telosd-darwin-amd64 \
  telosd-darwin-arm64 \
  telosd-linux-amd64 \
  telosd-linux-arm64 \
  SHA256SUMS \
  install.sh
do
  publish_immutable "${artifact}"
done
publish_immutable manifest.json

echo "${remote}/manifest.json"
