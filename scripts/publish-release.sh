#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"
project="${TELOS_GCP_PROJECT:?set TELOS_GCP_PROJECT to the GCP project that owns the release bucket}"
location="${TELOS_GCS_LOCATION:-us-west1}"

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

if ! gcloud storage buckets describe "gs://${bucket}" --project "${project}" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${bucket}" \
    --project "${project}" \
    --location "${location}" \
    --uniform-bucket-level-access
fi

gcloud storage cp "${dist}/"* "gs://${bucket}/releases/${version}/" \
  --cache-control="public,max-age=31536000,immutable"

gcloud storage cp "${dist}/telos-"* "gs://${bucket}/releases/latest/" \
  --cache-control="no-cache,max-age=0"
gcloud storage cp "${dist}/telosd-"* "gs://${bucket}/releases/latest/" \
  --cache-control="no-cache,max-age=0"
gcloud storage cp "${dist}/SHA256SUMS" "gs://${bucket}/releases/latest/SHA256SUMS" \
  --cache-control="no-cache,max-age=0"
gcloud storage cp "${dist}/manifest.json" "gs://${bucket}/releases/latest/manifest.json" \
  --cache-control="no-cache,max-age=0"
gcloud storage cp "${dist}/install.sh" "gs://${bucket}/releases/latest/install.sh" \
  --cache-control="no-cache,max-age=0"

gcloud storage buckets add-iam-policy-binding "gs://${bucket}" \
  --member=allUsers \
  --role=roles/storage.objectViewer \
  --project "${project}" >/dev/null

echo "https://usetelos.ai/releases/${version}/manifest.json"
