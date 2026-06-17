#!/usr/bin/env bash
# rundeck-per-step installer.
#   curl -fsSL https://raw.githubusercontent.com/justynroberts/rundeck-per-step/main/install.sh | bash
# Options (env vars):
#   PREFIX=/usr/local/bin          install location (default: ~/.local/bin)
#   VERSION=v0.1.0                 specific release tag (default: latest)
set -euo pipefail

REPO="justynroberts/rundeck-per-step"
PREFIX="${PREFIX:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
BIN="rundeck-per-step"

err() { echo "error: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  *) err "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) err "unsupported arch: $(uname -m)" ;;
esac

ASSET="${BIN}-${OS}-${ARCH}"
[[ "$OS" == "windows" ]] && ASSET="${ASSET}.exe"

if [[ "$VERSION" == "latest" ]]; then
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

mkdir -p "$PREFIX"
DEST="${PREFIX}/${BIN}"
[[ "$OS" == "windows" ]] && DEST="${DEST}.exe"

echo "downloading ${URL}"
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar -o "$DEST" "$URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress -O "$DEST" "$URL"
else
  err "need curl or wget"
fi

chmod +x "$DEST"
echo
echo "installed: $DEST"
case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) echo "note: $PREFIX is not on PATH. Add this to your shell rc:"
     echo "      export PATH=\"$PREFIX:\$PATH\"" ;;
esac

echo
"$DEST" --help 2>&1 | head -3 || true
