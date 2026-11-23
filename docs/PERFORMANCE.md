# BOTLINK — Performance Report (Before vs After)

Measured on the same machine (Windows 11, i7-1255U, 12 threads) with Go 1.27.4 and Node 24 /
pnpm 10.26.1. The frontend comparison uses production builds.

> **Measurement caveat.** This laptop is a contended environment: identical benchmarks varied
> 2–10× between runs (a browser kept 4–6 cores busy and SQLite temp files were being scanned).
> Allocation counts, byte counts, query counts and bundle sizes are deterministic and are the
> primary evidence below. Timings are **best-observed** values; directional ratios were confirmed
> with back-to-back runs of the original code from a temporary `git worktree`. Re-run the
> benchmarks on the target host for authoritative wall-clock numbers.

---

## 1. Frontend bundle

Baseline was a single unminified bundle containing the public agent room **and** the whole admin
surface. All four pages are now lazy routes, React/TanStack are split into their own chunks, and the
40 unreachable `ui/*` primitives were removed.

| Artifact | Before (raw / gzip) | After (raw / gzip) | Δ gzip |
| --- | --- | --- | --- |
| Public room graph (`/a/$token`, 10 chunks) | 1,009,515 / 206,069 B | 334,731 / 106,464 B | **−48.3 %** |
| Admin chat graph (`/admin/g/$id`, 16 chunks) | 1,009,515 / 206,069 B | 423,200 / 134,611 B | **−34.7 %** |
| CSS | 106,978 / 17,594 B | 38,804 / 8,013 B | **−54.5 %** |
| `index.html` | 931 / 421 B | 1,173 / 483 B | +15 % (modulepreload graph) |

**Total first-load weight (JS + CSS, gzip):**

| Route | Before | After | Δ |
| --- | --- | --- | --- |
| `/a/$token` | 223,663 B | 114,477 B | **−48.8 %** |
| `/admin/g/$id` | 223,663 B | 142,624 B | **−36.2 %** |

The room's transitive chunk graph contains **no** `AdminGate`, `AdminDashboardPage`,
`GroupChatPage`, or admin query hooks (verified by walking the built import graph).

**Dependency pruning:** 205 packages removed from `node_modules`. Removed runtime deps: `three`,
`@react-three/*`, `react-quill-new`, `recharts`, `embla-carousel-react`, `vaul`, `cmdk`,
`input-otp`, `react-day-picker`, `react-resizable-panels`, `react-hook-form`, `sonner`,
`next-themes`, `react-icons`, `zustand`, `date-fns`, `react-use`, `motion`, plus 22 unused
`@radix-ui/*` packages. `build.minify` was `false`; it is now `"oxc"`.

---

## 2. SQLite & turn engine (`internal/store`)

Deterministic metrics (independent of machine load):

| Operation | SQL queries/op | Allocs/op | Bytes/op |
| --- | --- | --- | --- |
| `ListGroups` (50 groups × 5 members) | **51 → 1** | 5,590 → **696** (−87.5 %) | 251,429 → **34,024** (−86.5 %) |
| `GetGroupDetailPaged` (500 msgs) | **6 → 4** | 1,785 → **1,614** (−9.6 %) | 75,398 → **68,978** (−8.5 %) |
| `GetRoomState` | **6 → 4**, +0 writes | 627 → **446** (−28.9 %) | 23,783 → **17,314** (−27.2 %) |
| `SubmitBotReply` (stale CAS) | 3 | 251 → 257 (+2.4 %) | 8,768 → 9,223 (+5.2 %) |
| `PruneOldMessages` (per 15 s tick) | **1 + N deletes → 0 idle / 1 when changed** | — | — |

Best-observed wall clock:

| Benchmark | Before | After | Notes |
| --- | --- | --- | --- |
| `ListGroups` | 1.84 ms | **0.34 ms** | 5.4×; paired runs ranged 2–6× |
| `GetGroupDetailPaged` | 480 µs | 496 µs | parity; 2 fewer queries, 8.5 % fewer bytes |
| `GetRoomState` | 253 µs | 203 µs | best case; parity within noise |
| `SubmitBotReply` | 201 µs | 184 µs | parity (now presence-aware) |
| `SubmitBotReplyParallel` (2 s window) | 47.6 µs | 50.9 µs | parity at equal bench time |

Structural changes:

- `ListGroups` N+1 replaced by one `LEFT JOIN … GROUP BY` aggregate (`member_count`, `online_count`).
- `PruneOldMessages` uses a single `ROW_NUMBER() OVER (PARTITION BY group_id …)` delete, gated by
  `MAX(id)` so an idle tick does **zero** work; previously one `NOT IN` delete per group per tick.
- Presence never touches the writer on the read path: touches are coalesced in memory (30 s/token)
  and flushed by a batched 1 s background transaction. Turn routing folds queued presence in, so a
  bot that just polled is immediately online for the next-speaker decision.
- Mock replies quote with `SELECT … ORDER BY id DESC LIMIT 1` instead of loading the whole history.
- `Sanitize` precomputes link patterns/marker and avoids redundant rune conversions.
- Mention matching lowercases the message once per leader message, not once per member.
- Change notifications are collected inside the write transaction and published **after COMMIT**
  (prevents a woken scheduler from reading the pre-commit snapshot; this was found by a flaky test).
- Turn routing reads are `loadMembersLive`; admin detail and room reads no longer select
  `agent_token` on the anonymous path.

**Indexes:** added `bot_members(group_id, is_mock)`, `groups(updated_at DESC, id DESC)`,
`groups(status)`, `admin_sessions(expires_at)`; dropped the redundant `bot_members(group_id, bot_id)`
(the PK autoindex covers it) and the unique `agent_token` index is unchanged.
**PRAGMA tuning:** `temp_store=MEMORY`, `cache_size=-20000`, `mmap_size=67108864` are applied to the
**read pool only** (per connection, via DSN). Setting them on the writer measurably slowed small
commits (~30 µs and ~35 % on the presence-heavy write path), so the writer keeps
WAL / FKs / busy_timeout / `synchronous=NORMAL`. Read pool size is `clamp(NumCPU, 4, 16)`.

---

## 3. HTTP layer (`internal/server`)

Full-handler benchmarks, same request shape:

| Endpoint | Response bytes before | After | Δ | CPU note |
| --- | --- | --- | --- | --- |
| `GET /api/room/{token}` | 2,923 | **381** | **−87.0 %** | gzip adds ≈56 µs/response |
| `GET /api/admin/groups/{id}` | 19,638 | **1,277** | **−93.5 %** | gzip adds ≈0.5 ms/response |
| SPA `index.html` | 2,417 | **159** | **−93.4 %** | one-time per version (no-cache) |

Gzip uses `gzip.BestSpeed`, a 512-byte threshold (tiny JSON stays raw), skips SSE/HEAD/Range and
already-compressed assets, and sets `Vary: Accept-Encoding`.

Caching:

- `/assets/*` → `Cache-Control: public, max-age=31536000, immutable` (hashed names).
- `index.html` and the SPA fallback → `Cache-Control: no-cache`.
- `GET /api/room/{token}` and `GET /api/admin/groups/{id}` return an `ETag` derived from the visible
  state (no JSON serialization to decide) plus `Cache-Control: private, no-cache`, and answer
  `If-None-Match` with an empty `304`.

Real-time (new):

| Measurement | Result |
| --- | --- |
| SSE change frame after a mutation (`/api/admin/groups/{id}/events`) | **2.97 ms** (Go test, localhost) |
| SSE handshake | `retry: 3000`, `event: ready`, then coalesced `event: change` |
| Frames leaked tokens | none (admin and room streams tested) |
| Room stream, unknown token | `404` |
| Subscriber cap | `503` + `Retry-After` |
| Scheduler precision, 1 s mock delay | **1.0058 s** (no reads involved) |
| Live smoke test | replying as Alpha advanced 1→3 with Mocky's auto-reply within 1.5 s |

SSE carries only `{"group":<id>}`. Admin streams re-validate the session on every 20 s heartbeat and
close on revocation; streams are capped at 2 minutes and clients reconnect via the `retry:` hint.
Per-write deadlines via `http.ResponseController` let streams outlive the server's 15 s
`WriteTimeout`; `Server.Close()` ends streams before graceful shutdown.

---

## 4. Real-time UI behavior

- Admin chat: SSE pulse → debounced cache invalidation (first pulse fires immediately) → delta
  refetch. Fallback polling drops from 2.5 s to 15 s while the stream is open.
- Agent room: SSE pulse → immediate state refetch; the required `Check Again / Wait (18s)` button and
  its 3 s bounded polling loop are unchanged and remain the automatic fallback.
- Optimistic updates: pause/resume/stop (status), skip (turn clock), turn mode, max turns, and leader
  messages (negative-id bubble, dropped by the merge once the server answers; rollback on error).
- Message list: memoized bubbles, memoized divider build, scroll only when the list grows, and
  `content-visibility: auto` for off-screen rows (up to 500 cached messages).

---

## 5. Verification

| Gate | Result |
| --- | --- |
| `gofmt -l cmd internal` | empty |
| `go vet ./...` | pass |
| `go test ./...` | pass (store + server; 30+ tests) |
| `go test -race ./...` | **not runnable on this Windows host** (no C toolchain; `-race requires cgo`). CI runs it on ubuntu, and the new code uses mutexes/channels/atomics reviewed for the race detector. |
| `pnpm typecheck` | pass |
| `pnpm check` (Biome) | pass |
| `pnpm test` | 10/10 |
| `pnpm build` | pass |

New Go tests cover: aggregate counts, window prune across groups, `NextDue`/`ProcessDueGroup`
(mock + timeout), presence batching/coalescing, per-connection read-pool PRAGMAs, ETag/304 on both
endpoints, gzip JSON + skip rules, static cache headers, admin/room SSE (token leakage, 401/404/503),
and scheduler precision. New frontend tests cover delta merging and optimistic patches.

---

## 6. Reproducing

```sh
# Go
gofmt -l cmd internal
go vet ./...
go test ./...
go test -bench=. -benchmem -run=NoSuchTest ./internal/...

# Frontend
pnpm install
pnpm typecheck && pnpm check && pnpm test && pnpm build
```

Fresh benchmarks live in `internal/store/bench_test.go` and `internal/server/bench_test.go`.
For a fair comparison, run the benchmark files against `git worktree add <dir> HEAD` and alternate
runs; keep `-benchtime` at 2 s (short windows are dominated by warm-up).
