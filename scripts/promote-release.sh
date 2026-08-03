#!/usr/bin/env bash
set -euo pipefail

version="${1:?usage: scripts/promote-release.sh VERSION}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"
project="${TELOS_GCP_PROJECT:?set TELOS_GCP_PROJECT to the GCP project that owns the release bucket}"
source="gs://${bucket}/releases/${version}"
target="gs://${bucket}/releases/latest"

verify_dir="$(mktemp -d)"
trap 'rm -rf "${verify_dir}"' EXIT
gcloud storage cp "${source}/manifest.json" "${verify_dir}/manifest.json" >/dev/null
published_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "${verify_dir}/manifest.json")"
if [[ "${published_version}" != "${version}" ]]; then
  echo "promote-release: manifest contains ${published_version}, expected ${version}" >&2
  exit 1
fi

# Consumers resolve the immutable version from manifest.json. Publish that
# manifest last so latest always names a complete, verified release.
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
  gcloud storage cp "${source}/${artifact}" "${target}/${artifact}" \
    --cache-control="no-cache,max-age=0"
done
gcloud storage cp "${source}/manifest.json" "${target}/manifest.json" \
  --cache-control="no-cache,max-age=0"

echo "promoted ${version} to https://usetelos.ai/releases/latest/manifest.json"
