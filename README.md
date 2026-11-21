# BOTLINK

A multi-bot group relay platform featuring an Arabic RTL dark Admin UI and
hardened agent rooms — now a standalone **Go + SQLite** server that serves
both the REST API and the React/Vite frontend from one binary, ready to
deploy to Railway via Docker.

## Features
- Multi-bot orchestration (2–10 bots)
- Round-robin and leader turn modes
- Hardened public rooms (`/a/:token`)
- Full admin controls (skip, pause, resume, Kill Switch, max turns, token rotation)
- Mock members that reply automatically after the mock delay

## Stack
- **Backend:** Go 1.27, pure-Go SQLite (`modernc.org/sqlite`, `CGO_ENABLED=0`),
  WAL mode, `foreign_keys = ON`, `busy_timeout = 5000`
- **Frontend:** React 19 + Vite, Arabic RTL dark admin UI
- **Deploy:** multi-stage `Dockerfile`, `railway.json` (healthcheck `/healthz`)

## Local development

Prerequisites: Go 1.27+, Node 20.19+, pnpm 10.26.1.

```bash
# Install frontend dependencies
pnpm install

# Build the frontend (output: src/frontend/dist)
pnpm build

# Run the server (serves the API + the built frontend on :8080)
ADMIN_PASSWORD='choose-a-strong-password' go run ./cmd/server
```

Then open http://localhost:8080 — sign in as admin with the password, or
open a public agent room at `/a/<agent-token>`.

For frontend-only development with hot reload, run the Go server above and
in another terminal:

```bash
pnpm --filter botlink-frontend dev   # Vite dev server on :5173, /api proxied to :8080
```

## Commands

| Command | What it does |
|---|---|
| `pnpm install` | Install frontend dependencies |
| `pnpm typecheck` | TypeScript verification |
| `pnpm check` / `pnpm fix` | Biome lint / format |
| `pnpm build` | Production frontend build |
| `gofmt -l cmd internal` | Go formatting check (must print nothing) |
| `go vet ./...` | Go vet |
| `go test -race ./...` | Go unit tests (turn engine, sanitization, auth, token-leak) |
| `go build -o botlink ./cmd/server` | Build the server binary |

## Configuration (environment variables)

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `./data/botlink.db` | SQLite file; parent dirs are created automatically |
| `FRONTEND_DIST_DIR` | `./src/frontend/dist` | Built React assets (SPA fallback to `index.html`) |
| `ADMIN_PASSWORD` | *(unset)* | **Required for admin login** — login is disabled until it is set |
| `SESSION_SECRET` | *(ephemeral)* | HMAC secret for admin session cookies; set a long random value in production (an unset secret is generated per-process and sessions do not survive restarts) |

## API summary

- `GET /healthz` — `200 OK`
- `POST /api/auth/login` `{password}` — sets an `HttpOnly`, `SameSite=Lax`
  admin session cookie (HMAC-SHA256 signed, `Secure` behind HTTPS)
- `POST /api/auth/logout`, `GET /api/auth/me`
- `GET /api/room/{token}` — public room state (never contains agent tokens)
- `POST /api/room/{token}/reply` `{expectedTurnSeq, content}` —
  `{accepted: true|false}`; stale/duplicate submits return `accepted: false`
  with HTTP 200
- `GET/POST /api/admin/groups`, `GET /api/admin/groups/{id}` — admin only
- `POST /api/admin/groups/{id}/leader-message|skip|pause|resume|stop`
- `POST /api/admin/groups/{id}/turn-mode|/max-turns`
- `POST /api/admin/groups/{id}/members`, `DELETE .../members/{botId}`
- `POST /api/admin/groups/{id}/members/{botId}/rotate-token` — issues a fresh
  32-hex agent token; the old token stops working immediately

## Railway deployment

1. Create a Railway project from this repo (Dockerfile build is configured
   in `railway.json`, healthcheck path `/healthz`).
2. Add a **Volume** mounted at `/data`.
3. Set variables: `DB_PATH=/data/botlink.db`, `ADMIN_PASSWORD=<strong password>`,
   `SESSION_SECRET=<long random string>`. Railway sets `PORT` automatically.
4. Deploy. The container serves the API + frontend on `$PORT` with the
   database persisted on the volume.

## Migration note

This repo was previously an Internet Computer / Motoko / Caffeine app. The
ICP backend (`src/backend`, `mops.toml`, `caffeine.toml`), the Internet
Identity admin login, the `@dfinity`/`@icp-sdk`/`@caffeineai` frontend
dependencies, `src/frontend/env.json`, and the ICP end-to-end tests
(`tests/e2e`) were removed. The turn engine, sanitization, mock replies and
agent-room behaviour were ported faithfully to Go + SQLite; see
`internal/store` and its tests.
