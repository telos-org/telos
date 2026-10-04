#!/usr/bin/env sh
set -eu

release_base_url="${TELOS_RELEASE_BASE_URL:-https://usetelos.ai/releases}"
version="@TELOS_VERSION@"
install_dir="${TELOS_INSTALL_DIR:-$HOME/.local/bin}"
agent_skills_dir="${TELOS_AGENT_SKILLS_DIR:-$HOME/.agents/skills}"
install_local="${TELOS_INSTALL_LOCAL:-0}"
case "$install_local" in
  0|1) ;;
  *) echo "telos install: TELOS_INSTALL_LOCAL must be 0 or 1" >&2; exit 1 ;;
esac
if [ -e "$install_dir/telosd" ] || [ -L "$install_dir/telosd" ]; then
  install_local=1
fi
if [ -z "${TELOS_AGENT_SKILLS_DIR:-}" ] && [ -f "$install_dir/.telos-skill-path" ]; then
  agent_skills_dir="$(dirname "$(cat "$install_dir/.telos-skill-path")")"
fi

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "telos install: missing required command: $1" >&2
    exit 1
  fi
}

need curl
need chmod
need tar

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"

case "$os" in
  darwin|linux) ;;
  *)
    echo "telos install: unsupported OS: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "telos install: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

base_url="$release_base_url/$version"
mkdir -p "$install_dir"
install_dir="$(cd "$install_dir" && pwd)"
bin_stage="$(mktemp -d "$install_dir/.telos-install.XXXXXX")"
skill_target=""
skill_stage=""
skill_backup=""
replacing=0
committed=0
cleanup() {
  if [ "$replacing" -eq 1 ] && [ "$committed" -eq 0 ]; then
    if [ -n "$skill_backup" ] && { [ -e "$skill_backup" ] || [ -L "$skill_backup" ]; }; then
      if [ -e "$skill_target" ] || [ -L "$skill_target" ]; then
        rm -rf "$skill_target"
      fi
      mv "$skill_backup" "$skill_target" || return
      skill_backup=""
    elif [ ! -d "$skill_stage" ]; then
      rm -rf "$skill_target"
    fi
    for component in $components; do
      if [ -e "$bin_stage/$component.previous" ] || [ -L "$bin_stage/$component.previous" ]; then
        mv -f "$bin_stage/$component.previous" "$install_dir/$component" || return
      elif [ ! -e "$bin_stage/$component" ]; then
        rm -f "$install_dir/$component"
      fi
    done
  fi
  if [ -n "$skill_stage" ]; then
    rm -rf "$skill_stage"
  fi
  if [ -n "$skill_backup" ]; then
    rm -rf "$skill_backup"
  fi
  rm -rf "$bin_stage"
}
trap cleanup EXIT INT TERM

curl -fsSL "$base_url/SHA256SUMS" -o "$bin_stage/SHA256SUMS"

download_verified() {
  artifact="$1"
  dest="$2"
  curl -fsSL "$base_url/$artifact" -o "$dest"
  expected="$(awk -v file="$artifact" '$2 == file { print $1 }' "$bin_stage/SHA256SUMS")"
  if [ -z "$expected" ]; then
    echo "telos install: checksum missing for $artifact" >&2
    exit 1
  fi
  if command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$dest" | awk '{ print $1 }')"
  elif command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$dest" | awk '{ print $1 }')"
  else
    echo "telos install: missing shasum or sha256sum for verification" >&2
    exit 1
  fi
  if [ "$actual" != "$expected" ]; then
    echo "telos install: checksum verification failed for $artifact" >&2
    exit 1
  fi
}

binaries="telos"
components="telos .telos-skill-path"
if [ "$install_local" -eq 1 ]; then
  binaries="$binaries telosd"
  components="$components telosd"
fi
for binary in $binaries; do
  download_verified "$binary-$os-$arch" "$bin_stage/$binary"
  chmod 0755 "$bin_stage/$binary"
done
download_verified "telos-cli-skill.tar.gz" "$bin_stage/telos-cli-skill.tar.gz"

mkdir -p "$agent_skills_dir"
agent_skills_dir="$(cd "$agent_skills_dir" && pwd)"
skill_target="$agent_skills_dir/telos-cli"
skill_stage="$(mktemp -d "$agent_skills_dir/.telos-cli.XXXXXX")"
tar -xzf "$bin_stage/telos-cli-skill.tar.gz" -C "$skill_stage"
chmod 0755 "$skill_stage"
if [ ! -f "$skill_stage/SKILL.md" ]; then
  echo "telos install: telos-cli skill is missing SKILL.md" >&2
  exit 1
fi
printf '%s\n' "$skill_target" > "$bin_stage/.telos-skill-path"
for component in $components; do
  if [ -d "$install_dir/$component" ]; then
    echo "telos install: cannot replace directory $install_dir/$component" >&2
    exit 1
  fi
  if [ -e "$install_dir/$component" ] || [ -L "$install_dir/$component" ]; then
    ln "$install_dir/$component" "$bin_stage/$component.previous"
  fi
done
replacing=1
if [ -e "$skill_target" ] || [ -L "$skill_target" ]; then
  skill_backup="$skill_stage.previous"
  mv "$skill_target" "$skill_backup"
fi
mv "$skill_stage" "$skill_target"
for component in $components; do
  mv -f "$bin_stage/$component" "$install_dir/$component"
done
committed=1

echo "installed telos $version to $install_dir"
echo "installed @telos/telos-cli:@TELOS_SKILL_VERSION@ to $agent_skills_dir/telos-cli"
if [ "$install_local" -eq 1 ]; then
  echo "installed telosd $version to $install_dir"
fi
if [ "$install_local" -eq 1 ] && ! command -v pi >/dev/null 2>&1; then
  echo "For local Telos runs, install pi with: npm install -g @earendil-works/pi-coding-agent"
  echo "Then run pi and use /login to configure model credentials before your first local run."
  echo "pi setup: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/quickstart.md"
fi
if ! command -v telos >/dev/null 2>&1; then
  echo "add $install_dir to PATH to run telos from any shell"
fi
