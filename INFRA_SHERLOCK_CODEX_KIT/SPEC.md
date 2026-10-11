# Infra Sherlock — Product Specification

**Version:** 1.2 (AI and notification planning revision, 2026-10-10)  
**Status:** Deterministic MVP delivered; Phase 2 outbound notifications and reactive OpenAI/Anthropic analysis (Slices A/B) implemented and locally verified. Slice C now includes local proactive recommendations and history. Optional OpenAI/Anthropic advisory triage is implemented. Opt-in automatic investigation admission is implemented; Slice D bounded tools/scoped follow-up is implemented; Slice E signed inbound triggers are implemented behind explicit consent. See project `docs/progress.md` for actual verification.
**Stack:** Go backend, React/TypeScript frontend, PostgreSQL  
**Product:** Self-hosted reliability investigations backed by observable evidence

This is the active product contract. Sections 1–12 preserve the delivered foundation; Sections 14–20 define the next phase: OpenAI and Anthropic support, decision-model triage, reactive/proactive investigations, and outbound notifications. Section 13 retains the remaining roadmap. The [delivered MVP v1.1](reference/v1.1/SPEC.md) and [original vision v1.0](reference/v1.0/SPEC.md) are archived unchanged. The ordered implementation plan is [Phase 2 plan](../docs/phase2-plan.md). The implementation plan and progress distinguish delivered slices from planned capabilities.

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

### Delivered MVP boundary (Phase 2 additions are defined below)

| Delivered foundation | Originally deferred |
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

The delivered baseline requires no AI key, Kubernetes configuration, Grafana account, or MCP approval screen. Phase 2 adds optional AI and notification setup after this working path. Demo and real data must remain separate: separate scoped services/connections, visible origin labels, and no mixing of evidence in a report.

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
- Severity follows documented deterministic thresholds and traffic/context, with no invented outage probability. The delivered baseline is in-app only. Phase 2 notification policy may deliver these findings externally without changing their evidence requirements.

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

Persist sanitized progress events and expose them through SSE, with event IDs and reconnect replay or a documented polling fallback. The delivered baseline has no model stream or autonomous tool-selection loop. Phase 2 adds a separately bounded AI run linked to this evidence/report.

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
- Telemetry operations remain read-only. Arbitrary shell commands, remote execution, and production remediation remain out of scope. The delivered baseline has no external AI transfer; Phase 2 permits explicitly configured provider calls and notification delivery under Sections 15–18.
- Document retention, backup/restore, connection-secret revocation, and limitations before release.

## 10. UI and API surface

Navigation: **Overview, Findings, Investigations, Services, Connections, Settings**. No empty navigation entries for deferred features.

- **Overview:** connection status, monitored services, active findings, latest successful scan, and stale/missing-data indicators.
- **Connections:** direct source wizard, actionable test errors, capabilities, edit/disable/delete; require confirmation before deleting a connection.
- **Services:** mapping and preview, monitoring toggle, detector configuration/coverage, investigation launch.
- **Findings:** filters, evidence, actual thresholds/comparisons, lifecycle actions, and a small useful/not-useful feedback control.
- **Investigations:** progress, report, evidence browser, cancellation, and explicit insufficient-data outcomes.
- **Settings:** retention, cadence/bounds, admin password, and system health in the delivered baseline; Phase 2 extends it with AI providers, automation, budgets, and notification destinations.

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

The five foundation slices below are delivered. Preserve their behavior and acceptance coverage. Future work follows the Phase 2 plan only after implementation is requested; this specification revision does not start implementation.

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

## 13. Remaining roadmap, outside Phase 2

The original [v1.0 spec](reference/v1.0/SPEC.md) retains detailed designs and acceptance ideas. The following are candidates for subsequent releases, not an automatic build queue:

1. **Easier onboarding and infrastructure signals:** Grafana discovery/proxy, scoped Kubernetes access, restart/resource/latency/disk/resilience detectors.
2. **Additional AI integrations:** local model hosting, additional providers, and advanced incident memory. OpenAI/Anthropic and typed triage are promoted into Phase 2 below.
3. **Extensibility:** remote MCP, explicit per-tool approval, credential restrictions, schema/output bounds, and transport negotiation. Metadata or an allowlist alone cannot prove a remote tool has no side effects.
4. **Richer context:** traces, deployment/Git history, topology, business invariants, and stronger statistical baselines.
5. **Operational workflows:** AI-authored Daily Brief, two-way Slack/Discord bots and commands, SLO integration, multi-user/OIDC, Helm distribution, and OTLP intake. Outbound Slack/Discord/generic webhooks and bounded inbound alert triggers are promoted into Phase 2.
6. **Later research:** incident memory, sandbox experiments, and reviewed change proposals. Production remediation requires a separate explicit design and authorization.

Prioritize these using evidence from the first deployment: detector usefulness, false positives, mapping friction, investigation quality, and missing signals. The target deployment stack has not yet been confirmed; direct Prometheus/Loki is the bounded initial assumption, not a claim about the user's infrastructure.

## 14. Phase 2 — AI assistance and notification scope

The product should explain a finding when asked, investigate selected changes automatically, and deliver useful alerts without consuming model tokens on every polling cycle. Keep Go, React/TypeScript, PostgreSQL, and River. No additional application service, vector database, or agent framework is required.

| Capability | Planned Phase 2 behavior |
| --- | --- |
| AI providers | Direct OpenAI and Anthropic adapters, encrypted credentials, capability checks, configurable model selection |
| Decision models | Optional OpenAI Decisions or structured classification through either provider; deterministic routing remains available |
| Reactive work | User launches an AI investigation or asks a scoped follow-up question; a later slice accepts authenticated alert webhooks |
| Proactive work | Scheduled scans identify candidates; automation triages meaningful changes and optionally starts a bounded investigation |
| Notifications | Slack incoming webhooks, Discord webhooks, and signed generic outbound webhooks; delivery status and retries |
| Efficiency | Compact evidence, change gates, deduplication, bounded context/tool calls, budget reservations, queue isolation, and reuse |
| Offline demo | Existing deterministic demo plus clearly marked provider/delivery fixtures; no cloud key needed |

Example: checkout errors rise → existing detector records a finding → configured alert rule sends a factual notification → triage recommends investigation → automation policy admits it within budget → an investigation collects approved evidence and suggests checks → an optional report-ready notification links to the report. Provider failure must not interrupt detection or the first factual alert.

“Decision model” means a model that selects a small typed answer, such as `observe` or `investigate`. “Reasoning model” means the model used for deeper evidence analysis and follow-up. These are selectable roles, not a requirement to pay for two providers or invoke two models on every request.

## 15. Providers, models, and data handling

**Delivered Slice B:** explicit user-requested analysis of existing evidence, one selected investigation model per provider, synthetic capability checks, privacy preview, cost admission and usage records. One invocation per run, no tools or automatic calls. Separate triage model roles are now available through the advisory Slice C increment below; Decisions, multi-step exploration and follow-up remain later work. The concrete current limits and provider retention caveats are documented in [AI operations](../docs/ai.md).

Provide separate Go integration boundaries for `Triage` and `Investigate`, with provider-specific wire formats hidden behind internal typed requests/results. Support:

- **OpenAI:** Responses for structured analysis and tool requests; optional Decisions adapter for compact typed classification. The current Decisions API documents `predicate`, `choice`, `score`, and refusal answers. It is not the tool-execution interface. Model/account availability must be checked rather than assumed. See [Decisions guide](https://developers.openai.com/api/docs/guides/decisions) and [API reference](https://developers.openai.com/api/reference/resources/decisions/methods/create).
- **Anthropic:** Messages for structured classification, analysis, and tool requests. It does not need to emulate OpenAI Decisions on the wire. See [Claude structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs).
- **No AI:** deterministic findings and reports remain fully usable. A disabled/unavailable provider produces an explicit fallback status.

A workspace chooses a triage provider/model and an investigation provider/model. Optional fallback configurations must be explicitly enabled and independently allowed to receive the service's data. Never silently switch vendors or choose a more expensive model. Model IDs, capability metadata, and price assumptions are versioned configuration, not hardcoded throughout business logic. Setup tests availability using synthetic data and reports unsupported endpoints/models clearly. Do not claim that listing a model proves its tool or structured-output capabilities.

Provider credentials are write-only, encrypted with authenticated record-bound encryption, and never sent to the browser after saving. Disabling a provider stops new dispatches; deletion removes its secret while retaining sanitized invocation history. Expand the existing key-rotation transaction and backup/restore verification to cover every new provider and destination secret.

External AI processing starts disabled. Setup presents a sample of the exact bounded payload and requires enabling it for the workspace and selected services. Counts, selected labels, user questions, evidence excerpts, and conversation history can all contain sensitive material: minimize and sanitize every transmitted field. Raw upstream responses, source credentials, arbitrary URLs, and unbounded logs are excluded. Services may prohibit excerpts or all AI processing. Provider data policies depend on endpoint/account configuration; do not promise zero retention from setting a single request flag.

Sanitize generated text before persistence, display, cache storage, or notification use; apply the same policy to saved user questions. Store invocation purpose, provider/model, policy/prompt/schema versions, evidence IDs and content digest, status, sanitized error, usage, reserved/estimated cost, and latency. Do not store private model reasoning. AI reports and conversations use the evidence retention period by default; expiry removes their copied excerpts and content as well as cached payloads. Retain only bounded operational metadata afterward. Users can delete a conversation and disable future processing. Provider-side deletion guarantees must be described separately.

## 16. Decision policy, reactive investigations, and proactive operation

### Delivered recommendation-first increment (2026-10-11)

The first Slice C increment implements local rules only, with **Off** (default) and **Recommendations only** (`triage_only`) modes. Select services explicitly; demo findings require a separate opt-in. New occurrences, severity changes and qualifying growth commit durable evaluation work with findings. A separate single-worker River queue groups changes for 60 seconds, rechecks policy/mapping/monitoring, lifecycle and retained complete evidence, then records a recommendation or skip reason. No model call, automatic investigation or new notification event originates from this pipeline.

Recommendation cooldown defaults to 30 minutes (5–1440 allowed); relative growth defaults to 2× (1.5–10 allowed) and must also meet the detector's absolute minimum. Log counts are normalized per sampled minute. Window/threshold changes reset comparison. Settings revisions invalidate queued old-policy work and seed existing findings on their next observation; enabling does not replay a backlog. Skipped evaluations advance the baseline, so cooldown expiry alone does not requeue them. Jobs older than 15 minutes or with exhausted retries become skipped. Keep 30 days of activity, while retaining checkpoints/cooldowns for deduplication. Settings provides service-filtered cursor history and links to findings. [Automation operations](../docs/automation.md) documents the delivered behavior.

### Delivered advisory AI triage increment

Optional OpenAI Responses and Anthropic Messages structured triage now supplements eligible local recommendations. Separate model/pricing/capability configuration uses existing provider credentials and service/demo consent. Automation requires explicit recurring opt-in. Each task reserves cost in the existing AI ledger, uses the AI queue, permits at most 4,000 estimated input and 1,000 output tokens, and expires 15 seconds after enqueue. The compact numeric summary has no names, log excerpts, fingerprints, selectors or URLs. Dispatch/publication recheck scope, current policies, lifecycle, settings and retained evidence. Failed/unavailable triage leaves the local rule decision intact.

Persist model advice separately as investigate, observe, needs_attention or need_more_data with a short validated/redacted reason and scoped evidence citations. Activity exposes provider/model, accounting, cancellation and reuse. Models cannot change the local decision/cooldown, alter findings or suppress notifications. Separately enabled automatic server policy may admit an investigation based on validated advice. Successful results may be reused for identical scoped evidence and versioned model/policy/request content; generated content expires no later than 24 hours or source expiry. Unknown dispatch outcomes retain reservations and are never automatically replayed.

### Delivered opt-in automatic admission increment

Automatic mode requires explicit recurring consent and a separately selected/tested investigation provider, positive budgets/prices, and service/demo egress consent. A durable admission job applies optional triage (investigate proceeds, other valid advice skips, unavailable triage falls back to local rules, cancellation stops admission). It atomically builds a deterministic report from the triggering scan evidence and enqueues Slice B bounded analysis with a reservation, audit event and separate service investigation cooldown. No fresh telemetry collection or tools are added. Dispatch, in-flight and publication checks enforce automation policy, monitoring, finding lifecycle/recovery, mapping, source, evidence and privacy eligibility. Activity exposes the applied action/reason and linked run. Defaults remain Off; no backlog replay. See [automation operations](../docs/automation.md).

The optional OpenAI Decisions adapter and live usefulness evaluation remain later Slice C work.

Before publishing a recommendation, recheck current Monitoring detector enablement and comparison settings, serialized with settings saves. Both the batch and the actual observation must be at most 15 minutes old. A newer complete scan without the same qualifying signal invalidates its pending item without resolving the finding; missing/disabled coverage does not prove recovery. Older delayed observations cannot undo that recovery, and a newer qualifying observation can restore eligibility. Skips never start or extend recommendation cooldowns. Exact finding links ignore retained list filters and pagination, including browser history navigation.

### Decision policy

The decision model may recommend `observe`, `investigate`, `needs_attention`, or `need_more_data`. The server validates the enum, scope, coverage, and any referenced evidence, then applies a deterministic policy. A model recommendation is never direct permission to call a tool, select a recipient, spend beyond budget, or mutate infrastructure.

Persist both the recommendation and actual action with a short reason. Never treat an uncalibrated model score as an outage probability. Refusal, timeout, malformed output, missing capabilities, and exhausted budget invoke the documented rules fallback. Model prose must not invent explanations for a Decisions response that contains only a typed answer.

Default policy:

| Condition | Result |
| --- | --- |
| No qualifying finding, no material change | No automatic model call |
| Missing/incomplete evidence | Record the coverage issue; no unsupported cause or healthy conclusion |
| Automation disabled, finding snoozed/dismissed/resolved, or service excluded | No new automatic AI investigation; manual inspection remains available |
| Qualifying new/changed finding; triage allowed | Run one compact triage task for the coalesced evidence snapshot |
| Investigation recommended; enabled policy and budgets permit | Start one bounded AI investigation |
| Triage unavailable | Preserve finding; use rules fallback and configured deterministic alert routing |
| Finding matches an immediate alert rule | Send independently of model availability/recommendation |

Do not let AI dismiss, resolve, snooze, lower deterministic severity, or suppress a policy-mandated alert. Automated `needs_attention` recommendations may appear in the UI; external delivery still requires a configured notification rule. Enabling automation authorizes the described recurring behavior without a confirmation on every event. Configuration must provide pause/disable and explain the next eligible action.

### Reactive work

Add **Investigate with AI** alongside the existing deterministic investigation. Initially analyze an immutable evidence snapshot and deterministic report. Slice D adds the explicit bounded tool loop and scoped report follow-up described below. A manual request can bypass the proactive change/cooldown gate, but never privacy, source, deadline, or budget checks.

An AI run has its own persisted state: `queued → analyzing → collecting → analyzing → reporting → completed`, with explicit `insufficient_evidence`, `budget_exhausted`, `cancelled`, and `failed` outcomes. The collecting/analyzing cycle may repeat only within limits. Keep the existing deterministic report available when enrichment fails. Persist monotonically ordered step events; do not reuse the existing unique `(run_id, stage)` constraint for repeated AI stages.

Tools initially expose only existing mapped Prometheus counts, Loki patterns, and evidence retrieval. The server resolves source IDs/selectors from the run snapshot, enforces its service and time bounds, and persists results as evidence before the model can cite them. The model cannot submit freeform PromQL/LogQL, arbitrary HTTP requests, SQL, shell commands, or notification calls. Additional signals need explicit native tool definitions in a later release.

Reports separate observed facts, hypotheses, supporting/contradicting evidence IDs, unknowns, and suggested manual checks. Validate IDs, retention, scope, and schema server-side. Citations establish provenance, not truth; scenario evaluations must test that the text follows the evidence. On invalid output, show the evidence-only fallback. Treat tool output/log instructions as untrusted data and test prompt-injection scenarios. See [OpenAI structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs) for the provider schema mechanism; Sherlock still validates semantics and failure cases itself.

Follow-up questions stay attached to a service/run scope. Use bounded recent turns plus a validated evidence summary; do not resend an ever-growing transcript. Recheck retention and egress policy before each follow-up. Broader service/window requests start a new scoped run. “Send this to a channel” is not an AI tool in this phase.

### Proactive work

Use the current persisted scan schedule. A finding change commits a durable automation event in the same transaction as the finding update. A separate worker groups changes over 60 seconds per service and evaluates eligibility. Ordinary `last_seen` refreshes do not trigger AI.

A material change initially means a new occurrence, deterministic severity change, or configured growth in error proportion/pattern rate compared with the last triaged observation. Every mapping defines the comparable metric/unit and minimum absolute change; a default relative gate is a doubling with the detector's absolute minimum still satisfied. Missing comparison data is a coverage change, not a numerical increase. New evidence content participates in deduplication after this eligibility gate, not as a replacement for it.

Default to one automatic deep investigation per service per 30 minutes, one active AI run per service, and no automatic replay of a backlog when automation is enabled. These are proposed starting limits, adjustable within server caps. Save the last triaged measurement and cooldown durably. Unchanged snapshots reuse validated results; a user can request fresh analysis explicitly. Recheck service mapping, monitoring, policy, finding state, and destination eligibility at dispatch boundaries.

Proactive behavior in this phase means investigating detector-backed emerging symptoms. It does not claim to predict failures from unavailable traces, deployment history, or Kubernetes data.

### Delivered Slice D implementation

Approved evidence/Prometheus/Loki tools and scoped follow-up now use persisted ordered AI steps, separate provider/tool queues, per-step admission, cumulative budgets and no uncertain paid replay. The original deterministic report/event contract remains intact; collecting appears in ordered tool steps while the parent AI run stays active. Manual tool use is explicit and automatic tools require an additional default-false policy checkbox. Follow-up uses at most three recent question/answer summaries and expires within 24 hours/source expiry. The delivered wall deadline is 180 seconds from enqueue, stricter than the proposal below. See [AI operations](../docs/ai.md#bounded-tools-and-scoped-follow-up-slice-d) and [verification](../docs/progress.md).

## 17. Efficiency, budgets, and durable execution

Separate River queues for existing telemetry work, AI work, and notification delivery. Initially allow two AI jobs and two delivery jobs concurrently, with one in-flight call per provider configuration and one per notification destination. Slow model calls must not occupy telemetry workers or prevent alerts. Tune these defaults using measured queue age and latency.

Proposed limits for the first implementation:

| Work | Initial bound |
| --- | --- |
| Triage | One compact synopsis, up to 4,000 input tokens and 1,000 generated tokens where applicable, 15-second total task deadline; no tools |
| Investigation | 180-second wall deadline after starting; at most 6 provider attempts including retries, at most 6 tool requests and 12 underlying telemetry HTTP attempts |
| Investigation context | Up to 12,000 input tokens per request, 30,000 cumulative input tokens, and 8,000 cumulative generated tokens including billed reasoning where exposed |
| Tool output | Existing telemetry caps plus an 8 KiB sanitized model-facing result per tool |
| Follow-up | One request by default; any extra collection uses a new bounded run |
| Retries | At most one retry per provider operation within remaining shared attempts, time, and token/cost reservations |

These are Sherlock design defaults, not provider guarantees. Cap output per call to remaining allowance; count system/tool schema/history in input estimates. Use provider token counters or a documented conservative bound where a tokenizer is unavailable. APIs without a compatible enforceable ceiling may run only with an explicit bounded pricing/admission strategy, otherwise remain unavailable for unattended use.

An administrator must set nonzero daily workspace and per-run cost allowances before enabling automatic AI; there is no implicit unlimited budget. Reserve estimated worst-case request cost transactionally before dispatch, including concurrent workers and retries, and settle from reported usage. Keep unresolved reservations after ambiguous timeouts until safely reconciled; do not free them and repeat paid work blindly. Configure model prices with an effective date; unknown prices disable automatic cost-based admission. Display reservations, estimated spend, actual reported tokens, and budget-blocked jobs separately. Sherlock's admission budget is not a guarantee about the provider's final invoice; provider-side account limits are additional controls.

Cache only successful validated results under workspace/service, origin, mapping revision, evidence digest/window, provider/model, prompt/schema, and egress-policy versions. The cache key excludes unrelated mutable display names. Expiry and privacy-policy changes invalidate cached payloads, and cache use revalidates evidence access. Provider prompt caching may additionally reuse stable prefixes where supported; it does not replace Sherlock result deduplication.

Persist call intent, attempt ID, deadline, cumulative budgets, completed tool results, and accepted response before advancing the run. Resume accepted steps rather than replaying the whole investigation. Do not promise exactly-once billing across a crash: use provider idempotency where documented; otherwise mark unknown-outcome calls and apply a bounded, visible retry policy. Disabling AI prevents future dispatch and requests cancellation of active calls; already transmitted data cannot be recalled.

Expose usage by provider/model/service/purpose, cache hits, fallback counts, token/cost estimates, queue age, latency, and notification failures. Optimize against a fixed scenario suite: cost per useful investigation, factual quality, missed critical cases, and duplicate alerts. Do not select models only by advertised price or self-reported confidence.

## 18. Outbound alerts and messages

Slice A implementation uses a fixed no-excerpts payload policy: bounded factual titles/service names, IDs and links only. Configurable excerpt release remains deferred. See [notification operations](../docs/notifications.md) and [progress](../docs/progress.md) for delivered behavior and verification.

Support three destination types: Slack incoming webhook, Discord webhook, and generic JSON webhook. A destination has an encrypted secret URL/credential, name, enabled status, allowed origins/services, event subscriptions, severity filter, and redaction/excerpt policy. Demo delivery is disabled by default; an explicit test action sends a clearly labeled synthetic message to the chosen destination.

Initial events are `finding.created`, `finding.escalated` (deterministic severity change), `investigation.completed`, and `finding.resolved` (manual lifecycle event, not proof of recovery). Ordinary scan updates send nothing. Acknowledged findings suppress routine repeat messages by default; snoozed/dismissed findings suppress automatic messages until policy permits a new occurrence. Resolution/report-ready subscriptions are optional. Route each event through filters, quiet hours, grouping, deduplication, and per-destination limits. The default grouping window is 60 seconds; explicitly configured immediate rules bypass grouping and AI, while still respecting transport rate limits.

Commit an immutable notification event/outbox row with the domain change. Delivery uses a stable event ID and unique `(workspace, destination, event)` job identity. Retry network failures, 429, and retryable 5xx with jitter and bounded backoff, respecting `Retry-After`; permanent authentication/configuration failures become visible failures. Initial retry bound: eight attempts and 24 hours, whichever expires first. Disable/delete cancels pending delivery and is rechecked before each dispatch. Only successfully acknowledged deliveries are marked sent. Provide attempt history, sanitized error, pending/retrying/sent/failed/cancelled status, and controlled manual retry. Retain operational delivery metadata for 30 days; payload copies expire no later than their source evidence/report, and expired content is never retried.

This is durable at-least-once delivery, not a promise of exactly-once external messages. A receiver may accept a request before the connection fails. Include an event ID; generic receivers can deduplicate it. Slack/Discord transport limitations and unknown outcomes must remain visible rather than being mislabeled as success.

Provider behavior:

- Slack uses a channel-bound incoming webhook and platform-specific payload formatting. Initial support sends new messages; do not promise edits/deletes, threaded lifecycle updates, or interactive actions from this transport. See [Slack incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/).
- Discord uses native webhook payloads, `wait=true` for acknowledgement, saved returned message IDs when present, bounded content/embeds, and `allowed_mentions` with no automatic mentions. See [Discord webhook execution](https://docs.discord.com/developers/resources/webhook#execute-webhook).
- Generic JSON has a versioned envelope with event ID/type/time, minimal service/finding/report metadata, and an authenticated Sherlock link. Sign `timestamp + "." + exact_body_bytes` using HMAC-SHA256 and a separate write-only signing secret. Send signature version and timestamp headers; document a five-minute receiver replay window plus event-ID deduplication. Each retry uses a fresh timestamp/signature and the same event ID/body.

Use factual, deterministic notification templates; AI enrichment is optional and clearly identified. Default payloads contain concise summaries and links, without log excerpts. Sanitize all user/model text and disable automatic mentions/unfurls where supported. The model cannot choose webhook URLs, credentials, recipients, headers, or message routing. Delivery rechecks data-release policy; an already queued payload cannot bypass a later opt-out.

Webhook URLs are bearer secrets: mask them in UI and never include them in diagnostics. Enforce HTTPS, bounded request/response sizes and timeouts, DNS/destination validation, no redirects, and no forwarding secrets across hosts. Official provider/Slack/Discord host policies and generic webhook destinations are separate from telemetry allowlists. Explicit deployment policy may allow a private generic receiver or local test fixture; it never allows metadata/link-local access. No insecure TLS toggle. Extend key rotation to webhook URLs and HMAC secrets before release.

Links use the configured public application URL. Warn when a localhost-only URL will not work for channel recipients; do not make reports public to solve that. One-way webhooks do not provide Slack/Discord chat commands or replies; those require a separately scoped bot integration later.

## 19. Slice E — inbound alert triggers

The delivered generic authenticated inbound alert endpoint builds on outbound delivery and bounded AI investigations. This permits a monitoring system to request an investigation; it is separate from sending messages to Slack/Discord. Native Alertmanager payload support is a subsequent adapter, not implied by accepting arbitrary JSON.

An administrator creates a binding to an existing service, allowed origin, shared signing secret, and rate/budget policy. The binding determines scope; the payload cannot choose a workspace, data source, model, tool, recipient, or arbitrary query. Require an HMAC signature over timestamp and exact body, reject stale timestamps, bound body size, rate-limit by binding, and deduplicate external event IDs. The signed request is the endpoint's authentication exception to browser session/CSRF; other APIs keep existing controls.

Commit verified receipt and enqueue transactionally, return an accepted receipt ID, and process asynchronously. Repeated event IDs return the same receipt; conflicting content is rejected. Validate event windows against supported collection horizons; return an explicit stale/unsupported outcome instead of silently investigating a different incident. External labels/text are untrusted context, not observed evidence. Fetch and validate telemetry before producing claims. Trigger loops are blocked by recording event origin and rejecting Sherlock's own outbound event envelope. Binding disablement prevents new dispatches.

Delivered limits: 16 KiB exact-body HMAC-SHA256 requests, ±5-minute signatures, 5–60 minute incidents ending within seven days, and at most two hours including comparison. Bindings default disabled; automatic mode, recurring/service/origin/AI consent and separate tool consent remain mandatory. Receipts snapshot consent revisions, use existing cooldown/cost admission plus binding request/daily limits, and never replay skipped work. Titles remain history-only untrusted context for 24 hours; receipt metadata/dedup lasts 30 days. See [inbound operations and exact signing example](../docs/inbound.md).

## 20. Phase 2 UI, acceptance, and delivery status

Extend existing navigation without placeholder screens:

- **Settings → AI:** providers, masked credentials, synthetic connection tests, separate triage/investigation roles, capability status, payload preview, egress consent, per-service exclusions, usage and budget controls.
- **Settings → Automation:** off/triage-only/auto-investigate, change gates, cooldown, pause, and an explanation of why a run was launched or skipped.
- **Settings → Notifications:** destinations, event/severity/service/origin filters, quiet hours, preview/test send, delivery history, and failures. Separate **Settings → Inbound alerts** exposes disabled-by-default service bindings and receipt history.
- **Findings:** triage recommendation, applied policy/reason, investigation status, and delivery status alongside existing lifecycle controls.
- **Investigations:** deterministic report plus optional AI analysis, evidence-linked hypotheses, unknowns, usage/limits, cancellation, and scoped follow-up.

Implement versioned API schemas alongside each slice; do not add unimplemented endpoints to the current OpenAPI document. Add provider configurations, automation policies/events, AI runs/steps/invocations/budget reservations, conversations, notification destinations/events/deliveries, and inbound bindings/receipts through new migrations. Every domain row is workspace-scoped, with composite ownership constraints. Keep provider-specific response blobs out of core finding identity.

Acceptance requires existing foundation tests plus: provider contract fixtures for valid/refused/malformed/truncated/rate-limited responses; concurrent budget admission and ambiguous timeout tests; key rotation across all secret types; restart/cancel/disable at every outbound boundary; prompt-injection and cross-scope evidence tests; no repeated AI calls on unchanged scans; alert delivery while AI is unavailable; delivery retry/dedup/expiry tests; webhook signature/replay checks; and Playwright coverage of configuration, reactive/proactive runs, follow-up, and delivery failures.

Maintain a versioned evaluation set covering the demo, healthy noise, insufficient data, overlapping incidents, contradictory evidence, and malicious log instructions. Require all deterministic safety/contract assertions to pass and compare AI report claims with labeled facts. Record usefulness and cost measurements before choosing default model IDs. Fixture success is not live provider certification: each enabled provider and notification adapter needs a documented, bounded live smoke test using user-supplied credentials and an explicitly selected test destination before release. No credentials or external test messages are required for this planning task.

Phase 2 Slices **A/B are implemented and locally verified with fixtures**; local recommendations/history from C are also delivered, with advisory OpenAI/Anthropic triage and opt-in automatic admission delivered; Slices D bounded tools/follow-up and E signed inbound triggers are implemented. The [implementation plan](../docs/phase2-plan.md) defines ordered slices and acceptance gates. Existing no-AI demo behavior remains the baseline throughout implementation.
