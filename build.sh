#!/usr/bin/env bash
# Cross-compile rundeck-per-step for all supported platforms.
# Outputs to ./dist/. Set VERSION env var to override the embedded version string.
set -euo pipefail

cd "$(dirname "$0")"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

mkdir -p dist
rm -f dist/*

declare -a TARGETS=(
  "darwin amd64"
  "darwin arm64"
  "linux  amd64"
  "linux  arm64"
  "windows amd64"
)

for t in "${TARGETS[@]}"; do
  read -r GOOS GOARCH <<<"$t"
  out="rundeck-per-step-${GOOS}-${GOARCH}"
  [[ "$GOOS" == "windows" ]] && out="${out}.exe"
  echo "→ building ${GOOS}/${GOARCH}"
  GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "dist/${out}" .
done

(cd dist && shasum -a 256 rundeck-per-step-* > SHA256SUMS)
echo
echo "built version: ${VERSION}"
ls -lh dist/
