# syntax=docker/dockerfile:1
# BOTLINK — Go + SQLite unified server (REST API + React frontend)
# Multi-stage: Node/pnpm frontend build -> Go static binary -> minimal Alpine.

# ---- Stage 1: build the React frontend -------------------------------------
FROM node:20.19.0-bookworm-slim AS frontend
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@10.26.1 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY src/frontend/package.json src/frontend/
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --filter botlink-frontend...
COPY src/frontend src/frontend
RUN pnpm --filter botlink-frontend build

# ---- Stage 2: build the Go server (pure Go, no CGO) ------------------------
FROM golang:1.27.4-bookworm AS gobuild
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd cmd
COPY internal internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /botlink ./cmd/server

# ---- Stage 3: minimal runtime ----------------------------------------------
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 botlink
WORKDIR /app
COPY --from=frontend /app/src/frontend/dist ./frontend/dist
COPY --from=gobuild /botlink ./botlink
RUN chown -R botlink:botlink /app && mkdir -p /data && chown botlink:botlink /data
USER botlink
ENV PORT=8080 \
    DB_PATH=/data/botlink.db \
    FRONTEND_DIST_DIR=/app/frontend/dist
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["./botlink"]
