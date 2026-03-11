#!/usr/bin/env bash
# BujiCoder OSS — Uninstall Script
# Usage: curl -fsSL https://raw.githubusercontent.com/TechnoAllianceAE/bujicoder/main/scripts/uninstall.sh | bash
set -euo pipefail

BINARY="buji"
CONFIG_DIR="${HOME}/.bujicoder"

info()  { printf '\033[1;34m▸\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m⚠\033[0m %s\n' "$*"; }

main() {
    printf '\n\033[1m  BujiCoder Uninstaller\033[0m\n\n'

    local removed=0

    # Remove binary from common install locations
    for dir in /usr/local/bin "${HOME}/.local/bin"; do
        local binary="${dir}/${BINARY}"
        if [ -f "$binary" ]; then
            if [ -w "$dir" ]; then
                rm -f "$binary"
                info "Removed ${binary}"
                removed=1
            elif command -v sudo >/dev/null 2>&1; then
                sudo rm -f "$binary"
                info "Removed ${binary} (sudo)"
                removed=1
            else
                warn "Cannot remove ${binary} — permission denied. Try: sudo rm ${binary}"
            fi
        fi
    done

    if [ "$removed" = "0" ]; then
        info "Binary '${BINARY}' not found in /usr/local/bin or ~/.local/bin"
    fi

    # Ask about config directory
    if [ -d "$CONFIG_DIR" ]; then
        printf '\n'
        warn "Config directory found: ${CONFIG_DIR}"
        printf '  Contains: API keys, conversations, logs, permissions.\n'
        printf '  Remove it? [y/N] '

        if [ -t 0 ]; then
            read -r answer
            case "$answer" in
                [yY]|[yY][eE][sS])
                    rm -rf "$CONFIG_DIR"
                    info "Removed ${CONFIG_DIR}"
                    ;;
                *)
                    info "Kept ${CONFIG_DIR}"
                    ;;
            esac
        else
            printf 'skipped (non-interactive)\n'
            info "Kept ${CONFIG_DIR} — remove manually with: rm -rf ${CONFIG_DIR}"
        fi
    fi

    printf '\n'
    ok "BujiCoder has been uninstalled."
    printf '\n'
}

main "$@"
