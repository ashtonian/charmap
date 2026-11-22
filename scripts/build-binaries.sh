#!/usr/bin/env bash
# Cross-compiles charmap on the build host into OUT_DIR/<os>/<arch>/charmap,
# the per-platform layout the Dockerfile copies from, so an image build
# compiles nothing and runs nothing under emulation.
#
# Usage: scripts/build-binaries.sh OUT_DIR VERSION PLATFORM...
#   scripts/build-binaries.sh build/image dev linux/amd64 linux/arm64
#   docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile -t charmap:dev build/image
set -euo pipefail

out=${1:?usage: build-binaries.sh OUT_DIR VERSION PLATFORM...}
version=${2?usage: build-binaries.sh OUT_DIR VERSION PLATFORM...}
shift 2
if (($# == 0)); then
  echo "usage: build-binaries.sh OUT_DIR VERSION PLATFORM..." >&2
  exit 2
fi
cd "$(dirname "$0")/.."
for platform in "$@"; do
  if [[ ! $platform =~ ^[a-z0-9]+/[a-z0-9]+$ ]]; then
    echo "error: platform must be os/arch, got '$platform'" >&2
    exit 2
  fi
  CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} \
    go build -trimpath -ldflags="-s -w -X main.version=${version}" -o "$out/$platform/charmap" .
done
