# BOTLINK loopback QA harness

These scripts exercise the real BOTLINK server with a fake DeepSeek provider.
They are development-only:

- both URLs must be loopback (`localhost`, `127.0.0.1` or `::1`);
- the server must run with `BOTLINK_DEV=1` and
  `BOTLINK_DEV_PROVIDER_URL=http://127.0.0.1:8098`;
- use a dedicated test database;
- the fake key is not a real credential and no paid request is made.

`integration.mjs` refuses to run if a database already contains a non-QA LLM
connection, so it cannot overwrite prices on a real database. The fake provider
is in memory only; restarting it clears all modes and counters.

## Run

Build the server and start the fake provider:

```sh
go build -o qa-scripts/run/botlink-server.exe ./cmd/server
node qa-scripts/fake-provider.mjs
```

In a second terminal, start the server on a fresh test database:

```sh
PORT=8099 \
DB_PATH=qa-scripts/run/integration.db \
FRONTEND_DIST_DIR=src/frontend/dist \
ADMIN_PASSWORD=integration-pass \
SESSION_SECRET=replace-with-32-plus-random-characters \
BOTLINK_DEV=1 \
BOTLINK_DEV_PROVIDER_URL=http://127.0.0.1:8098 \
TURN_TIMEOUT_SEC=30 \
AGENT_MAX_CONCURRENCY=2 \
qa-scripts/run/botlink-server.exe
```

In a third terminal, run the checks:

```sh
ADMIN_PASSWORD=integration-pass node qa-scripts/integration.mjs
```

`ADMIN_PASSWORD` is a local test-only password, not a production secret.
Override `BOTLINK_BASE`, `FAKE_BASE` and `FAKE_PORT` only with loopback
origins. The run leaves groups, runs and one `fake-provider` connection in the
test database; delete that database to start clean.
