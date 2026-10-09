#!/usr/bin/env sh
set -eu

install_telosd=0
for option in "$@"; do
  case "$option" in
    --with-telosd) install_telosd=1 ;;
    --help|-h)
      cat <<'EOF'
Usage: install.sh [--with-telosd] [--help]

Install the Telos CLI and bundled skill. An existing telosd is also updated.

  --with-telosd  Include the daemon for local execution.
  --help, -h    Show this help.

Environment:
  TELOS_INSTALL_DIR       Binary directory (default: $HOME/.local/bin).
  TELOS_AGENT_SKILLS_DIR  Skill directory (default: $HOME/.agents/skills).
                         Reinstalls remember the previous skill directory.
EOF
      exit 0
      ;;
    *)
      echo "telos install: unknown option: $option; use --help for usage" >&2
      exit 1
      ;;
  esac
done

release_base_url="${TELOS_RELEASE_BASE_URL:-https://usetelos.ai/releases}"
version="@TELOS_VERSION@"
install_dir="${TELOS_INSTALL_DIR:-$HOME/.local/bin}"
agent_skills_dir="${TELOS_AGENT_SKILLS_DIR:-$HOME/.agents/skills}"
if [ -e "$install_dir/telosd" ] || [ -L "$install_dir/telosd" ]; then
  install_telosd=1
fi
if [ -z "${TELOS_AGENT_SKILLS_DIR:-}" ] && [ -f "$install_dir/.telos-skill-path" ]; then
  recorded_skill="$(cat "$install_dir/.telos-skill-path")"
  case "$recorded_skill" in
    /telos-cli|/*/telos-cli) agent_skills_dir="$(dirname "$recorded_skill")" ;;
    *)
      echo "telos install: invalid installed skill path; set TELOS_AGENT_SKILLS_DIR to its parent directory" >&2
      exit 1
      ;;
  esac
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

# Follow symlinks so a linked installation is updated where it points and
# keeps its link, as `telos update` does.
resolve() {
  path="$1"
  hops=0
  while [ -L "$path" ]; do
    hops=$((hops + 1))
    if [ "$hops" -gt 40 ]; then
      echo "telos install: too many symlinks at $1" >&2
      exit 1
    fi
    link="$(readlink "$path")"
    case "$link" in
      /*) path="$link" ;;
      *) path="$(dirname "$path")/$link" ;;
    esac
  done
  printf '%s\n' "$path"
}

base_url="$release_base_url/$version"
mkdir -p "$install_dir"
install_dir="$(cd "$install_dir" && pwd)"
bin_stage="$(mktemp -d "$install_dir/.telos-install.XXXXXX")"
skill_target=""
skill_stage=""
skill_previous=""
cleanup() {
  # A skill swap interrupted between its two renames puts the previous skill back.
  if [ -n "$skill_previous" ] && [ -e "$skill_previous" ] && [ ! -e "$skill_target" ] && [ ! -L "$skill_target" ]; then
    mv "$skill_previous" "$skill_target" || true
  fi
  for record in "$bin_stage"/stage-*; do
    if [ -f "$record" ]; then
      rm -f "$(cat "$record")"
    fi
  done
  if [ -n "$skill_stage" ]; then
    rm -rf "$skill_stage"
  fi
  if [ -n "$skill_previous" ]; then
    rm -rf "$skill_previous"
  fi
  rm -rf "$bin_stage"
}
# Signals exit through the EXIT trap so cleanup runs exactly once.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

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

# Components are replaced in this order. The CLI goes last, so a failed
# replacement leaves a working CLI to install again with.
components=".telos-skill-path telos"
if [ "$install_telosd" -eq 1 ]; then
  components=".telos-skill-path telosd telos"
  download_verified "telosd-$os-$arch" "$bin_stage/telosd"
fi
download_verified "telos-$os-$arch" "$bin_stage/telos"
download_verified "telos-cli-skill.tar.gz" "$bin_stage/telos-cli-skill.tar.gz"

mkdir -p "$agent_skills_dir"
agent_skills_dir="$(cd "$agent_skills_dir" && pwd)"
skill_target="$(resolve "$agent_skills_dir/telos-cli")"
skill_stage="$(mktemp -d "$(dirname "$skill_target")/.telos-cli.XXXXXX")"
tar -xzf "$bin_stage/telos-cli-skill.tar.gz" -C "$skill_stage"
chmod 0755 "$skill_stage"
if [ ! -f "$skill_stage/SKILL.md" ]; then
  echo "telos install: telos-cli skill is missing SKILL.md" >&2
  exit 1
fi
printf '%s\n' "$agent_skills_dir/telos-cli" > "$bin_stage/.telos-skill-path"

# Stage each file beside its target, so replacing it is one atomic rename.
for component in $components; do
  target="$(resolve "$install_dir/$component")"
  if [ -d "$target" ]; then
    echo "telos install: cannot replace directory $target" >&2
    exit 1
  fi
  staged="$(mktemp "$(dirname "$target")/.telos-install.XXXXXX")"
  printf '%s\n' "$staged" > "$bin_stage/stage-$component"
  printf '%s\n' "$target" > "$bin_stage/target-$component"
  cp "$bin_stage/$component" "$staged"
  if [ "$component" = .telos-skill-path ]; then
    chmod 0644 "$staged"
  else
    chmod 0755 "$staged"
  fi
done

# A directory cannot be renamed over another, so the previous skill moves
# aside first; cleanup puts it back if the new one does not take its place.
if [ -e "$skill_target" ] || [ -L "$skill_target" ]; then
  skill_previous="$skill_stage.previous"
  mv "$skill_target" "$skill_previous"
fi
mv "$skill_stage" "$skill_target"
rm -rf "$skill_previous"
skill_previous=""
for component in $components; do
  target="$(cat "$bin_stage/target-$component")"
  if ! mv -f "$(cat "$bin_stage/stage-$component")" "$target"; then
    echo "telos install: could not replace $target; run the installer again to finish" >&2
    exit 1
  fi
done

echo "installed telos $version to $install_dir"
echo "installed @telos/telos-cli:@TELOS_SKILL_VERSION@ to $agent_skills_dir/telos-cli"
if [ "$install_telosd" -eq 1 ]; then
  echo "installed telosd $version to $install_dir"
fi
if [ "$install_telosd" -eq 1 ] && ! command -v pi >/dev/null 2>&1; then
  echo "For local Telos runs, install pi with: npm install -g @earendil-works/pi-coding-agent"
  echo "Then run pi and use /login to configure model credentials before your first local run."
  echo "pi setup: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/quickstart.md"
fi
if ! command -v telos >/dev/null 2>&1; then
  path_dir="$install_dir"
  if [ -n "${HOME:-}" ] && [ "$HOME" != / ]; then
    case "$install_dir" in
      "$HOME"/*) path_dir="\$HOME${install_dir#"$HOME"}" ;;
    esac
  fi
  echo
  case "${SHELL##*/}" in
    fish)
      echo "To run telos from any shell, run: fish_add_path $path_dir"
      ;;
    *)
      case "${SHELL##*/}" in
        zsh) profile="~/.zshrc" ;;
        bash)
          if [ "$(uname -s)" = Darwin ]; then profile="~/.bash_profile"; else profile="~/.bashrc"; fi
          ;;
        *) profile="~/.profile" ;;
      esac
      path_line="export PATH=\"$path_dir:\$PATH\""
      echo "To run telos from any shell, add it to your PATH:"
      echo "  echo '$path_line' >> $profile"
      echo "Then open a new terminal, or run: $path_line"
      ;;
  esac
fi
