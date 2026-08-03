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
For an explicit internal-only override, set TELOS_ALLOW_UNSIGNED_DARWIN=1.
EOF
  exit 1
fi

gcloud storage buckets describe "gs://${bucket}" --project "${project}" >/dev/null

remote="gs://${bucket}/releases/${version}"
if gcloud storage objects describe "${remote}/manifest.json" >/dev/null 2>&1; then
  verify_dir="$(mktemp -d)"
  trap 'rm -rf "${verify_dir}"' EXIT
  gcloud storage cp "${remote}/SHA256SUMS" "${verify_dir}/SHA256SUMS" >/dev/null
  cmp "${dist}/SHA256SUMS" "${verify_dir}/SHA256SUMS" >/dev/null || {
    echo "publish-release: ${version} already exists with different artifacts" >&2
    exit 1
  }
  echo "publish-release: ${version} already exists with matching checksums"
else
  gcloud storage cp "${dist}/"* "${remote}/" \
    --cache-control="public,max-age=31536000,immutable" \
    --if-generation-match=0
fi

echo "${remote}/manifest.json"
