# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -buildid=" \
      -o /out/archivesinavideo ./cmd/archivesinavideo

FROM --platform=$BUILDPLATFORM alpine:latest AS runtime-files

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 65532 -S saveweb && \
    adduser -u 65532 -S -D -H -G saveweb saveweb && \
    mkdir -p /app/warcs /app/temp && \
    chown -R 65532:65532 /app

FROM alpine:latest

COPY --from=runtime-files /etc/passwd /etc/passwd
COPY --from=runtime-files /etc/group /etc/group
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-files --chown=65532:65532 /app /app

WORKDIR /app
COPY --from=build /out/archivesinavideo /usr/local/bin/archivesinavideo

LABEL org.opencontainers.image.title="sinavideo" \
      org.opencontainers.image.description="Saveweb archive worker for Sina Video" \
      org.opencontainers.image.source="https://github.com/saveweb/sinavideo"

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/archivesinavideo"]
