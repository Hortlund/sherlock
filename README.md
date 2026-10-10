# Sherlock

**Follow the evidence. Understand what changed.**

Sherlock is a self-hosted investigation tool for developers and operators running services with **Prometheus and Loki**. It detects changes in your metrics and logs, collects the supporting evidence, and turns it into a report you can inspect: what happened, what remains unknown, and what to check next.

Connect your existing telemetry, map a service, and investigate a finding. Add **OpenAI or Anthropic** for optional AI analysis, and send alerts to **Slack, Discord, or a webhook receiver**.

**Status:** Working MVP with outbound notifications, manually requested AI analysis, and proactive investigation recommendations. Automatic AI runs, multi-step investigations, and inbound alert triggers are on the roadmap. The current installation supports one administrator and one workspace.

## From signals to next steps

- **Spot changes.** Find new exception patterns, warning bursts, and increases in error rate by comparing recent activity with an earlier window.
- **Inspect the evidence.** Inspect the observations, timestamps, coverage, and stored evidence behind a finding.
- **Choose the next check.** Read suggested manual checks, or request AI hypotheses grounded in the collected evidence.
- **Keep your team informed.** Route factual notifications to your chosen destinations with service/severity filters and quiet hours.

For example, a checkout service starts logging connection-acquisition timeouts while its error rate rises. Sherlock can detect the change, collect the relevant signals, and show them together in an investigation. An AI analysis can suggest explanations and checks. A database failure or an exhausted connection pool remains a hypothesis until the evidence supports it.

## How it works today

1. **Connect Prometheus, Loki, or both.** Test access and map source labels and metrics to a service. Each source can be used independently; missing signals are reported explicitly.
2. **Scan your services.** Run a scan manually or enable scheduled monitoring. Three rule-based detectors look for new exception patterns, warning bursts, and error-rate regressions.
3. **Investigate a finding.** Collect a bounded incident window and comparison data, then inspect a persistent report with observations, evidence links, unknowns, and manual checks.
4. **Ask for AI analysis when useful.** Choose a configured OpenAI or Anthropic provider, preview the outgoing payload, and explicitly send it. The AI report appears alongside the rule-based report, with evidence references and usage history.
5. **Manage the finding and notify your team.** Acknowledge, snooze, dismiss, or resolve findings. Configured notification rules send qualifying events to Slack, Discord, or signed generic webhooks.

Detection, rule-based reports, and notifications work without AI credentials. AI currently analyzes evidence already collected by an investigation; each run makes at most one bounded provider request.

## Built for controlled investigations

- **Read-only telemetry access.** Sherlock queries your sources. Production remediation, shell commands, and infrastructure changes are outside the current scope.
- **Explicit AI data sharing.** Enable processing per workspace, provider, and service. Preview the request before sending; log excerpts require additional opt-in. Only the approved payload goes to the configured provider.
- **Bounded work and spending.** Collection and AI calls have request/time limits. AI admission uses daily and per-run budgets, with recorded usage and reconciliation for uncertain charges. Provider invoices can differ from configured estimates.
- **Durable background work.** PostgreSQL and River persist jobs and delivery history. Notification retries are bounded; an ambiguous AI response is recorded without automatically repeating the paid call.
- **Inspectable results.** Missing evidence, failed calls, and insufficient signal remain visible. AI citations identify supporting evidence; they do not prove a hypothesis.

API keys and destination secrets are encrypted at rest. See [AI data handling and accounting](docs/ai.md), [notification behavior](docs/notifications.md), and [operations](docs/operations.md) for the details.

## Try it locally

Requires **Docker with Compose**, **Go 1.26+** for the one-time secret generator, and `make`.

```sh
git clone https://github.com/Hortlund/sherlock.git
cd sherlock
make setup
make demo
```

Open [localhost:8080](http://localhost:8080). Enter `SHERLOCK_SETUP_TOKEN` from the generated `.env` file, then choose your administrator email and password.

In the app:

1. Open **Connections → Try the synthetic demo**.
2. Go to **Services → Run scan**.
3. Review the findings and click **Investigate** to inspect the evidence and report.

The demo uses simulated telemetry through the real collectors, database, and job queue. It needs no cloud credentials. AI calls and external notifications require separate configuration and explicit permission to send demo data.

To run without the demo source:

```sh
docker compose up --build -d --wait
```

`make setup` creates fresh secrets in `.env`, excludes them from Git, and refuses to overwrite an existing installation. Keep a secure backup of that file: its encryption key is required to decrypt stored credentials. `make down` stops the app while preserving the database volume.

### Connect your own stack

Add and test a Prometheus or Loki connection, map the service's labels and request counter or log patterns, and preview the mapping. Then run a scan or enable scheduled monitoring. By default, scans compare the latest 15 minutes with the preceding 60 minutes every five minutes.

Configure **Settings → Notifications** for outbound alerts and **Settings → AI providers** for optional analysis. AI setup requires your own API key, model ID, prices, budget, and allowed services; run the synthetic capability test before analyzing an investigation.

The default app is available only on loopback, and PostgreSQL has no published port. Remote access requires HTTPS through a trusted reverse proxy and an exact `SHERLOCK_ORIGIN`. See the [deployment and backup guide](docs/operations.md) before exposing an installation.

## Roadmap

The next steps build on the same evidence, privacy, and budget controls. These are ordered milestones, not release-date commitments.

| Milestone | Status | What it delivers |
| --- | --- | --- |
| Evidence-first foundation | Implemented | Prometheus/Loki connections, service mapping, scheduled scans, three detectors, finding lifecycle, and rule-based investigation reports. |
| Outbound notifications | Implemented · Phase 2 A | Slack, Discord, signed webhooks, routing filters, quiet hours, retries, and delivery history. |
| Reactive AI analysis | Implemented · Phase 2 B | OpenAI/Anthropic setup, payload previews, manual analysis, evidence references, usage tracking, cancellation, and budgets. |
| Proactive rules and activity | Implemented · First part of Phase 2 C | Local investigation recommendations, selected services, 60-second grouping, persistent cooldowns and an activity history that explains recommendations and skips. |
| Decision models and automatic investigation | Next · Remaining Phase 2 C | Optional AI triage and explicitly enabled automatic analysis within privacy, budget and concurrency limits. |
| Investigation tools and follow-up | Planned · Phase 2 D | Let AI request approved, bounded, read-only telemetry lookups across multiple steps, then support follow-up questions with cumulative cost and time limits. |
| Inbound alert triggers | Planned · Phase 2 E | Accept signed alerts from external systems, prevent replay/duplicates, and start investigations within administrator-configured service boundaries. |

Longer-term ideas include Grafana discovery, Kubernetes and trace context, additional detectors, and broader user/workspace support. These are deferred beyond the current delivery plan. Automatic remediation and two-way chat bots are also outside the current scope.

Local tests cover the delivered features using provider/delivery fixtures, alongside real local Prometheus/Loki checks. Live OpenAI/Anthropic and Slack/Discord account checks remain deployment validation; AI usefulness and unattended behavior still need scenario evaluations.

See the [detailed implementation plan](docs/phase2-plan.md) for acceptance criteria and [progress log](docs/progress.md) for completed checks and known limits.

## Stack and architecture

Sherlock runs as a modular monolith: one Go application serves the API, background workers, and compiled React UI. PostgreSQL stores application state, evidence, and durable jobs.

| Layer | Technology |
| --- | --- |
| API and workers | Go, `net/http`, River |
| Interface | React, TypeScript, Vite, TanStack Query |
| Storage | PostgreSQL, `pgx`, `sqlc`, versioned SQL migrations |
| Telemetry | Direct Prometheus and Loki HTTP APIs |
| Optional AI | OpenAI Responses and Anthropic Messages APIs |
| Deployment | Docker Compose: app + PostgreSQL, with an optional demo source |

## Development and checks

```sh
npm ci --prefix web
make build
make lint
make test
cd web && npx playwright install chromium && cd ..
make e2e
make real-sources
```

`make test` runs race-enabled Go checks and PostgreSQL integration tests. The disposable test database uses loopback port 55432 and memory-backed storage. Browser tests run an isolated installation on port 18080; they do not modify the app on port 8080. `make real-sources` checks the adapters against disposable Prometheus and Loki containers. Stop the test database with `docker compose -f compose.test.yaml down`.

For local Go development, export `DATABASE_URL`, `SHERLOCK_ENCRYPTION_KEY`, `SHERLOCK_SETUP_TOKEN`, and `SHERLOCK_ORIGIN`, then run `make dev`. The default web directory is `web/dist`. For Vite hot reload, run `npm run dev --prefix web` separately and set the Go server's origin to `http://127.0.0.1:5173`; visit that exact address. Vite proxies API requests to the Go server on port 8080.

`make generate` regenerates typed SQL access using pinned sqlc. Commit generated queries alongside their source SQL. Add a new migration for schema changes; applied migrations have verified checksums and must remain unchanged.

## Documentation

- [Product specification](INFRA_SHERLOCK_CODEX_KIT/SPEC.md) — active scope and behavior.
- [Roadmap and acceptance criteria](docs/phase2-plan.md) — implementation order and planned work.
- [Progress and verification](docs/progress.md) — what has actually been built and tested.
- [AI setup and accounting](docs/ai.md) — provider configuration, data release, limits, and usage.
- [Proactive recommendations](docs/automation.md) — service rules, cooldowns, activity history, and current limits.
- [Notifications](docs/notifications.md) — destination setup and webhook signing.
- [Operations](docs/operations.md) — deployment, network access, retention, backups, and key rotation.
- [OpenAPI](docs/openapi.yaml) — implemented HTTP API.
- [Architecture decisions](docs/decisions.md) — implementation choices and tradeoffs.
- [Archived specifications](INFRA_SHERLOCK_CODEX_KIT/reference/README.md) — the original vision and earlier MVP scope, preserved for reference.
