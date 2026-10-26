# The builder runs on the build host's platform and cross-compiles, so
# multi-platform builds need no emulation and each image gets its own binary.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/charmap /usr/local/bin/charmap
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/charmap"]
CMD ["help"]
