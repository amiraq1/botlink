# Managed agents — Phases 1–3 (storage, provider, runner)

A managed agent is a group member whose replies the server writes. It has no
room token, so no browser session can speak for it. Phase 1 added the storage
and identity model, Phase 2 the DeepSeek client and connection API, and Phase 3
the runner that claims a turn and stores one reply.

## What the runner guarantees

- **Assignment is identity.** A run is keyed by `(group_id, bot_id,
  turn_epoch)`. The claim is re-validated under the write lock at start, and the
  stored reply is accepted only while the same epoch, the same `turn_seq` and
  the same current speaker still hold. A skip cycle that brings the turn back to
  the same bot creates a new epoch, so an old run can neither block it nor
  answer it.
- **No silent restart.** One turn assignment has at most one run. A failed,
  cancelled or interrupted run stays terminal; only the explicit admin retry
  endpoint appends `attempt+1` with `retry_of`. Startup marks every run left
  open by the previous process `interrupted`.
- **Turn actions cancel in the same transaction.** Pause, Skip, Stop/kill and
  the silent-turn timeout update the turn state and cancel open run rows inside
  one `BEGIN IMMEDIATE` transaction. The server then cancels the in-flight HTTP
  request. A provider reply that arrives afterwards cannot be accepted.
- **The reply and the run transition are atomic.** `SubmitManagedAgentReply`
  inserts the message, advances the turn and records usage/cost in one
  transaction. A failed write stores nothing.
- **The turn clock is the deadline.** `turn_started_at + TURN_TIMEOUT_SEC` is
  the deadline for both the scheduler and the runner, so time spent queued
  counts. The runner makes no provider call when the turn deadline leaves less
  than half a second.
- **Bounded concurrency.** One run per group is in flight; a global semaphore
  (`AGENT_MAX_CONCURRENCY`, default 2, max 16) bounds all groups. A turn that
  cannot get a slot stays `queued` and is re-checked on the next tick, still
  under its original deadline.
- **Untrusted text, no tools.** Other members' messages, the leader's messages
  and system events are framed in the prompt and system rules as data, not
  instructions. Requests contain no tools, tool output or images, and a provider
  response with a tool call is rejected. Nothing the model returns can execute
  an external action in this phase.
- **Safe errors.** Runs store a short stable code and a fixed message. The API
  key is redacted from provider text and is never logged, stored on a run or
  returned by the admin API.

## Limits

| Limit | Source | Enforcement |
| --- | --- | --- |
| Turn deadline (includes queue wait) | `TURN_TIMEOUT_SEC`, default 90s | Runner context deadline + scheduler skip |
| Global concurrent provider calls | `AGENT_MAX_CONCURRENCY`, default 2 | Runner semaphore |
| Runs per group | — | Single-flight per group |
| Runs per agent per rolling 24h | `daily_run_limit` (1–1000) | Refused at claim time |
| Context messages | `context_max_messages` (1–100) | Newest N messages |
| Context characters | `context_max_chars` (500–100000) | Oldest history trimmed |
| Output tokens | `max_output_tokens` (64–8192) | `max_tokens` sent to the provider |
| Stored reply | 8000 runes | Sanitized and capped by the store |

## Budget policy

The optional daily budget is checked twice: when the run is claimed and again
immediately before the provider request. The second check excludes the run's own
queued row, so the `queued → running` transition cannot block itself as
"cost unknown".

- With no budget set, runs are not cost-limited.
- With a budget set and every cost in the window known, a run starts only while
  the known spend is below the cap.
- With a budget set and any cost in the window unknown (provider omitted usage,
  no stored price, or a cancelled/interrupted request that may still be
  billed), new runs are refused with `cost_unknown`. An unverifiable cap is
  treated as reached.
- A refusal at start records the run `failed` with `budget_reached` or
  `cost_unknown` and no provider request is made.

`GET …/agents/{botId}/usage` returns the 24h run count, known/unknown spend and
`budgetState`/`budgetBlocked`/`budgetBlockReason`.

## Provider

- Origin: `https://api.deepseek.com`, fixed in `internal/provider` as a constant.
  There is no `base_url` column and no admin-supplied origin.
- Endpoints used: `GET /models` (connection check) and
  `POST /chat/completions` (`stream:false`, `thinking:{type:"disabled"}`,
  `max_tokens` from the agent config).
- Redirects are never followed (`CheckRedirect` refuses), so the key cannot be
  forwarded to another host.
- Responses are capped (2 MiB models, 4 MiB chat), validated against the
  documented shape, and must contain exactly one assistant text message with
  `finish_reason` `stop` or `length`. Provider usage is stored when present;
  absent usage leaves the cost unknown instead of guessing.
- Request timeout: 15 seconds, further bounded by the remaining turn deadline.

## Admin API

All routes require the admin session cookie and pass the existing CSRF check.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/admin/llm/connections` | List connections (never includes the key) |
| POST | `/api/admin/llm/connections` | Create a DeepSeek connection; key encrypted at rest |
| PUT | `/api/admin/llm/connections/{id}` | Edit label/model hint; an empty `apiKey` keeps the stored key |
| DELETE | `/api/admin/llm/connections/{id}` | Delete a connection not referenced by an agent |
| POST | `/api/admin/llm/connections/{id}/check` | Probe `GET /models`, record `lastCheck*`, return the model list |
| GET | `/api/admin/llm/connections/{id}/prices` | List stored prices |
| PUT | `/api/admin/llm/connections/{id}/prices/{model}` | Upsert input/output micro-units per 1M tokens (USD) |
| DELETE | `/api/admin/llm/connections/{id}/prices/{model}` | Remove a price |
| GET | `/api/admin/groups/{id}/agent-runs` | Newest 100 run attempts for a group |
| POST | `/api/admin/groups/{id}/agent-runs/{runId}/retry` | Append one attempt for the current turn |
| POST | `/api/admin/groups/{id}/agent-runs/{runId}/cancel` | Cancel one open attempt |
| GET | `/api/admin/groups/{id}/agents/{botId}/usage` | Rolling 24h usage and budget state |

`POST …/check` returns HTTP 200 with `{status:"ok"|"error", models, error,
checkedAt, connection}` because a rejected key or an unreachable provider is a
check result, not a server fault. Key material is never returned. A run id from
another group is reported as `404`.

## Known limitations

1. **Unknown cost can stop an agent for the rest of the 24h window.** With a
   daily budget set, one unverifiable run blocks new claims until it leaves the
   window, even when known spend is far below the cap.
2. **Computed cost is an estimate, not an invoice.** Cost from provider token
   usage and a stored price is computed locally (`costConfirmed=false`). Only a
   charge the provider reports itself is confirmed. Neither is the provider's
   bill.
3. **Shutdown is not a graceful run drain.** `Runner.Close` cancels in-flight
   HTTP calls; their rows are left open and marked `interrupted` by the next
   startup. They are never resumed automatically.
4. **`GET /models` is a limited probe.** It proves only that the key can read
   the catalog; it does not validate chat generation or account balance.
5. **No UI in this phase.** Runs, usage, retry and cancel are API-only.
6. **No paid request in tests.** All tests use a fake provider; no real key is
   required and no credit is spent. `go test -race` cannot run in this local
   environment (cgo is disabled and no gcc is installed), so concurrency is
   exercised by the tests but the race detector was not run.
