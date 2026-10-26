# The builder runs on the build host's platform and cross-compiles, so
# multi-platform builds need no emulation and each image gets its own binary.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=""
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/charmap .

# No shell or package manager: copy the binary into the image that runs it,
# e.g. COPY --from=docker.io/ashtonian/charmap:2 /usr/local/bin/charmap /usr/local/bin/
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/charmap /usr/local/bin/charmap
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/charmap"]
CMD ["help"]
