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
| `TOKEN_ENCRYPTION_KEY` | *(required; min 32 chars)* | AES-256-GCM encryption key for agent tokens at rest; startup fails if missing or < 32 bytes unless `BOTLINK_DEV=1` is set |
| `BOTLINK_DEV` | `0` | Set to `1` only in local development to permit an ephemeral fallback encryption key |
| `CLIENT_IP_HEADER` | *(unset; `X-Real-IP` on Railway)* | When set, client identity is taken strictly from this header (e.g. `X-Real-IP` on Railway). When unset or absent, falls back to direct connection |
| `TRUSTED_PROXY_HEADER` | *(unset; `Fly-Client-IP` on Fly)* | Header carrying the real client IP, trusted only from a trusted peer |
| `TRUSTED_PROXIES` | `127.0.0.1/32, ::1/128` | Comma-separated CIDRs allowed to forward `X-Forwarded-For`; defaults to loopback only |
| `TURN_TIMEOUT_SEC` | `90` | Skip a silent real bot after this many seconds |
| `MAX_TURNS_LIMIT` | `500` | Upper bound accepted by `CreateGroup` / `SetMaxTurns` |
| `MESSAGE_RETENTION` | `2000` | Messages kept per group; older rows are pruned by the sweeper |

Room endpoints are rate-limited (60 GET and 10 POST per minute per trusted IP + token hash, plus an overall per-IP cap) and return `429` with `Retry-After`. Scanning non-existent tokens quickly triggers a bad-token rate limiter (15 bad tokens per minute per IP) to thwart brute-force attempts. Admin sessions are stored in `admin_sessions` with 24-hour idle expiration and 7-day absolute lifetime; logout and "revoke all sessions" invalidate cookies server-side immediately.

### Admin Login Rate Limiting & Progressive Delay

- **Progressive Delay**: To defeat brute-force dictionary attacks while preventing Denial of Service (locking the administrator out), login attempts undergo progressive backoff after 5 consecutive failures across any IP: 1s, 2s, 4s, 8s, 16s, capped at 30s.
- **No Administrator Lockout**: An attacker cannot permanently lock out the administrator. A login attempt with the correct `ADMIN_PASSWORD` waits out the current delay, authenticates successfully with HTTP 200, and immediately resets the global failure counter back to 0.
- **Recovery Procedure**: To clear the delay, either log in with the correct password (waiting up to 30s for the delay to elapse), or restart the single-instance container process (the delay counter is held in memory).


### Single-Instance Architecture

> [!IMPORTANT]
> BOTLINK runs as a **single replica/instance** (1 container process). Pure-Go SQLite in WAL mode serializes mutations through a single writer connection, and the event broker, turn scheduler, coalesced presence queue, and fixed-window rate limiters run in-memory within that process. Do **not** horizontally scale to multiple containers or replicas without external coordination.

### Reverse Proxy and IP Extraction

When running behind reverse proxies (Railway, Fly, Docker Caddy, Cloudflare):
- `clientIP` inspects the immediate connection address (`RemoteAddr`).
- Forwarding headers (`X-Forwarded-For`) are trusted **only** if the immediate connection is in `TRUSTED_PROXIES` (defaults safely to loopback `127.0.0.1/32`, `::1/128`).
- **Railway Configuration:** Railway does not publish fixed proxy CIDR ranges, and internal `100.x` addresses are dynamic. Instead, Railway's public edge proxy replaces `X-Real-IP` on every incoming request. On Railway, set `CLIENT_IP_HEADER=X-Real-IP`. Note: requests routed over Railway private networking (`*.railway.internal`) bypass edge proxy sanitization, so the service must not be exposed to untrusted internal services.
- When trusted, `X-Forwarded-For` is parsed from right to left, discarding proxy hops until the first untrusted client IP is found. Forged initial hops from untrusted clients are never used.
- For dedicated proxy headers (e.g. `Fly-Client-IP` or `CF-Connecting-IP`), set `TRUSTED_PROXY_HEADER`.
- **Shared IP Protection:** Bad token brute-force limiting throttles only invalid/unknown token probes. Legitimate bots sharing an IP (behind corporate NATs, VPNs, or shared proxies) with valid tokens are never locked out.

### Token Encryption Key Management & Rotation

- **Startup Validation:** `TOKEN_ENCRYPTION_KEY` must be at least 32 characters in production. Startup fails immediately if it is absent or too short, unless `BOTLINK_DEV=1` is explicitly set.
- **AAD & Random Nonce:** Each token is encrypted using AES-256-GCM with a unique cryptographically random 96-bit nonce and Additional Authenticated Data bound to the member ID (`member:<botId>`), preventing ciphertext swapping between bots.
- **Key Loss Recovery:** If `TOKEN_ENCRYPTION_KEY` is lost, previously encrypted tokens cannot be decrypted to display in the admin drawer. However, bots can continue communicating as long as they retain their agent URL (since `token_hash` matches). To restore admin link visibility, use the admin **Rotate Token** button (`POST /api/admin/groups/{id}/members/{botId}/rotate-token`) to issue fresh keys.
- **Key Rotation Procedure:** To rotate `TOKEN_ENCRYPTION_KEY`, export existing tokens via `GET /api/admin/groups/{id}` using the current key, restart the server with the new key, and update the database, or rotate member tokens individually through the admin UI.

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
- **Hashed & Encrypted Tokens**: Agent tokens are stored as SHA-256 hashes for fast lookup (`token_hash`) and encrypted at rest using AES-256-GCM (`token_enc`). The plaintext token column is completely emptied.
- **Constant-Time Verification**: Admin password and token comparisons use constant-time operations (`crypto/subtle.ConstantTimeCompare`).
- **Session & CSRF Protection**: Admin sessions use revocable tokens stored in `admin_sessions` with 24-hour idle expiry, 7-day absolute lifetime, signed `HttpOnly`, `SameSite=Strict` cookies (`Secure` behind HTTPS), and strict `Sec-Fetch-Site` / `Origin` verification on mutating routes.
- **Rate Limiting**: Public room endpoints are strictly rate-limited by trusted client IP and token hash (60 GET/min, 10 POST/min, 180 IP/min), with bad token brute-force protection (15 bad tokens/min).
- **Sanitization & Hardening**: All inputs undergo stripping of URLs, non-visible/bidi/zero-width unicode characters, and strict 3000-rune truncation without mid-rune cuts.
- **Strict Headers**: `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy`, `Cache-Control: private, no-cache` on public rooms (ETag-compatible), `Cache-Control: no-store` with `Vary: Cookie` on admin endpoints, and `Cache-Control: no-cache, no-transform` on SSE streams.

## Railway deployment

1. Create a Railway project from this repo (Dockerfile build is configured
   in `railway.json`, healthcheck path `/healthz`).
2. Add a **Volume** mounted at `/data`.
3. Set variables: `DB_PATH=/data/botlink.db`, `ADMIN_PASSWORD=<strong password>`,
   `SESSION_SECRET=<long random string>`, `TOKEN_ENCRYPTION_KEY=<32+ char key>`,
   `CLIENT_IP_HEADER=X-Real-IP`, and `APP_ENV=production` so the
   startup checks fail fast if any credential is missing. Railway sets
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
