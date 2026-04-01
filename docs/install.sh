#!/usr/bin/env bash
# BujiCoder installer — downloads the latest release binary for macOS or Linux.
# Usage: curl -fsSL https://community.bujicoder.com/install.sh | bash
set -euo pipefail

REPO="TechnoAllianceAE/bujicoder"
BINARY="buji"
INSTALL_DIR="${BUJICODER_INSTALL_DIR:-$HOME/.local/bin}"

# ── Helpers ──────────────────────────────────────────────────────────────────

info()  { printf '\033[1;34m▸\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
err()   { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || err "Required tool '$1' not found. Please install it and try again."
}

# ── Detect platform ─────────────────────────────────────────────────────────

detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    linux)  os="linux" ;;
    darwin) os="darwin" ;;
    *)      err "Unsupported OS: $os — use Windows installer from GitHub Releases." ;;
  esac

  case "$arch" in
    x86_64|amd64)   arch="amd64" ;;
    arm64|aarch64)  arch="arm64" ;;
    *)              err "Unsupported architecture: $arch" ;;
  esac

  PLATFORM="${os}_${arch}"
}

# ── Fetch latest release tag ────────────────────────────────────────────────

fetch_latest_version() {
  need curl
  local url="https://api.github.com/repos/${REPO}/releases/latest"
  VERSION="$(curl -fsSL "$url" | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')"
  [ -n "$VERSION" ] || err "Could not determine latest release version."
}

# ── Download & install ──────────────────────────────────────────────────────

install_binary() {
  local asset="${BINARY}_${PLATFORM}"
  local download_url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  local tmp
  tmp="$(mktemp -d)"
  trap "rm -rf \"$tmp\"" EXIT

  info "Downloading BujiCoder ${VERSION} for ${PLATFORM}…"
  curl -fsSL -o "${tmp}/${BINARY}" "$download_url" || err "Download failed — check that a release exists for ${PLATFORM}."

  chmod +x "${tmp}/${BINARY}"

  mkdir -p "$INSTALL_DIR"
  mv "${tmp}/${BINARY}" "${INSTALL_DIR}/${BINARY}"

  ok "Installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"
}

# ── Ensure $INSTALL_DIR is on PATH ──────────────────────────────────────────

check_path() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) return ;;
  esac

  local shell_name
  shell_name="$(basename "${SHELL:-bash}")"
  local rc=""
  case "$shell_name" in
    zsh)  rc="$HOME/.zshrc" ;;
    bash) rc="$HOME/.bashrc" ;;
    fish) rc="$HOME/.config/fish/config.fish" ;;
  esac

  printf '\n'
  printf '\033[1;33m⚠\033[0m  %s is not in your PATH.\n' "$INSTALL_DIR"
  if [ -n "$rc" ]; then
    printf '   Add it by running:\n\n'
    if [ "$shell_name" = "fish" ]; then
      printf '     fish_add_path %s\n\n' "$INSTALL_DIR"
    else
      printf '     echo '\''export PATH="%s:$PATH"'\'' >> %s && source %s\n\n' "$INSTALL_DIR" "$rc" "$rc"
    fi
  else
    printf '   Add %s to your PATH to use buji from anywhere.\n\n' "$INSTALL_DIR"
  fi
}

# ── Main ────────────────────────────────────────────────────────────────────

main() {
  printf '\n\033[1m  BujiCoder Installer\033[0m\n\n'

  detect_platform
  fetch_latest_version
  install_binary
  check_path

  printf '\n  Run \033[1mbuji\033[0m to get started. 🚀\n\n'
}

main "$@"
