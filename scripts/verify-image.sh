#!/usr/bin/env bash
# Checks that every platform of a charmap image runs a binary built for that
# platform. The binary reports its own GOOS/GOARCH, so an image carrying
# another platform's binary fails here even when emulation would run it.
#
# Usage: scripts/verify-image.sh IMAGE_REF
# VERIFY_WITH_BUILDKIT=true executes through BuildKit's bundled emulators.
# PLATFORMS overrides the platforms checked (default: linux/amd64 linux/arm64).
set -euo pipefail

ref=${1:?usage: verify-image.sh IMAGE_REF}
read -r -a platforms <<<"${PLATFORMS:-linux/amd64 linux/arm64}"

if [[ ${VERIFY_WITH_BUILDKIT:-false} == true ]]; then
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  cat >"$scratch/Dockerfile" <<'DOCKERFILE'
ARG IMAGE
FROM ${IMAGE}
RUN ["/usr/local/bin/charmap", "version"]
DOCKERFILE
fi

for platform in "${platforms[@]}"; do
  if [[ ${VERIFY_WITH_BUILDKIT:-false} == true ]]; then
    # RUN uses BuildKit's user-mode emulator without host binfmt registration.
    # Disabling cache ensures the binary executes for every verification.
    if ! docker buildx build --no-cache --platform "$platform" \
      --build-arg "IMAGE=$ref" --progress plain --output type=cacheonly \
      "$scratch" >"$scratch/build.log" 2>&1; then
      cat "$scratch/build.log" >&2
      exit 1
    fi
    # Read RUN stdout only; platform names also occur in build metadata.
    out=$(sed -nE 's/^#[0-9]+ [0-9.]+ (charmap .*)$/\1/p' "$scratch/build.log")
  else
    # The classic image store holds only one platform per reference.
    docker image rm --force "$ref" >/dev/null 2>&1 || true
    out=$(docker run --rm --pull always --platform "$platform" "$ref" version)
  fi
  echo "$platform: $out"
  if [[ $out != *" $platform" ]]; then
    echo "error: $ref for $platform reports '${out##* }'" >&2
    exit 1
  fi
done
if [[ ${VERIFY_WITH_BUILDKIT:-false} != true ]]; then
  docker image rm --force "$ref" >/dev/null 2>&1 || true
fi
