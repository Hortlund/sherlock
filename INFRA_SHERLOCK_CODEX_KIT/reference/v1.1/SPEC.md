# Infra Sherlock — Focused MVP Specification

**Version:** 1.1 (scope revision, 2026-10-10)  
**Status:** Active specification; local MVP implemented and verified 2026-10-10 (see project `docs/progress.md`)  
**Stack:** Go backend, React/TypeScript frontend, PostgreSQL  
**Product:** Self-hosted reliability investigations backed by observable evidence

This is the source of truth for the next build. The original [v1.0 specification](reference/v1.0/SPEC.md) is preserved unchanged for reference. Its broader P0 is no longer the MVP commitment. Deferred ideas are retained in Section 13; do not implement them merely because they appear in the archive.

## 1. Product promise and first release

Connect existing Prometheus and Loki sources, map their signals to a service, detect a meaningful change, and inspect an investigation with reproducible evidence and explicit unknowns.

The first release must establish that Sherlock can find useful signals without overwhelming the operator or overstating what the data proves. It works entirely without AI credentials. A deterministic investigator assembles observations, timelines, rule-supported hypotheses, and manual validation steps; it does not invent explanations.

### First complete user journey

1. Start Sherlock and PostgreSQL using Docker Compose.
2. Bootstrap one administrator and choose **Try Demo** or **Connect My Stack**.
3. Connect Prometheus and/or Loki directly, test access, and see actual signal coverage.
4. Map source labels and metrics to a canonical service and select it for monitoring.
5. Run a scan or let scheduled scans collect bounded observations.
6. See a deduplicated finding for a new exception pattern, warning burst, or error-rate regression.
7. Click **Investigate**, follow collection progress, and inspect a report with evidence, caveats, and suggested checks.
8. Acknowledge, snooze, dismiss, or resolve the finding.

The reference fault scenario is a checkout service developing database connection-acquisition timeouts. Sherlock can establish that timeouts appeared or increased. It must not claim database failure, pool saturation, or deployment causality without additional supporting signals.

### Explicit MVP boundary

| Included | Deferred |
| --- | --- |
| Direct Prometheus and Loki connections | Grafana discovery/proxy, Kubernetes, traces, change-history connectors |
| Manual service mapping plus bounded label suggestions | Automatic cross-stack identity and topology discovery |
| Three deterministic detectors | Resource, disk, latency, resilience, and business-invariant detectors |
| Deterministic evidence-based investigations | Hosted/local model integration, model triage, generic MCP |
| Single administrator and workspace | Additional users, roles, OIDC, multi-workspace product flows |
| In-app findings and lifecycle | Paging, external notifications, Daily Brief |
| Reproducible demo and real API adapters | Remediation, shell access, configuration changes, PR generation |

Prometheus-only and Loki-only installations are supported: unavailable detectors show `insufficient_signal`. Connecting both sources is required for the combined walkthrough, not for using Sherlock at all.

## 2. Architecture and stack

Use a modular monolith. One Go application initially runs the HTTP API and bounded background workers and serves the compiled React assets. PostgreSQL stores application state, evidence, and durable jobs. Expose one app port. No separate frontend server is required in production.

```text
React/TypeScript UI
        | REST + server-sent events
        v
Go API / authentication / service mapping
        |                         |
        v                         v
PostgreSQL <---------------- Go workers
                                  |
                         Prometheus / Loki
                                  |
                     normalized bounded evidence
                                  |
                       detectors / investigations
                                  |
                        findings and reports
```

- **Go:** idiomatic packages, `net/http`, `context.Context`, structured logs, timeouts, cancellation, and bounded concurrency.
- **React + TypeScript + Vite:** React Router, TanStack Query, accessible UI components. Pick a component kit during implementation; visual polish must not expand scope.
- **PostgreSQL:** SQL migrations, `pgx`, and `sqlc` for typed query access. Keep SQL and transactions explicit.
- **Jobs:** prefer River backed by the same PostgreSQL instance, subject to verifying recovery, retry, cancellation, and licensing requirements at implementation time. Record the choice before queue implementation. Do not build a custom queue by default.
- **Contracts:** document REST with OpenAPI and generate frontend API types where practical.
- **Deployment:** Docker Compose with app and PostgreSQL; a demo profile may add fixture sources. No Redis, Kafka, vector database, or Python service.

Keep interfaces at actual integration boundaries. Build concrete behavior before adding abstractions for future providers. The collector, detector, and report boundaries should permit later AI assistance without making it a runtime dependency.

### Intended repository layout

Application code will live at the project root, alongside this specification kit. The kit remains the canonical specification location; avoid competing copies of the active spec.

```text
INFRA_SHERLOCK_CODEX_KIT/           # active spec and archived reference
cmd/sherlock/main.go
internal/api/                     # REST, SSE, validation
internal/auth/
internal/config/
internal/crypto/
internal/db/
internal/jobs/
internal/connectors/prometheus/
internal/connectors/loki/
internal/identity/
internal/evidence/
internal/detectors/
internal/investigations/
internal/findings/
web/
migrations/
examples/demo/
tests/contract/
tests/e2e/
docs/decisions.md
docs/progress.md
compose.yaml
Makefile
```

## 3. Installation, authentication, and setup

- Document a one-time persistent encryption-key setup and then `docker compose up --build -d`. Default URL: `http://localhost:8080`.
- Bind to loopback by default. Document TLS reverse-proxy configuration for remote access.
- Run migrations with concurrency safety; expose `/healthz` and `/readyz`; preserve database state across restarts.
- Provide `make dev`, `make test`, `make e2e`, `make demo`, `make lint`, and `make down` when the corresponding functionality exists.
- Bootstrap exactly one admin using an installation-scoped setup token generated or supplied locally. Bootstrap must be atomic, token-protected, and unavailable once initialized.
- Hash passwords with Argon2id, rate-limit authentication, rotate/expire sessions, use HttpOnly and SameSite cookies, enforce CSRF protection, and use Secure cookies under HTTPS. Explicit localhost development configuration may use HTTP.
- Use one workspace. Scope domain records and every authenticated data query to it from the beginning. Global job/migration metadata may be infrastructure-scoped; job payloads carry workspace scope.

### Setup flow

1. **Secure workspace:** bootstrap/login.
2. **Choose data:** clearly marked synthetic demo or real sources.
3. **Connect:** direct Prometheus and/or Loki URL, supported authentication, optional tenant header and CA configuration; test before monitoring.
4. **Map a service:** give it a name/environment, choose source selectors and metric mappings, preview matching data and coverage.
5. **Start monitoring:** choose enabled detectors, thresholds, and bounded cadence; run initial scan.

No AI key, Kubernetes configuration, Grafana account, or MCP approval screen is part of this release. Demo and real data must remain separate: separate scoped services/connections, visible origin labels, and no mixing of evidence in a report.

## 4. Connections, identity, and signal contracts

### Native connectors

Support connection creation, testing, editing, disabling, and deletion. Configuration includes name, kind, base URL, supported authentication (none, bearer token, or basic), optional Loki tenant, and TLS trust configuration. TLS verification is enabled by default. Credentials are write-only in the UI/API.

Each connection reports `untested`, `connected`, `degraded`, `disconnected`, or `disabled`, plus last test/success, a sanitized error, and actual capabilities. Distinguish authentication failure, TLS failure, timeout, incorrect path, and a valid empty result. A successful connection test does not mean the service has sufficient telemetry.

- **Prometheus:** bounded instant/range queries and label suggestions through documented APIs.
- **Loki:** bounded label suggestions and log/range queries through documented APIs.
- Use contract fixtures plus a local integration check against actual Prometheus/Loki before release. Verify upstream API details during implementation.
- Disabled/deleted connections cannot start new collection. Cancel outstanding work where possible; handlers recheck connection state before requests. Preserve already stored evidence subject to retention, marking its source unavailable.

### Service mapping

Service identity consists of a stable internal ID, workspace, user-provided name and environment, plus optional cluster/namespace metadata. Associate each source with explicit selectors. Source-local labels are suggestions only; never merge services solely because names match. Preserve mapping version and selectors with collected evidence and each investigation snapshot.

Mapping UI must support preview, correction, and visible per-detector coverage. Changing a mapping invalidates incompatible baselines; it must not rewrite historical evidence.

### Signal interpretation

- Do not assume metric names such as a particular HTTP request counter exist. Offer editable mappings to approved query templates with numerator/denominator, units, label dimensions, and counter semantics.
- Validate error-rate selectors as a compatible subset of total request traffic. Apply counter-reset-aware calculations and traffic minimums. Reject incompatible mappings and show actionable errors.
- Define log severity mapping for structured fields or explicit text patterns; do not assume every log line is an exception.
- Queries require service scope, bounded lookback, timeouts, sample/output limits, and concurrency budgets. No arbitrary query or tool execution through the application API. Advanced freeform queries are deferred.
- A truncated sample is not a total count. Evidence records whether values are exact aggregates, sampled observations, or incomplete. Fingerprint counts derived from capped logs are lower bounds; count/burst detectors must abstain when completeness is insufficient.
- Missing, stale, low-volume, or incompatible signals produce `insufficient_signal` or `degraded`, never a healthy result by default.

## 5. Evidence and data retention

Evidence is the shared contract between collection, detection, and investigation. Store bounded, sanitized data sufficient to reproduce and interpret an observation, not a second telemetry warehouse.

Required fields: ID, workspace/service/connection IDs, origin (`demo` or `live`), kind, observation and collection times, query window, mapping version, sanitized selectors/query metadata, value/units or excerpt, sample size, completeness/truncation, source coverage, and redaction version.

Show the stored evidence even if the upstream source is no longer available. Offer reproducible query metadata and a source link when a valid link can be constructed; a URL alone is not sufficient evidence. Never include credentials in source references.

Normalize volatile log values for stable fingerprints and keep only a few sanitized excerpts. Bound cardinality and record fingerprint algorithm version. Redact credentials, authorization headers, cookies, and configured sensitive patterns before persistence or application logging. Automated redaction cannot promise to remove every sensitive value; allow excluding sources/services and disabling excerpts.

Default evidence retention is seven days, configurable. Finding/report history may remain after evidence expires, but references must show `expired` rather than silently disappear. Keep referenced evidence until its retention deadline, not indefinitely. Provide cleanup jobs and documented backup/restore and secret deletion behavior.

Suggested domain tables: users, sessions, workspace/settings, connections/encrypted credentials, services/source mappings, detector configuration/state, scan cursors, evidence, findings/events, investigations/events/reports, and audit events. Add tables as needed for implemented slices, not as empty scaffolding for the archived roadmap.

## 6. Three MVP detectors

Each detector declares signal requirements, lookback/comparison windows, minimum samples, thresholds, dedup identity, and test fixtures. Thresholds are configurable and shown with findings; document concrete initial defaults when implemented.

| Detector | Trigger | Required guardrails |
| --- | --- | --- |
| New exception fingerprint | An error pattern appears in the current window and is absent in a sufficiently covered comparison window | Minimum occurrences; complete enough baseline; wording says “new in comparison window,” not never seen before |
| Warning burst | A configured warning/retry pattern increases materially over its baseline | Minimum count, comparable window coverage, log-volume context, and no totals inferred from truncated samples |
| Error-rate regression | Mapped error proportion increases over baseline | Valid error/total counter mapping, minimum traffic, reset handling, absolute and relative change guards |

Use separate baselines per service, mapping version, and signal. Default scan cadence is five minutes, with a 15-minute current window and preceding 60-minute comparison window; these are initial defaults, configurable within query limits. Baseline warm-up must be visible. Initial historical queries may establish a baseline if source coverage permits it.

These simple windows do not model seasonality or prove a release caused a regression. Do not label findings as deployment regressions without deployment evidence.

Statuses per detector/service: `active`, `warming_up`, `insufficient_signal`, `degraded`, `disabled`. Display the reason and how to supply missing data. Emit candidates only when guards pass.

### Noise and finding lifecycle

- Stable dedup identity: workspace, service, detector, relevant signal dimensions, and mapping version. Overlapping scans update an open finding, not create duplicates.
- States: `new`, `acknowledged`, `snoozed`, `resolved`, `dismissed`. Audit transitions; allow resolution/dismissal from open states, acknowledgement from new/snoozed, and snoozing from new/acknowledged. Repeating the current state is idempotent.
- Snooze expires back to `new`; retain evidence updates while snoozed. Resolution is manual in the MVP. A fresh qualifying observation after configurable cooldown can create a new occurrence after resolution or dismissal; replaying the same observation cannot.
- Default recurrence cooldown is 24 hours. Keep dismissed/resolved occurrences in history. Expose settings and suppression reason.
- Severity follows documented deterministic thresholds and traffic/context, with no invented outage probability. In-app only; no paging.

## 7. Deterministic investigations

A user starts an investigation from a finding or service, with a selected time range. Snapshot identity, mappings, origin, and coverage; collect bounded observations and construct a time-ordered report using deterministic rules/templates.

State machine: `queued -> collecting -> evaluating -> reporting -> completed`, with terminal `insufficient_evidence`, `cancelled`, or `failed`. Missing one source may yield a useful partial report with explicit limitations. An investigation with no adequate evidence ends as `insufficient_evidence`.

Default bounds: 30-minute selected incident window, 90-second execution deadline after the job starts, at most 12 outbound telemetry requests including retries, capped result sizes, and bounded per-connection concurrency. A maximum two-hour retrieval horizon permits baseline context; distinguish baseline evidence from incident-window evidence. Enforce limits server-side and expose cancellation.

Reports include:

- What was observed and the affected service/window.
- Timeline and supporting evidence IDs.
- Comparison values, coverage, sampling limitations, and missing signals.
- Rule-supported possible explanations, contradictory evidence where available, and what remains unknown.
- Manual checks that could support or disprove an explanation.
- A clear distinction between observed symptoms and unproven causes.

For the timeout scenario, a valid report may say: “Connection-acquisition timeout logs increased; the mapped request error rate also rose. Pool or database metrics are unavailable, so the underlying cause is undetermined.” Only include the error-rate claim when that evidence exists.

Validate that evidence references belong to the same workspace, service scope, origin, and declared query windows. Report validation checks provenance and structure; it cannot prove semantic correctness. Tests must also check claims against scenario ground truth. If validation fails, persist a safe evidence-only result or failure, never a fabricated explanation.

Persist sanitized progress events and expose them through SSE, with event IDs and reconnect replay or a documented polling fallback. There is no model stream or autonomous tool-selection loop in the MVP.

## 8. Durable execution and operational behavior

- Commit scan/investigation state and job insertion transactionally. Handlers must be idempotent under retries and duplicate delivery.
- Persist schedules, cursors, investigation checkpoints, and deadlines. An in-memory timer alone is not a scheduler.
- Bound workers and query concurrency per connection. Apply timeouts, backoff, and response-size limits; one failing source must not block unrelated work.
- Recover unfinished jobs after restart. Retrying work must not duplicate findings or report events. Execution retries retain the original persisted deadline/request budget; exhausted investigations end explicitly and can be restarted by the user.
- Recheck monitoring enablement, connection status, and service mapping at safe boundaries. Persist a consistent scan snapshot.
- Expose structured logs, request IDs, low-cardinality operational metrics, health/readiness, and sanitized errors. Protect diagnostics from unintended public exposure.
- Initial targets: a connection test finishes or times out within 15 seconds; the seeded dashboard loads within two seconds p95 after first load on the documented development setup; each running investigation respects its deadline.

## 9. Security required before real connections

Reducing features does not defer protection of production credentials or network access.

- Encrypt credentials using authenticated encryption with unique nonces, versioned keys, and a persistent master key outside PostgreSQL. Refuse real-source operation without it; demo-only ephemeral-key mode must be explicit. Define rotation and missing-key failure behavior.
- Use minimal read privileges on Prometheus/Loki and authenticate all data/configuration endpoints. Public exceptions are health, static/login assets, rate-limited login, and the separately token-protected bootstrap route. Enforce workspace scope server-side.
- Enforce URL, scheme, DNS resolution, redirect, and destination checks on connection tests and collection. Block metadata/link-local destinations; authorize private/loopback destinations only through explicit deployment-controlled allowlists needed for self-hosted sources and demo containers. Browser input must not broaden network policy.
- Keep TLS verification enabled. Support trusted private CAs without a general insecure toggle in the normal setup flow.
- Treat log text and labels as untrusted display data. Escape output, sanitize errors, and exclude credentials from responses, logs, audit payloads, and source links.
- Audit setup, authentication/security changes, connection mutations, scans, investigations, and finding transitions. Never audit secret values.
- No external AI data transfer, arbitrary shell commands, remote execution, or production mutations. The MVP only reads from configured telemetry APIs.
- Document retention, backup/restore, connection-secret revocation, and limitations before release.

## 10. UI and API surface

Navigation: **Overview, Findings, Investigations, Services, Connections, Settings**. No empty navigation entries for deferred features.

- **Overview:** connection status, monitored services, active findings, latest successful scan, and stale/missing-data indicators.
- **Connections:** direct source wizard, actionable test errors, capabilities, edit/disable/delete; require confirmation before deleting a connection.
- **Services:** mapping and preview, monitoring toggle, detector configuration/coverage, investigation launch.
- **Findings:** filters, evidence, actual thresholds/comparisons, lifecycle actions, and a small useful/not-useful feedback control.
- **Investigations:** progress, report, evidence browser, cancellation, and explicit insufficient-data outcomes.
- **Settings:** retention, cadence/bounds, admin password, and system health. No AI or provider settings yet.

REST prefix `/api/v1`. Minimum resources: setup/bootstrap, auth/login/logout/me, connections and tests, services and mappings/monitoring, detectors/configuration, scans, findings/state/feedback, evidence, investigations/cancel/events, and settings. Document exact routes and payloads through OpenAPI as implemented.

Use cursor pagination for growing lists and safe errors shaped `{code, message, request_id, details?}`. SSE and normal requests enforce identical authentication/scope rules. Accessible controls, legible dense layouts, useful empty states, and responsive behavior take precedence over animations.

## 11. Demo and verification

The demo uses reproducible synthetic telemetry served through the same Prometheus/Loki connector interfaces and the same detection/investigation pipeline as real sources. Do not directly insert a finished finding/report and call that an end-to-end demo. A local demo profile may run fixture API servers; users must not need cloud credentials. Demo time must be reproducible through an injectable clock or clearly defined fixture time anchoring.

Required scenarios:

1. Checkout database-timeout pattern plus mapped error-rate increase.
2. Warning/retry burst with stable request error rate; do not claim an outage.
3. Healthy service with noisy logs; no qualifying finding.
4. Missing metrics, insufficient traffic, baseline warm-up, and truncated logs; abstain or qualify correctly.
5. Unrelated changes near an incident; no unsupported causal claim.
6. Hostile instructions/HTML/secret-shaped strings in source data; safe rendering/redaction and no execution.

Verification layers:

- **Unit:** fingerprints/redaction, detector guards, mappings, evidence/report validation, lifecycle/dedup, and credential handling.
- **Integration:** PostgreSQL migrations/auth scope, job retries/restart recovery, retention, and adapter contracts including auth/TLS/timeout/empty/truncated responses. Test real local Prometheus/Loki query semantics before release.
- **Playwright:** bootstrap, demo, connection setup/test, mapping, scan, finding, investigation/evidence, lifecycle actions, disable connection, and absence of returned secrets.
- Check scan replay, restart, and mapping changes explicitly. Record actual results and environment; distinguish fixture coverage from real API coverage. Do not invent accuracy percentages or claim remote integration tests ran.

## 12. Implementation slices and release gate

Each slice delivers working behavior with relevant automated checks and documentation. Keep the application bootable; build no placeholders for the archived feature set. Implementation starts only when requested after this specification review.

### Slice 1 — Bootable secure foundation

Go API, React shell, PostgreSQL migrations, Compose, health checks, token-protected admin bootstrap, sessions/CSRF, key handling, and initial CI.

Acceptance: documented startup works from a clean checkout, login works, bootstrap cannot race or repeat, unauthorized data access fails, and secrets never appear in read responses.

### Slice 2 — Connect and map one service

Prometheus/Loki adapters, network/query controls, encrypted connection storage, guided tests, manual service mapping with previews/coverage, evidence persistence, and demo source fixtures.

Acceptance: add/test/edit/disable/delete both source types, explain failures, collect genuine evidence through each adapter, and label demo versus real data. Unsupported mappings report missing capability.

### Slice 3 — Detect and manage findings

Durable jobs/schedules, bounded scans, the three detectors and baselines, dedup, findings lifecycle, and Overview/Findings UI.

Acceptance: reference fixture produces expected findings, healthy fixture does not, repeated/overlapping scans deduplicate, incomplete data is handled honestly, and restart/disable controls work.

### Slice 4 — Complete the investigation journey

User-triggered deterministic investigations, persistent progress/SSE, scoped evidence validation, reports/unknowns/manual checks, cancellation, and end-to-end walkthrough.

Acceptance: demo follows the complete pipeline without AI, evidence is inspectable, reports make only supported observations, and missing sources lead to honest partial/insufficient-evidence results.

### Slice 5 — Verify against real sources and prepare release

Actual local Prometheus/Loki integration, retention, backup/restore and key-rotation verification, security/error/restart checks, final documentation, and release smoke test.

Acceptance: all required checks run successfully on the documented environment; no fake integrations, hidden setup steps, lost jobs, leaked secrets, or invented causal certainty. Where a required check cannot run, record it as a release blocker.

### MVP release checklist

- [x] Documented Compose startup and secure single-admin onboarding.
- [x] Direct Prometheus/Loki connection management with meaningful status/errors.
- [x] Manual service mapping, signal preview, and honest detector coverage.
- [x] Three bounded detectors with sample guards, baselines, and dedup.
- [x] Durable periodic/manual scans and findings lifecycle.
- [x] Deterministic investigation with progress, cancellation, citations, and unknowns.
- [x] Demo exercises the real application pipeline without cloud credentials.
- [x] Single-source operation and missing/stale/truncated-data cases behave correctly.
- [x] Credential, network, authorization, retention, and restart protections verified.
- [x] Relevant unit/integration/Playwright checks and real local API tests pass.

## 13. Preserved roadmap, outside this MVP

The original [v1.0 spec](reference/v1.0/SPEC.md) retains detailed designs and acceptance ideas. The following are candidates for subsequent releases, not an automatic build queue:

1. **Easier onboarding and infrastructure signals:** Grafana discovery/proxy, scoped Kubernetes access, restart/resource/latency/disk/resilience detectors.
2. **Assisted investigations:** optional AI providers, typed triage, egress opt-in, budgets, evaluations, and server-enforced tool policy. Verify provider capabilities at implementation time; no dependency on a named API/model before then.
3. **Extensibility:** remote MCP, explicit per-tool approval, credential restrictions, schema/output bounds, and transport negotiation. Metadata or an allowlist alone cannot prove a remote tool has no side effects.
4. **Richer context:** traces, deployment/Git history, topology, business invariants, and stronger statistical baselines.
5. **Operational workflows:** Daily Brief, external notifications, SLO integration, multi-user/OIDC, Helm distribution, and optional OTLP intake.
6. **Later research:** incident memory, sandbox experiments, and reviewed change proposals. Production remediation requires a separate explicit design and authorization.

Prioritize these using evidence from the first deployment: detector usefulness, false positives, mapping friction, investigation quality, and missing signals. The target deployment stack has not yet been confirmed; direct Prometheus/Loki is the bounded initial assumption, not a claim about the user's infrastructure.
