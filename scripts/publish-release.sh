#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"
project="${TELOS_GCP_PROJECT:-telos-experiments}"
location="${TELOS_GCS_LOCATION:-us-west1}"

if [[ -z "${version}" ]]; then
  dist="$("${repo_root}/scripts/build-release.sh")"
  version="$(basename "${dist}")"
else
  dist="$("${repo_root}/scripts/build-release.sh" "${version}")"
fi

if ! gcloud storage buckets describe "gs://${bucket}" --project "${project}" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${bucket}" \
    --project "${project}" \
    --location "${location}" \
    --uniform-bucket-level-access
fi

gcloud storage cp "${dist}/"* "gs://${bucket}/releases/${version}/" \
  --cache-control="public,max-age=31536000,immutable"

gcloud storage cp "${dist}/manifest.json" "gs://${bucket}/releases/latest/manifest.json" \
  --cache-control="public,max-age=60"
gcloud storage cp "${dist}/SHA256SUMS" "gs://${bucket}/releases/latest/SHA256SUMS" \
  --cache-control="public,max-age=60"

gcloud storage buckets add-iam-policy-binding "gs://${bucket}" \
  --member=allUsers \
  --role=roles/storage.objectViewer \
  --project "${project}" >/dev/null

echo "https://storage.googleapis.com/${bucket}/releases/${version}/manifest.json"
