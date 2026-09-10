# syntax=docker/dockerfile:1
FROM node:20-alpine AS ui
WORKDIR /src
COPY ui/package*.json ./ui/
RUN npm ci --prefix ui
COPY ui ./ui
COPY web ./web
# SkillPage imports skills/bce/SKILL.md (?raw) from the repo root
COPY skills ./skills
RUN npm run build --prefix ui

FROM golang:1.24-bookworm AS go-build
# VERSION is injected by the release workflow with the pushed git tag; local
# builds fall back to the in-source default (internal/version).
ARG VERSION=v0.0.1
WORKDIR /src
COPY go.mod go.sum ./
# Cache mounts (GOMODCACHE + GOCACHE) survive layer invalidation, so a source
# change only recompiles affected packages instead of redoing the tree-sitter
# CGo build. In CI they are persisted via buildkit-cache-dance (see workflow).
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
# bce-server needs CGO for the tree-sitter AST chunker; the runtime image's
# glibc (bookworm-slim) matches the build image, so dynamic linking is safe.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X github.com/linqiu919/better-context-engine/internal/version.Version=${VERSION}" -o /out/bce-server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bce-agent ./cmd/agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git wget && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/bce-server /usr/local/bin/bce-server
COPY --from=go-build /out/bce-agent /usr/local/bin/bce-agent
EXPOSE 18181
USER 65532:65532
ENTRYPOINT ["bce-server"]
