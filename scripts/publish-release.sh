#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-}"
bucket="${TELOS_RELEASE_BUCKET:-telos-runtime-artifacts}"

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

"${repo_root}/scripts/test-release-installer.sh" "${dist}" "${version}"

if [[ -z "${TELOS_AUTH_TOKEN:-}" ]]; then
  echo "publish-release: TELOS_AUTH_TOKEN is required to publish bundled skills" >&2
  exit 1
fi
command -v jq >/dev/null 2>&1 || {
  echo "publish-release: jq is required" >&2
  exit 1
}

case "$(uname -s)-$(uname -m)" in
  Darwin-x86_64) publisher="${dist}/telos-darwin-amd64" ;;
  Darwin-arm64) publisher="${dist}/telos-darwin-arm64" ;;
  Linux-x86_64) publisher="${dist}/telos-linux-amd64" ;;
  Linux-aarch64|Linux-arm64) publisher="${dist}/telos-linux-arm64" ;;
  *)
    echo "publish-release: unsupported publisher host $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

skill_version="${version#v}"
published_ref=""
published_digest=""

publish_skill() {
  local skill_name="$1"
  local skill_dir="${repo_root}/skills/${skill_name}"
  local expected_ref="@telos/${skill_name}:${skill_version}"
  local skill_json
  local published_version

  skill_json="$(
    "${publisher}" push "${skill_dir}" \
      --scope telos \
      --version "${skill_version}" \
      --json
  )"
  published_ref="$(jq -er '.skill.ref' <<<"${skill_json}")"
  published_digest="$(jq -er '.skill.digest' <<<"${skill_json}")"
  published_version="$(jq -er '.skill.version' <<<"${skill_json}")"
  if [[ "${published_ref}" != "${expected_ref}" || "${published_version}" != "${skill_version}" ]]; then
    echo "publish-release: registry returned ${published_ref}, expected ${expected_ref}" >&2
    exit 1
  fi
  if [[ ! "${published_digest}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    echo "publish-release: registry returned invalid skill digest ${published_digest}" >&2
    exit 1
  fi
}

publish_skill "telos-cli"
cli_skill_ref="${published_ref}"
cli_skill_digest="${published_digest}"
publish_skill "telos-spec"
spec_skill_ref="${published_ref}"
spec_skill_digest="${published_digest}"

manifest_tmp="${dist}/manifest.json.tmp"
jq \
  --arg cli_ref "${cli_skill_ref}" \
  --arg cli_digest "${cli_skill_digest}" \
  --arg spec_ref "${spec_skill_ref}" \
  --arg spec_digest "${spec_skill_digest}" \
  '.skills = [
    {ref: $cli_ref, digest: $cli_digest, artifact: "telos-cli-skill.tar.gz"},
    {ref: $spec_ref, digest: $spec_digest, artifact: "telos-spec-skill.tar.gz"}
  ]' \
  "${dist}/manifest.json" >"${manifest_tmp}"
mv "${manifest_tmp}" "${dist}/manifest.json"

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
  telos-cli-skill.tar.gz \
  telos-spec-skill.tar.gz \
  SHA256SUMS \
  install.sh
do
  publish_immutable "${artifact}"
done
publish_immutable manifest.json

echo "${remote}/manifest.json"
