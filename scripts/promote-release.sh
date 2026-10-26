#!/usr/bin/env bash
set -euo pipefail

version="${1:?usage: scripts/promote-release.sh VERSION}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"
source="gs://${bucket}/releases/${version}"
target="gs://${bucket}/releases/latest"

verify_dir="$(mktemp -d)"
trap 'rm -rf "${verify_dir}"' EXIT
gcloud storage cp "${source}/manifest.json" "${verify_dir}/manifest.json" >/dev/null
published_version="$(jq -er .version "${verify_dir}/manifest.json")"
if [[ "${published_version}" != "${version}" ]]; then
  echo "promote-release: manifest contains ${published_version}, expected ${version}" >&2
  exit 1
fi

# Consumers resolve the immutable version from manifest.json. Publish that
# manifest last so latest always names a complete, verified release.
artifacts=(
  telos-darwin-amd64 \
  telos-darwin-arm64 \
  telos-linux-amd64 \
  telos-linux-arm64 \
  telosd-darwin-amd64 \
  telosd-darwin-arm64 \
  telosd-linux-amd64 \
  telosd-linux-arm64 \
  telos-cli-skill.tar.gz \
  SHA256SUMS \
  install.sh
)
windows="$(jq -r 'any(.platforms[]; .os == "windows" and .arch == "amd64")' "${verify_dir}/manifest.json")"
if [[ "$windows" == true ]]; then
  artifacts+=(telos-windows-amd64.exe install.ps1)
fi
for artifact in "${artifacts[@]}"; do
  copy_flags=(--cache-control="no-cache,max-age=0")
  if [[ "$artifact" == "install.ps1" ]]; then
    copy_flags+=(--content-type="text/plain; charset=utf-8")
  fi
  gcloud storage cp "${source}/${artifact}" "${target}/${artifact}" "${copy_flags[@]}"
done
if [[ "$windows" == false ]]; then
  obsolete="$(gcloud storage ls "${target}/*" | awk -v prefix="${target}/" '$0 == prefix "telos-windows-amd64.exe" || $0 == prefix "install.ps1"')"
  if [[ -n "$obsolete" ]]; then
    printf '%s\n' "$obsolete" | gcloud storage rm --read-paths-from-stdin
  fi
fi
gcloud storage cp "${source}/manifest.json" "${target}/manifest.json" \
  --cache-control="no-cache,max-age=0"

echo "promoted ${version} to https://usetelos.ai/releases/latest/manifest.json"
