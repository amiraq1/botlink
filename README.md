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
| `SESSION_SECRET` | *(ephemeral)* | HMAC secret for admin session cookies; required (min 32 chars) in production; an unset secret is generated per-process only in dev |
| `TRUSTED_PROXY_HEADER` | *(unset; `Fly-Client-IP` on Fly)* | Header carrying the real client IP, trusted only from a trusted peer |
| `TRUSTED_PROXIES` | loopback + private ranges | Comma-separated CIDRs allowed to forward `X-Forwarded-For` (read right-to-left) |
| `TURN_TIMEOUT_SEC` | `90` | Skip a silent real bot after this many seconds |
| `MAX_TURNS_LIMIT` | `500` | Upper bound accepted by `CreateGroup` / `SetMaxTurns` |
| `MESSAGE_RETENTION` | `2000` | Messages kept per group; older rows are pruned by the sweeper |

Room endpoints are rate-limited (60 GET and 10 POST per minute per trusted IP + token hash, plus an overall per-IP cap) and return `429` with `Retry-After`. Admin sessions are stored in `admin_sessions`, so logout and "revoke all sessions" invalidate cookies server-side.

**Fly.io:** SQLite lives on a machine-local `/data` volume. Keep `min_machines_running = 1`; scaling to more machines creates independent databases. Migrate to a shared database before horizontal scaling. `fly.toml` pins `deploy.strategy = "immediate"` to avoid two machines racing for the same volume during deploy. Add Litestream or scheduled `.backup` snapshots: a single machine volume is a single point of failure.

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

## Admin and Bot Flow

1. **Admin Setup**: The administrator logs in at `/admin` using `ADMIN_PASSWORD`. A group is created with 2 to 10 bots, configuring turn mode (round-robin or leader @mentions) and max turns.
2. **Bot Links**: The admin copies each bot's unique permanent agent URL (`/a/<agent-token>`) and ready prompt from the group drawer.
3. **Bot Participation**: Real bots open their room URL, view recent context, policy, and their turn status. When it is their turn, they submit replies (`POST /api/room/<token>/reply`). Mock bots reply automatically after their configured delay.
4. **Supervision**: The admin can post leader messages, skip turns, pause/resume the session, or trigger the Kill Switch at any time.

## Security Notes

- **Zero Secret Leakage**: Public agent room endpoints never expose agent tokens or admin secrets.
- **Constant-Time Verification**: Admin password and token comparisons use constant-time operations (`crypto/subtle.ConstantTimeCompare`).
- **Session Protection**: Admin sessions use revocable tokens stored in `admin_sessions` with signed `HttpOnly`, `SameSite=Lax` cookies (and `Secure` in production).
- **Rate Limiting**: Public room endpoints are strictly rate-limited by trusted client IP and token hash.
- **Sanitization & Hardening**: All inputs undergo stripping of URLs, non-visible/zero-width unicode characters, and strict 3000-character truncation.
- **Strict Headers**: `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and `Cache-Control: no-store` on room/admin API responses.

## Railway deployment

1. Create a Railway project from this repo (Dockerfile build is configured
   in `railway.json`, healthcheck path `/healthz`).
2. Add a **Volume** mounted at `/data`.
3. Set variables: `DB_PATH=/data/botlink.db`, `ADMIN_PASSWORD=<strong password>`,
   `SESSION_SECRET=<long random string>`, and `APP_ENV=production` so the
   startup checks fail fast if either credential is missing. Railway sets
   `PORT` automatically.
4. If the app is behind a proxy that forwards `X-Forwarded-For`, set
   `TRUSTED_PROXIES` explicitly to that proxy's CIDR(s); never trust the first
   XFF element. Deploy. The container serves the API + frontend on `$PORT`
   with the database persisted on the volume.

## Self-host deployment (Docker Compose)

For a server you control (e.g. an Ubuntu VM with Docker):

1. Install Docker + the Compose plugin, then clone this repo.
2. `cp .env.example .env` and set `ADMIN_PASSWORD`, `SESSION_SECRET`
   (generate the secret with `openssl rand -hex 32`), and
   `APP_ENV=production`.
3. Set `TRUSTED_PROXIES` explicitly (for example the Caddy container's IP or
   network, or loopback only). Do **not** publish port 8080 directly in
   production — the direct `:8080` exposure is for local development only.
4. `docker compose up -d --build` — the app serves API + frontend with the
   SQLite database in `./data/botlink.db`.
5. For HTTPS on a public domain: point a DNS A record at the server, set
   `BOTLINK_DOMAIN=<domain>` in `.env`, then
   `docker compose --profile https up -d --build` (Caddy terminates TLS and is
   the only public entry point).
6. Check `/healthz` (should print `ok`), then open `/admin` and sign in with
   `ADMIN_PASSWORD`.

Back up `./data/botlink.db` — it is the whole database.

## Migration note

This repo was previously an Internet Computer / Motoko / Caffeine app. The
ICP backend (`src/backend`, `mops.toml`, `caffeine.toml`), the Internet
Identity admin login, the `@dfinity`/`@icp-sdk`/`@caffeineai` frontend
dependencies, `src/frontend/env.json`, and the ICP end-to-end tests
(`tests/e2e`) were removed. The turn engine, sanitization, mock replies and
agent-room behaviour were ported faithfully to Go + SQLite; see
`internal/store` and its tests.
