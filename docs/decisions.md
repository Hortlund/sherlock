# Decisions

## 2026-10-10 — Focused first release

The user selected Go, React, and PostgreSQL and requested a smaller MVP specification before implementation. The first-release contract was [SPEC v1.1](../INFRA_SHERLOCK_CODEX_KIT/reference/v1.1/SPEC.md); the [original v1.0](../INFRA_SHERLOCK_CODEX_KIT/reference/v1.0/SPEC.md) is archived unchanged.

- Use a modular Go monolith, React/TypeScript UI, and PostgreSQL for application state and durable work.
- Focus on direct Prometheus/Loki connections, explicit service/signal mapping, three detectors, finding lifecycle, and deterministic evidence-based investigations.
- Defer Grafana discovery, Kubernetes, AI, MCP, Daily Brief, external notifications, and additional detectors. Preserve their designs as roadmap reference.
- Keep authentication, secret protection, network/query limits, provenance, retention, and restart recovery in the release requirements.
- Demo telemetry must run through the same collector/detector/investigator pipeline as real data.

Implementation recommendations: `net/http`, Vite, React Router, TanStack Query, `pgx`/`sqlc`, REST/SSE, and a PostgreSQL job library. River is preferred pending verification of requirements and licensing; it is not installed or finally selected yet.

Assumptions to validate during implementation: the first deployment can expose direct Prometheus and/or Loki APIs; metric names and label conventions require mapping. The user's actual deployment stack remains unspecified. Initial cadence, comparison windows, retention, and cooldown values are documented defaults, not user-supplied operational requirements.

The five delivery slices are in SPEC Section 12. Implementation was authorized after review; consult `progress.md` for current delivery status.

## 2026-10-10 — Foundation implementation

- Native Go `net/http`; pinned pgx/sqlc and Argon2id implementation. React/Vite, React Router, TanStack Query, and Lucide icons; accessible native form controls without introducing a large component framework.
- Public initialization-status endpoint returns only a boolean. Exact configured Origin is mandatory for mutations, with session-bound CSRF tokens on authenticated writes. Login rotates the current browser session; password changes revoke all sessions.
- Auth throttling persists in PostgreSQL: 60 requests/minute globally, 10 per directly connected IP, and at most two password operations in flight. Ignore untrusted forwarded-IP headers.
- AES-GCM version 1 with record-bound associated data; encrypted installation sentinel detects an incorrect key at startup. Key rotation remains release-hardening work before encrypted source credentials are operated at scale.
- Static React assets are copied into the non-root container and served by Go. No runtime frontend server or external asset/font service. Docker databases use PostgreSQL 18's `/var/lib/postgresql` volume layout.
- Local `.env` is generated without printing secrets; URL-safe database/setup tokens prevent connection-string parsing errors. Never overwrite an existing installation configuration.

## Durable execution (implementation)

River v0.49.0 (MPL-2.0 core, pgx driver; license verified in the pinned module) owns job claiming, retries, rescue, and worker concurrency. We do not use its commercial durable periodic-job feature. Service schedules persist `next_scan_at` in Postgres; a bounded timer only pumps due rows, transactionally inserting a scan and River job while advancing the persisted schedule. Missed ticks coalesce into one current scan. Application run snapshots, deadlines, outbound request counts, and results persist independently of delivery attempts. Fixed windows and transactional finding dedup make replay safe.

Counter queries require fresh series at the window end and at least 80% of one sample per minute across the window. This conservative MVP contract supports counters scraped at least once per minute; slower scrape configurations abstain. It is a coverage guard, not a guarantee of gap-free telemetry. Log absence is meaningful only with a nonempty, complete comparison sample spanning most of its window.


## Investigation transport and provenance

Use the specification's polling fallback for this MVP: persisted stage events with monotonic IDs, a reconnect cursor, authenticated polling every second while active, and stable report retrieval after completion. River cancellation interrupts a running worker's context. Every report citation is reloaded and checked against workspace, service, origin, mapping revision, time window, and retention. Templates describe counts/proportions and unknowns; they do not attribute a database outage or deployment cause.

## Real API verification

Verified query shapes against the official [Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/) and [Loki HTTP API](https://grafana.com/docs/loki/latest/reference/loki-http-api/). The reproducible local contract check uses actual Prometheus v3.2.1 and Loki v3.4.2 query engines, not emulated responses. Newer server releases have not been exhaustively certified; the documented stable API contract is the integration boundary.

## 2026-10-10 — Review corrections

- Match all sensitive patterns against original log text and merge redaction spans before excerpt truncation. Bound match cardinality by withholding a line above the limit; never expose a partial unmatched value. Evidence records redaction v2, while fingerprint hashing/volatile normalization retains its v1 identifier.
- Queue per-source requests before acquiring a global request slot. Waiters share the caller's deadline/cancellation and recheck source/run guards before dispatch; unrelated sources retain access to the global budget.
- Accept independent metrics/log selectors with legacy shared-selector fallback. Normalize both representations for comparison. Only source or signal mapping changes advance the service revision; metadata and monitoring changes preserve lifecycle suppression.
- Serialize investigation launches with the service row and compare active-run windows, revision and settings. Return a conflict instead of silently substituting another investigation.
- Use cadence plus five minutes for the overview freshness threshold. Fetch progress under a terminal-state query key to guarantee a final event refresh without an effect/timer race.

## 2026-10-10 — Follow-up regression fixes

- Treat canonical null metrics labels as empty in the mapping editor. Log pattern requirements are enforced by server-side mapping validation only when Loki is configured, matching the existing source-dependent metric validation. Prometheus-only metadata edits no longer depend on filling unused log patterns. Document the nullable wire field in OpenAPI.
- Return redaction completeness separately from sanitized text. A line withheld by the match bound never enters fingerprint groups, and either incomplete log window prevents both log detectors from asserting findings.
- Preserve the original built-in field-name and email placeholders used by v1 fingerprints, while continuing to match all patterns against full raw lines before truncation. Custom overlaps redact the entire union, including a field name when it matches a custom sensitive pattern. Golden fingerprints and database lifecycle checks protect compatibility; retained evidence remains immutable.

## 2026-10-10 — Phase 2 specification (not implementation)

The user requested AI support for OpenAI/Anthropic, decision models, reactive/proactive operation, efficiency, and Slack/Discord/webhook delivery, then explicitly chose to update the spec and plan first. [SPEC v1.2](../INFRA_SHERLOCK_CODEX_KIT/SPEC.md) is now active. The three v1.1 kit files were archived byte-for-byte with checksums; v1.0 remains untouched.

- Interpret decision models using the original vision's OpenAI Decisions concept: compact typed triage. Keep model roles and the server's deterministic action policy separate. OpenAI Decisions is optional; structured classification through either provider and a rules fallback cover the same product need.
- Reuse the Go/Postgres/River architecture. Separate notification and AI queues from telemetry; use transactional domain events, durable budgets/checkpoints, and explicit unknown-outcome handling.
- Deliver outbound Slack/Discord/signed generic webhooks first, then reactive structured AI reports, proactive triage, bounded tools/follow-up, and finally signed inbound triggers. Two-way bots remain deferred.
- Trigger automatic AI on qualifying changes, not every scan. Proposed limits, coalescing, cooldowns, cumulative usage, result reuse, and transactional cost reservations are specified; model IDs and spending allowances remain setup choices informed by evaluations.
- Keep factual alert routing independent of AI failure or triage suppression. AI proposes hypotheses/actions; server policy controls scope, recipients, tool access, and budgets. No production remediation is introduced.
- Add explicit provider-data opt-in and preview, per-service exclusions, generated-content retention, and all new secrets to existing key rotation/restore. Demo external sends are separately opt-in.
- Existing run-stage uniqueness does not support repeated AI steps; use linked AI run/step records. Preserve finding identity and deterministic reports.

Official provider and webhook references are linked in the active specification. Documentation availability does not establish account-specific model access or successful integration; no provider calls, external messages, runtime changes, or new implementation tests were performed for this planning task.

## 2026-10-10 — Phase 2 Slice A implementation

- Commit immutable notification events plus River jobs alongside finding creation/severity escalation/manual resolution and completed investigations. Fan-out locks destination rows, groups for 60 seconds (ten events maximum), and preserves a unique destination/event item even when lifecycle checks exclude it.
- Separate two-worker notification queue; destination session advisory locks serialize actual sends across replicas. Record attempt intent before network dispatch. Reuse frozen bytes/IDs after unknown outcomes, within eight attempts and 24 hours/source expiry. Manual retry cannot reset those budgets. Recheck lifecycle/configuration and cancel a frozen batch if its content is no longer eligible.
- Fix external payload release to bounded factual names/titles/IDs/links without excerpts. Preserve live/demo filters, opt-in demo routing, explicit test confirmation, and an independent no-AI path. Edits invalidate queued deliveries and do not backfill older events.
- Encrypt destination URL and generic signing secret together under workspace/notification/record associated data. Extend atomic rotation and actual backup/restore checks now. Deletion erases secret bytes; TTL cleanup erases payload copies before operational metadata ages out at 30 days.
- Official Slack/Discord endpoints, native payloads and acknowledgement rules. Generic HMAC signs timestamp plus exact body bytes; each retry refreshes the signature. Share safe DNS/dial behavior with connectors through a separate network-policy instance, with no redirects/proxy or inherited telemetry exceptions.
- Use existing Settings and React Query for destination controls, synthetic preview/test and paginated attempt history. No additional runtime service or AI dependency. Fixture transport tests are distinct from actual Slack/Discord channel smoke tests.

## 2026-10-10 — Phase 2 Slice B implementation

- Use direct OpenAI Responses and Anthropic Messages structured output with a common validated report contract. Keep the official endpoints fixed, private-network exceptions disabled, OpenAI storage off and all tools absent. Configure model IDs/prices explicitly and require a synthetic capability test; no model/account access is assumed.
- Start with one bounded invocation of existing evidence. The AI run is its invocation, reservation and dispatch-intent record; separate step/conversation records are deferred until multi-step execution. A single investigation role is available now; triage roles belong to Slice C. Preserve deterministic report and notification paths.
- Serialize workspace admission/config edits with a short row lock; never hold a transaction across HTTP. Use a separate two-worker AI queue and provider advisory lock. One call per run, no automatic paid retry, intent persisted before dispatch, unknown outcomes retain reservations across UTC days and process restarts. Stuck River rescue is four minutes so a legitimate 190-second AI worker is not rescued prematurely.
- Bound the serialized request and output; snapshot integer prices/date and reconcile reported usage. Input overhead is estimated rather than provider-certified. Usage above the admission estimate disables capability and withholds the report; invoices are not guaranteed by local budgets. Manual cost reconciliation requires a finished uncertain call older than four minutes and an audited administrator assertion.
- Evidence provenance/retention/source access and provider/service/demo consent are rechecked at admission, dispatch and result publication. Preview digests bind request instructions/schema/model and provider revision. Cache reuse also binds workspace policy revision, and UI selects the actual reused invocation even when a newer attempt failed.
- Reuse the existing full-line redactor for opted-in excerpts and generated plain text. Clear input copies at terminal states; source expiry gates report reads/cleanup, and settled metadata expires after 30 days. Unknown accounting metadata persists until reconciled. Provider deletion erases keys and content; rotation and real database backup/restore cover provider secrets.
- Test cloud protocols through explicit HTTP transport fixtures only. Browser fixtures exist solely in a test binary guarded to the disposable E2E database/schema. Production has no fixture environment switch or configurable provider URL. Local tests establish contract/safety behavior, not live model quality or account access.

## 2026-10-10 — AI review fixes

- Bind asynchronous preview responses to a selection generation and check the selected provider again before sending. Switching providers invalidates pending previews, including when switching back.
- Page AI operational history by descending `(created_at, id)`, with workspace/investigation-scoped cursors. Both Settings and investigation history expose older pages so retained unknown charges remain reconcilable.
- Dispatch the freshly rebuilt, digest-verified compact payload. Persisted JSONB remains a lifecycle gate, but its whitespace normalization must not change the admitted request bytes or bound.
