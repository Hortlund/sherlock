# Progress

**Current status:** all five MVP slices and Phase 2 Slices A/B notifications and reactive OpenAI/Anthropic analysis implemented; local verification passed. See the final entry for evidence and limits. Earlier entries record the state at that point in development.

## 2026-10-10 — Specification revision

- Preserved the original SPEC, AGENTS, and CODEX_START_HERE files under `INFRA_SHERLOCK_CODEX_KIT/reference/v1.0/` with SHA-256 checksums.
- Replaced the active specification with v1.1: a focused Go/React/PostgreSQL MVP, explicit deferred roadmap, five implementation slices, and release acceptance criteria.
- Aligned the build handoff and agent instructions, including a project-root pointer so future application work finds the active contract.
- Recorded decisions and assumptions. No application code, dependencies, containers, or application tests exist yet.

Verification completed: archive copies were compared byte-for-byte before editing, and all three recorded SHA-256 checksums still match after the revision. A Python document check passed for seven active documents, eight relative links, balanced code fences, sections 1–13, five implementation slices, and section references. An `rg` scope review confirmed the handoff and instructions consistently defer the broader features. Application tests are not applicable to this documentation-only change.

## 2026-10-10 — Slice 1: secure foundation

Implemented Go HTTP API, React/Vite setup/login/overview/settings, PostgreSQL/sqlc, locked/checksummed migrations, atomic token-protected bootstrap, Argon2id passwords, hashed persistent sessions, exact-origin/CSRF checks, database-backed auth throttling, password change/revocation, installation encryption-key verification, Docker Compose, and CI configuration.

Verified locally: `make test` (race-enabled unit tests and four PostgreSQL integration tests), `make lint`, production React build, `make e2e` (desktop/mobile bootstrap → login → persisted session → logout → password change → new login), and `docker compose up --build -d --wait`. Both containers healthy; `/readyz` returns ready. App restart also verified. GitHub CI has been configured but not run remotely.

Initial browser test attempted before Chromium finished installing; after installation, the test passed. Screenshots initially captured the entrance animation and were corrected to disable animations during capture. Browser QA uses the in-app browser for the actual Compose installation plus committed Playwright regression tests on a disposable isolated installation. Test credentials never bootstrap the user's installation.

Slice 1 is complete. Slice 2 (direct connections, service mapping, evidence, and demo fixtures) is next. No detector or investigation functionality is claimed yet.

## 2026-10-10 — Slices 2 and 3

Slice 2 delivered encrypted Prometheus/Loki source CRUD/testing, strict destination and query bounds, manual mappings, genuine adapter-backed preview, immutable evidence and separate synthetic sources. Unit, PostgreSQL integration, and two browser tests passed before beginning slice 3. Browser screenshots were compared with the connections concept; native form fields, table, teal accents and gray navigation rail preserve the intended design.

Slice 3 delivered River jobs, persisted/coalesced schedules, immutable run snapshots and budgets, three guarded detectors, deduplication, lifecycle/feedback, and Overview/Findings/scan coverage UI. Verified detector scenarios, a queued job processed by a new application instance, replay/overlap dedup, lifecycle transitions and retained request budget in a PostgreSQL integration test. Three Playwright journeys passed, including demo scan, evidence, acknowledge/resolve and reload on desktop/mobile. A first recovery test lacked the restarted instance's explicit loopback allowlist; fixing the test configuration made it pass. Full regression suite will run again with the completed investigation slice.

## 2026-10-10 — Slices 4 and 5: local MVP complete

Delivered durable deterministic investigations with scoped snapshots, persisted progress, authenticated polling/replay, cancellation, deadline/request-budget preservation, evidence provenance validation, reports, unknowns and suggested manual checks. Delivered configurable detector/collection settings, label-name suggestions, private redaction patterns, source availability on retained evidence, expiry tombstones and cleanup, authenticated system counters, transactional encryption-key rotation, and backup/restore instructions and tooling. OpenAPI documents the implemented surface.

Final local verification:

- `make lint`: Go formatting/vet and TypeScript check passed.
- `make test`: race-enabled Go suite and all ten PostgreSQL integration tests passed. Coverage includes queued-job recovery with a new application instance, replay/dedup, persisted schedule and disable/mapping boundaries, cancellation/deadline/budget preservation, report scope, lifecycle, retention, actual pg_dump/pg_restore, and encryption-key rotation/rollback.
- `make e2e`: five browser journeys passed: secure setup/session/password flow; adapter-backed demo/connection controls; scan/findings/evidence/lifecycle; investigation/report/progress/reload; seeded Overview performance. Desktop and 390px mobile screenshots were visually inspected. Latest measured p95 was 15 ms across 20 warm local Overview navigations in this small seeded installation; this is not a production load benchmark.
- `make real-sources`: passed against actual Prometheus v3.2.1 and Loki v3.4.2 containers, with imported counters and pushed logs. Verified current/comparison counter calculations, Loki windows, redaction, absent data, and stale counters. Containers and their disposable volumes were removed afterward. Contract fixture tests separately cover HTTP errors, private CA verification, response caps, cancellation/timeouts, log truncation and hostile/secret-shaped text.
- `docker compose --profile demo up --build -d --wait`: app, Postgres and synthetic source running; app/Postgres health checks passed. The user's installation remains uninitialized and ready for their own administrator credentials. No test account was added to it.
- In-app browser: rebuilt setup UI rendered correctly; no console warnings/errors. Setup and connection screenshots were compared with generated concepts. Copy, gray/white layout, teal accents, typography hierarchy, native controls and icons were preserved; responsive reports remain readable without horizontal overflow.
- Archived v1.0 SPEC/AGENTS/handoff checksums still match the recorded originals exactly.

All five slices are implemented and locally verified. CI configuration exists but has not run on a remote repository. Production telemetry, reverse-proxy deployment and load beyond the small local fixture have not been tested. Progress deliberately uses the documented polling fallback, not SSE. No AI, Kubernetes, external notifications or remediation features were added.

Final hardening follow-up: bounded each custom redaction replacement before the next pattern, and added cancellation checks while processing log entries. An intentionally extreme ten-pattern race test was interrupted after exposing unnecessary repeated sanitization work; the implementation was simplified and the relevant capped-log/expanding-pattern connector suite passed in 5.7 seconds. Lint and the final Compose rebuild passed afterward. App restart returned `ready` and `initialized:false`; the disposable PostgreSQL test container/network was removed. The app, persistent database and demo fixture remain running.


## Review fixes — 2026-10-10

Addressed all seven review findings:

1. Full-line redaction matches before excerpt truncation, including overlapping patterns and a fail-closed match limit.
2. Deadline-aware per-source waiting rather than dropping concurrent scans; unrelated sources can proceed.
3. Stable finding identity across service metadata/monitoring/no-op edits, with mapping changes still invalidating the revision. Scheduled finding writes also recheck monitoring.
4. Independent Prometheus/Loki selectors with legacy mapping and queued-snapshot compatibility.
5. HTTP 409 for an active investigation with different windows, mapping revision or settings; equivalent concurrent launches reuse one run.
6. Overview freshness follows cadence plus five minutes of grace.
7. A separate terminal-state event fetch prevents completion/cancellation progress from being lost between polls.

Regression verification: the connector tests cover excerpt-boundary disclosure, overlapping redactors, waiting/cancellation, unrelated-source progress, and distinct selector queries. PostgreSQL tests cover shared-source scans, lifecycle preservation, equivalent/conflicting launches and cadence thresholds. Playwright covers final completion/cancellation events and editing/saving distinct selectors from a legacy mapping. The existing demo/auth/findings/report workflows remain covered.

- `make e2e`: all 8 browser tests passed (5 existing, 3 regression cases). Desktop selector editing and mobile cancellation layouts were checked; no runtime errors in the added UI cases. Browser tests run on isolated localhost port 18080. Screenshots are under `/tmp/sherlock-review-*.png`, outside the repository.
- `make real-sources`: actual Prometheus/Loki contract checks passed; disposable source containers and volume removed afterward.
- `make lint test`: formatting, Go vet, TypeScript checks, race-enabled unit checks and all 15 PostgreSQL integration tests passed, including backup/restore and the new monitoring-disable guard.
- `docker compose up --build -d --wait app`: rebuilt the local app successfully; app and database health checks passed. The persistent installation and credentials were preserved.

## Follow-up review fixes — 2026-10-10

Addressed all four regressions from the second review:

1. Loki-only service editing handles canonical `error_labels: null` without crashing.
2. Prometheus-only mappings can save with empty unused log patterns. The API still rejects missing error/warning patterns when Loki is configured.
3. Redaction match-limit failures withhold the affected line from fingerprint groups and mark the observation incomplete. Both log detectors abstain when either comparison window is incomplete, including when excerpts are disabled.
4. Built-in redaction retains the original v1 field-name and email markers, including nested/overlapping built-in matches. Full-line custom matching, conservative custom-overlap redaction, and UTF-8-safe excerpt limits remain. Golden hashes and database tests verify that existing acknowledged findings and dismissed findings within cooldown do not duplicate.

Verification:

- `make lint unit`: formatting, Go vet, TypeScript, and race-enabled Go tests passed. Added fixed v1 fingerprint goldens, overlap/adjacency and match-limit boundaries, UTF-8 truncation, and collector-to-detector abstention checks.
- `make integration`: all 17 PostgreSQL integration test functions passed, including the previous seven-fix regression coverage. The new single-source round-trip test initially expected HTTP 201 instead of the API's existing HTTP 200; its assertion was corrected and the full suite rerun successfully. Both new integration tests were rerun after the final overlap adjustment and passed.
- `make e2e`: all 10 browser tests passed. The two new single-source cases use canonical API-shaped responses, save/reopen twice, preserve the selected source's labels, and assert no page errors. The API integration test separately verifies those canonical responses and source-dependent validation.
- `make real-sources`: actual Prometheus/Loki contract checks passed; disposable source containers and volume were removed. These checks preceded the final built-in overlap compatibility refinement, which was covered by the final race-enabled unit and targeted database runs.
- Rebuilt the local application with `docker compose up --build -d --wait app`; app/database health checks passed and `/readyz` returned `ready`. Persistent installation data and credentials were preserved. The disposable PostgreSQL test container/network were removed.

Updated the OpenAPI nullable mapping field, operational redaction behavior, and implementation decisions. Historical reference specifications remain untouched.

## 2026-10-10 — Phase 2 spec and implementation plan

The user requested OpenAI/Anthropic support, decision models, reactive/proactive behavior, efficiency, and Slack/Discord/webhook delivery, and explicitly selected specification/planning before implementation. Updated the active spec to v1.2 and added [the ordered Phase 2 plan](phase2-plan.md).

- Planned separate triage/investigation roles, optional OpenAI Decisions, provider-independent structured results, bounded read-only tools, follow-up, and evidence-backed reports.
- Specified material-change gates, coalescing, cooldowns, queues, token/time/tool limits, durable cost reservations, and honest fallback/unknown-outcome behavior.
- Specified Slack/Discord/signed generic outbound delivery first, independent of model availability; signed inbound alert triggers are a later slice. Included filters, retries, delivery history, expiry, and no implicit two-way bot support.
- Added egress consent/preview, per-service exclusions, output redaction, generated-content retention, and rotation/restore requirements for all new secrets.
- Aligned root/kit instructions, handoff, README, and architecture decisions with the planned scope. No Phase 2 feature is marked implemented.
- Archived the three v1.1 kit files unchanged; all six v1.0/v1.1 archive checksums match the recorded values. Documentation validation checked 23 local links, balanced Markdown fences, and ordered spec sections 1–20.

This was documentation-only. Consulted official OpenAI, Anthropic, Slack, and Discord documentation; references are linked in the spec. No runtime code, database migrations, deployment configuration, credentials, or running services were changed. No paid AI calls, external test messages, or application test suites were run for this planning revision.

## 2026-10-10 — Phase 2 Slice A: outbound notifications

Implemented Slack incoming webhooks, Discord webhooks and signed generic receivers in Settings. Includes encrypted write-only URLs/signing secrets, revision-checked edits/deletion, live/demo/service/event/severity filters, IANA-timezone quiet hours, 60-second grouping, synthetic preview and confirmed test sends, paginated delivery history and controlled manual retry. Payloads use bounded factual titles/service names, IDs and authenticated links; excerpt release is fixed to off. AI and inbound triggers remain unimplemented Slices B–E.

Added transactional notification events/River outbox hooks for new findings, severity increases, manual resolution and completed investigations. Ordinary scan updates do not create alerts. A separate two-worker queue and per-destination advisory lock bound delivery. Attempts persist before dispatch; crashes/lost acknowledgements remain unknown rather than being falsely marked sent. Eight attempts and 24 hours/source expiry bound retries, including manual retries. Frozen retry bytes/IDs, 429 delays, lifecycle/configuration rechecks, cancelled stale configuration, expiry cleanup and 30-day operational metadata retention are implemented. Destination edits use the same lock order as workers and do not wait for outbound HTTP.

Verification:

- `make lint unit integration`: formatting, Go vet, TypeScript and race-enabled unit checks passed. All 23 PostgreSQL integration test functions passed, including all 17 prior tests and six notification tests. Covers concurrent event fan-out/replay, rollback/outbox atomicity, grouping/live-vs-demo, snooze suppression, resolution provenance/expiry, severity/report event dedup, 429/permanent failure/manual retry, unchanged retry bytes, quiet hours, eight unknown-outcome attempts, deletion/retention, old-event suppression after edits, restart recovery, concurrent dispatch/disable, secret rotation and actual pg_dump/pg_restore with queued notification jobs/secrets.
- Notification transport contract tests passed: official URL restrictions, explicit generic HTTP/network exceptions, signed exact request bytes, redirect refusal, Slack acknowledgement, Discord wait/message IDs/retry-after, mention controls, and quiet-hour DST transitions. Platform contract tests emulate responses; they are not actual Slack/Discord channel sends.
- `make e2e`: all 11 browser tests passed. The new test configures a generic local receiver, verifies blank secret fields on edit, previews without network traffic, runs a demo scan, checks signed factual alerts, observes HTTP 401 failure, manually retries through HTTP 429 and then HTTP 204, inspects all three attempts, and deletes the destination. Existing authentication, evidence, investigations and review regressions remain green. Desktop and 390px mobile screenshots were inspected; no horizontal overflow or runtime errors in the new journey.
- Initial browser runs exposed a test login-rate-limit collision, an ambiguous select label, and a stale browser cache after direct API fixture creation. Reused the session already verified by the auth suite, named the select explicitly, and refreshed fixture state. The final test waits for the receiver's 429 before switching it to success, avoiding a timing-dependent retry assertion. Authentication limits remain unchanged.
- `make real-sources`: actual local Prometheus/Loki checks passed; disposable source containers/volume were removed. Notification fixtures never contacted external services.
- OpenAPI YAML parsed and all local schema references resolved; 28 Markdown local links and balanced code fences checked. All six v1.0/v1.1 archive SHA-256 checksums still match. Updated notification setup/signing documentation, operations, API contract, plan and decisions.

No real Slack/Discord credentials or selected cloud test channel were supplied, so live destination smoke tests remain a deployment gate. No AI provider calls, paid usage, inbound alert ingestion, or production remediation were added. Rebuilt with `docker compose up --build -d --wait app`: app/Postgres health checks passed and `/readyz` returned `ready`. The existing initialized installation, credentials, database volume and demo source were preserved; no fixture destinations or accounts were added to it.

## 2026-10-10 — Phase 2 Slice B: OpenAI and Anthropic

Implemented encrypted write-only provider configurations, workspace/per-service/demo/excerpt consent, explicit model/prices/date, synthetic capability checks, input previews, and reactive structured analysis of completed investigations. Both adapters normalize to one evidence-linked report contract. The UI shows deterministic and AI reports together, actual reported tokens/configured cost, cancellation, unknown reservations, local deletion and verified-charge reconciliation. No automatic model calls, Decisions/triage roles, tools, follow-up or inbound triggers are shipped in this slice.

Admission and configuration changes serialize under a workspace lock. A separate two-worker AI queue and provider advisory lock enforce bounded concurrency. Calls persist dispatch intent and have no automatic paid retry; uncertain usage remains reserved across restarts and UTC days. Evidence and policy are checked before sending and before publishing; invalid output, scope changes, expired evidence, refusals and truncation keep the original report available. Provider keys participate in rotation/backup/restore/deletion. Copied payloads clear at terminal states; reports expire with source evidence. [AI operations](ai.md) documents exact limits, data release, setup and accounting assumptions.

Verified locally:

- `make unit` and `make lint`: race-enabled Go unit/contract checks, formatting, vet and TypeScript passed. Provider fixtures cover both official request formats, refusals, truncation, malformed/missing/negative/overflow usage, 401/429/408/5xx, lost acknowledgements and bounded input. Report fixtures cover unsupported authority fields, missing unknowns/support, cross-scope citations and output redaction.
- `make integration`: all 28 then-current PostgreSQL test functions passed, including five new AI functions (both-provider subtests), concurrent budget admission, unknown reservations/reconciliation, crash recovery without dispatch replay, in-flight deletion/cancellation, expiry, output validation, rotation and actual `pg_dump`/`pg_restore` preserving pending AI jobs and encrypted keys. The final sixth AI function passed separately with the race detector: stale preview after model change, cross-workspace access, service exclusion, malicious excerpt isolation/redaction and disabled evidence sources. There are now 29 database test functions in total; the isolated browser-server entry point skips normal test runs.
- `make e2e`: all 12 Chromium tests passed on the final UI, including previous auth/demo/findings/notifications/review regressions and the new provider workflow. A transport fixture compiled only into the test binary exercises real API, database and River jobs. Setup/test both providers → exact payload preview → report/citation inspection → malformed/refused fallback → cancel → budget denial all passed. An older saved success is displayed correctly even after a newer failed attempt. No relevant console/page errors.
- Browser plugin not available; used the repository Playwright workflow at `http://127.0.0.1:18080`, desktop 1536×1024 and mobile 390×844. Page identity, meaningful content, absence of framework error overlays, interactions and screenshots checked. Desktop settings/report and mobile report screenshots are in `/tmp/sherlock-foundation-qa/ai-*.png`; no horizontal mobile overflow. These are simulated reports, not actual provider responses.
- `make real-sources`: real local Prometheus/Loki HTTP checks passed; their disposable source containers/volume were removed.
- OpenAPI YAML parsed successfully: 51 paths including all 12 implemented AI routes, local references and mutation CSRF parameters checked. Local Markdown links/fences and all six archived v1.0/v1.1 checksums verified.
- Rebuilt the existing installation with `docker compose up --build -d --wait app`; app/database healthy, `/readyz` ready, setup remains initialized. Existing data/credentials preserved. Disposable PostgreSQL test container/network removed after verification.

Initial tooling attempts found uncached latest Prettier and absent default YAML modules; validation used the already available pinned Prettier/YAML tooling without changing application dependencies. A test-fixture syntax error was corrected before the passing test runs. The preview/model binding and cached-report selection issues found during implementation have explicit regression coverage.

No real OpenAI/Anthropic calls or new external notifications were made. Account-specific capability/usage smoke tests and model usefulness remain a deployment gate; input admission is a documented estimate and is not a guarantee about provider invoices. Slices A/B are locally verified; C–E remain planned.

## 2026-10-10 — AI integration review fixes

Fixed all three reported defects:

- Provider selection now invalidates pending previews with a generation counter. Late responses cannot replace a newer selection's preview, and sending checks the selected provider again.
- AI history now returns stable cursor pages of 50 records, scoped to the workspace and optional investigation. Settings and investigation history offer **Load older AI runs**; unknown charges, including synthetic tests, remain reachable for reconciliation beyond the first page.
- Provider dispatch uses the freshly rebuilt, digest-verified compact payload rather than PostgreSQL's JSONB serialization. Preview, admission and dispatched request bytes agree, including dense excerpts near the 12,000 input bound.

Verification:

- `make lint unit`: Go formatting/vet, TypeScript, and race-enabled unit/contract checks passed.
- `make integration`: all 31 PostgreSQL integration test functions passed with the race detector. New regressions traverse 52 records with equal-time ordering, validate cursors/filtering, reconcile the old unknown reservation, and compare preview/admission/actual transport bytes at the JSONB size boundary for both providers.
- `make e2e`: all 14 browser tests passed. New intercepted browser cases exercise an OpenAI preview arriving after an Anthropic preview and verify the submitted provider/digest; load 50 newer records plus an older unknown charge and reconcile it through Settings. Existing real API/worker fixture workflows for providers, notifications and the deterministic app also passed.
- The first browser run exposed a timing assumption in the existing notification test: its five-second assertion competed with River's five-second scheduler interval when the second delivery was snoozed for destination serialization. Extended only that assertion to 15 seconds; the complete rerun passed. Production notification timing and policy are unchanged.
- New mobile history screenshot inspected at 390px; no horizontal overflow or page errors in the new browser cases. Screenshot: `/tmp/sherlock-ai-history-review-mobile.png`.
- Updated AI operations, OpenAPI, decisions and the plan. Parsed all 51 API paths, resolved 356 schema references, checked the paging contract and verified all six historical archive checksums unchanged.

Provider traffic remained simulated; no paid cloud calls or real external notifications were made. Prometheus/Loki adapters were unchanged, so their separate real-source suite was not rerun for these fixes.

Rebuilt the existing local app with `docker compose up --build -d --wait app`. App/Postgres are healthy, `/readyz` returns `ready`, and setup still reports `initialized: true`. Existing installation data, credentials and the running demo source were preserved. Removed the disposable test database container/network after checks; no production volumes were removed. Refresh `http://localhost:8080` to load the fixes.


## 2026-10-10 — Workspace UI and UX redesign

Reorganized the workspace into grouped navigation, a useful overview, dedicated Settings sections, and separate investigation Report / AI analysis / Run activity views. Added consistent typography, spacing, status colors, responsive layouts and focused editors for connections, service mappings, AI providers and notification destinations. Evidence expands beside its observation; findings lifecycle actions are grouped, and service metadata no longer competes with a separate environment column. Shared primitives live in `web/src/components/WorkspaceUI.tsx`, with workspace styles separated from authentication styles.

Verification:

- `make lint unit` passed: Go formatting/vet, TypeScript and race-enabled unit/contract checks. Production frontend build passed. No backend/schema/dependency changes.
- Final `make e2e`: all 17 Chromium tests passed. Existing real API/worker fixture journeys cover authentication, sources, scans, findings lifecycle, reports, both AI providers, notification delivery and earlier regressions. Three new cases cover section/deep-link/history behavior, skip-link focus, modal focus cycling/Escape/restoration, mobile navigation, report views and inline evidence. A focused rerun regenerated screenshots after making screenshot capture wait for loaded settings and disable transient animations.
- Testing exposed and fixed a stale findings cache introduced by the new overview, keyboard focus escaping the final drawer control, and a skip link that would otherwise replace the Settings hash. Existing selectors were updated to use the new explicit views. No new runtime/console errors in the new journeys.
- Inspected the running demo through Codex's in-app browser at 847px wide, including Settings, Services and inline investigation evidence. Used repository Playwright tests for repeatable 1536×1024 desktop, 847×900 tablet and 390×844 mobile checks. Relevant mobile pages have no document-level horizontal overflow; navigation/tables have contained scrolling.
- Compared the generated settings/report concepts and final screenshots with `view_image`: navigation grouping, main/context columns, typography, white/gray/teal palette, copy hierarchy, controls and responsive stacking. Increased report reading size/context width, fixed the cramped account footer, preserved active-tab styling on hover and simplified the service table. Intentional differences retain real evidence wording, configured consent/state, existing icon metaphors and plain provider names; simulated fixture banners appear only in test screenshots.
- Screenshot evidence remains outside the repository in `/tmp/sherlock-redesign/` and `/tmp/sherlock-foundation-qa/`. Design concepts are retained in `docs/design/`. Existing archived specs are unchanged.
- Rebuilt the existing demo with `docker compose up --build -d --wait app`; app/Postgres healthy, `/readyz` ready and setup still initialized. Data, credentials and demo source preserved. No real cloud AI calls or external notification messages were made.

Removed the disposable test database container/network after verification. Refresh `http://localhost:8080` to use the redesigned app. Proactive triage, tools/follow-up and inbound alerts remain planned Slices C–E.

## 2026-10-10 — Workspace UI review fixes

Fixed both review findings in `web/src/workspace.css`: saved finding feedback now has a tinted, bold, underlined selected state, and AI provider rows wrap long model IDs within their available width.

Verification: `npm run build --prefix web` and `git diff --check` passed. A temporary Playwright script with intercepted API fixtures checked 1536×1024 and 390×844 viewports: both feedback choices update their visible selected state, selection remains visible after reload, a 120-character uninterrupted model ID stays within its row without document overflow, and the provider editor opens with the full model value and closes correctly. Page identity/content, framework overlay absence, and console/page-error checks passed. Desktop and mobile screenshots were inspected; script and screenshots remain outside the repository under `/tmp/sherlock-ui-fixes*`. The full backend/browser suite was not rerun for these CSS-only fixes.

Rebuilt the existing demo with `docker compose up --build -d --wait app`; app and PostgreSQL are healthy. Existing installation data and credentials were preserved. Refresh `http://localhost:8080` to load the fixes.

## 2026-10-11 — CI integration pool and cleanup repair

GitHub Actions run 38088142964 timed out in `TestConcurrentScansSharingSourceComplete`: on the smaller runner, pgx's CPU-dependent default pool had four connections, all retained by the four concurrent scans' advisory locks. Their next queries could never acquire a connection. The integration/browser fixture now explicitly uses ten connections, matching `cmd/sherlock`. The concurrency regression has a 15-second context and drains every worker result before returning on failure.

Application cleanup now skips Compose when setup has not created `.env`; test-database cleanup runs as its own `always()` step, including when application cleanup fails.

Verification: reproduced the original timeout locally with `pool_max_conns=4`; the fixed concurrency test passed three consecutive race-enabled runs with the same DSN. `make lint test` and all 14 existing browser tests (`make e2e`) passed on the published-code baseline. Workflow YAML/cleanup conditions parsed successfully, and application cleanup passed without `.env`. The separate uncommitted UI redesign was not included in this CI repair. GitHub runner verification is tracked on the repair PR.
