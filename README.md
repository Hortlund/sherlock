# Sherlock

Self-hosted reliability investigations. Go API and workers, React/TypeScript UI, PostgreSQL persistence. The active build contract is [SPEC v1.2](INFRA_SHERLOCK_CODEX_KIT/SPEC.md); [progress](docs/progress.md) records what actually works.

## Run locally

Requires Docker Compose and Go 1.26+ for the one-time secret generator.

```sh
make setup
docker compose up --build -d --wait
```

Open [http://localhost:8080](http://localhost:8080). Enter `SHERLOCK_SETUP_TOKEN` from your local `.env` file and choose your administrator email/password. The token is intentionally not printed. `.env` is created with mode 0600 and excluded from Git and Docker build context. Keep a secure backup: the encryption key is required to reopen this installation. `make setup` refuses to replace an existing file.

Compose publishes only the application on loopback. PostgreSQL uses a persistent Docker volume and has no published port. `make down` stops containers while preserving data. Do not remove the volume unless you intend to discard the installation.

For another local port, change both `SHERLOCK_PORT` and `SHERLOCK_ORIGIN` in `.env`. For remote use, terminate HTTPS at a trusted reverse proxy and set the exact public `SHERLOCK_ORIGIN`; the application enables Secure cookies for HTTPS. Do not expose the default HTTP development installation publicly. Forwarded client-IP headers are deliberately not trusted; authentication throttling counts the proxy as a single peer.

## Development and checks

```sh
npm ci --prefix web
make build
make lint
make test
cd web && npx playwright install chromium && cd ..
make e2e
```

`make test` runs Go race-enabled unit tests and PostgreSQL integration tests. The disposable test database uses `compose.test.yaml`, loopback port 55432, and memory-backed storage. Integration tests create their own schemas. E2E tests use a fresh schema and server on port 18080 and clean them up afterward. They never bootstrap or modify the installation on port 8080. Stop the test database with `docker compose -f compose.test.yaml down`.

For local Go development, export `DATABASE_URL`, `SHERLOCK_ENCRYPTION_KEY`, `SHERLOCK_SETUP_TOKEN`, and `SHERLOCK_ORIGIN`, then run `make dev`. The default web directory is `web/dist`. For Vite hot reload, run `npm run dev --prefix web` in a second terminal and set the Go server's origin to `http://127.0.0.1:5173`; visit that exact address. Vite proxies API requests to the Go server on port 8080.

`make generate` regenerates typed SQL access using pinned sqlc. Commit generated queries alongside the source SQL. Migrations apply transactionally under an advisory lock and verify recorded checksums. Never edit an applied migration; add a new one.

## Delivered MVP

- One-time token-protected admin setup; the database enforces a single administrator/workspace.
- Argon2id passwords, opaque hashed sessions with 12-hour expiry, cookie rotation on login, and session revocation on logout/password change.
- Exact-origin checks on mutations, session-bound CSRF tokens, request size limits, and PostgreSQL-backed authentication throttling.
- AES-GCM secret encryption bound to record scope; startup refuses a changed encryption key.
- Health/readiness endpoints, non-root application container, structured sanitized logs, and persistent security audit events.

Public API exceptions are health/readiness, static UI assets, initialization status (a boolean only), rate-limited login, and token-protected bootstrap. All workspace data is authenticated. See [OpenAPI](docs/openapi.yaml) for implemented routes.


## Try the complete demo

```sh
make demo
```

After setup, open **Connections → Try the synthetic demo**, then **Services → Run scan**. Review the two findings and click **Investigate** to collect a fresh report. Demo telemetry passes through the same HTTP adapters, database, queue, rules, and report validation as live data. It is visibly marked Simulated. It never inserts finished findings or reports directly.

For real sources, add/test a connection, choose exact service labels, configure the request counter/error labels or log severity patterns, and preview. Enable scheduled monitoring in the mapping editor, or run manual scans. Default scans compare 15 minutes with the preceding 60 minutes every five minutes. Investigations default to a 30-minute incident window, plus comparison context. Missing, sparse, stale or capped observations abstain instead of claiming health. The MVP has three deterministic detectors and no AI dependencies or production write actions.

Findings support acknowledgement, snooze, manual resolution/dismissal, recurrence cooldown and usefulness feedback. Reports persist observations, citations, unknowns and manual checks. Progress uses authenticated polling with replayable event IDs. Settings exposes detector thresholds, cadence/windows, retention, password changes and system health.

See [operations](docs/operations.md) for explicit private-network allowlists, query/coverage bounds, credential deletion, evidence expiry, backup/restore, key rotation and release checks. The [original specification](INFRA_SHERLOCK_CODEX_KIT/reference/v1.0/SPEC.md) remains unchanged.

```sh
make real-sources
```

This additional check starts disposable real Prometheus and Loki containers, seeds bounded synthetic telemetry, verifies actual query semantics and removes its own test volumes. It is distinct from the lightweight demo fixture server.

## Phase 2: notifications and AI analysis

[Phase 2](docs/phase2-plan.md) starts with outbound notifications. **Settings → Notifications** supports Slack, Discord, and signed generic webhooks, including filters, quiet hours, previews, test sends, retries, and delivery history. Demo alerts require explicit opt-in. See [notification setup and receiver signing](docs/notifications.md).

**Settings → AI providers** supports OpenAI and Anthropic with encrypted keys, explicit service consent, budgets and synthetic capability tests. A completed investigation offers **Investigate with AI → Preview AI payload → Send to AI provider**, with citations, usage, cancellation and a deterministic fallback. See [AI setup and accounting](docs/ai.md). Decision-model triage, proactive work, tools/follow-up and inbound alert triggers remain planned. Notifications and the deterministic demo work without AI credentials. The [v1.1 spec](INFRA_SHERLOCK_CODEX_KIT/reference/v1.1/SPEC.md) remains archived unchanged.
