FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY . .
RUN go mod download

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /charmap .

FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates

COPY --from=builder /charmap /usr/local/bin/charmap

WORKDIR /work
ENTRYPOINT ["charmap"]
CMD ["-h"]
