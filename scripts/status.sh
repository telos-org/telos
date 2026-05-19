#!/usr/bin/env bash
set -euo pipefail

version="${TELOS_VERSION:-}"
if [[ -z "${version}" ]]; then
  if git describe --tags --exact-match >/dev/null 2>&1; then
    version="$(git describe --tags --exact-match)"
  else
    version="v0.0.0-dev.$(git rev-parse --short=12 HEAD)"
  fi
fi

echo "STABLE_TELOS_VERSION ${version}"
echo "STABLE_GIT_COMMIT $(git rev-parse HEAD)"
