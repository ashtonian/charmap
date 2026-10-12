#!/usr/bin/env bash
# Checks that every platform of a charmap image runs a binary built for that
# platform. The binary reports its own GOOS/GOARCH, so an image carrying
# another platform's binary fails here even when emulation would run it.
#
# Usage: scripts/verify-image.sh IMAGE_REF
# PLATFORMS overrides the platforms checked (default: linux/amd64 linux/arm64).
set -euo pipefail

ref=${1:?usage: verify-image.sh IMAGE_REF}
read -r -a platforms <<<"${PLATFORMS:-linux/amd64 linux/arm64}"

for platform in "${platforms[@]}"; do
  # Docker's classic image store keeps one local image per reference, so the
  # previous platform's copy must go before this platform's can be pulled
  # ("cannot overwrite digest").
  docker image rm --force "$ref" >/dev/null 2>&1 || true
  out=$(docker run --rm --pull always --platform "$platform" "$ref" version)
  echo "$platform: $out"
  if [[ $out != *" $platform" ]]; then
    echo "error: $ref for $platform reports '${out##* }'" >&2
    exit 1
  fi
done
docker image rm --force "$ref" >/dev/null 2>&1 || true
