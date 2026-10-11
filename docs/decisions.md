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


## 2026-10-10 — Workspace information architecture

- Group workspace reading flows separately from configuration. Split Settings by URL hash and investigation detail by a `view` query parameter, keeping existing AI/notification deep links valid.
- Reuse shared React/CSS primitives and native modal dialogs. Drawers keep creation/editing close to their list and provide explicit keyboard focus cycling, Escape and focus restoration.
- Keep report observations, limitations and manual checks together while separating optional AI controls and execution activity. Expand evidence next to its citation. All production content still comes from the existing API.
- Refetch findings and overview counts when their views mount. The new overview shares the findings cache; its earlier empty response must not hide results from a scan just completed elsewhere.
- Preserve the Go/Postgres/River architecture, provider consent, notification routing and deterministic workflows. This is a usability revision of delivered Slices A/B, not delivery of planned proactive features.

## 2026-10-11 — Recommendation-first proactive rules

- Follow the user's explicit choice to deliver local recommendations before automatic runs. Only Off/Recommendations modes are implemented; this pipeline has no model adapter, investigation enqueue or outbound delivery action.
- Persist material-change measurements and pending service batches in the finding transaction. One separate River worker groups for 60 seconds; a service lock serializes evaluation across replicas, and policy/mapping versions invalidate stale work. Update each pending item to the latest observation without extending the window.
- Compare error proportions directly and sampled log counts per minute. Require both relative and absolute growth; include thresholds/windows in a comparison key. A changed comparison requires another observation instead of claiming numerical growth.
- Advance checkpoints on skipped evaluations too. Policy edits seed existing findings on their next observation, avoiding backlog on re-enable. Persist service cooldowns independently of 30-day activity retention. Finalize expired/exhausted work through the same evaluation/lock path, so cleanup also preserves deduplication.
- Keep activity workspace-scoped, service-filtered and cursor-paged with a timestamp/ID tie-breaker. Add an optional finding ID filter so an older history entry can still open its finding beyond the first list page. Retain the existing Settings layout and manual investigation flow.

## 2026-10-11 — Proactive review fixes

- Pass detector coverage through finding publication. Only a newer complete scan can clear a pending signal absent from its candidates. Store that timestamp separately in forward migration 008, preserve the original activity measurement, and advance the checkpoint timestamp when skipped. Delayed candidates cannot erase newer recovery; later qualifying candidates can restore eligibility.
- Bound actual observation age as well as batch age to 15 minutes. A historical scan executed after restart may update factual findings/events, but cannot produce a fresh proactive recommendation or consume a cooldown.
- Recheck current Monitoring enablement/windows/thresholds inside evaluation. A shared/exclusive transaction advisory lock serializes recommendations with settings saves even before the first settings row exists. Cadence/retention changes remain comparable.
- Derive empty effective filters/cursors for exact finding links. Preserve the user's list state for returning to the list without letting that state hide the linked finding on browser Back/Forward.

## 2026-10-11 — Advisory AI triage

- Keep the local recommendation and its cooldown immutable. An optional AI assessment is shown separately; even `observe` cannot suppress a finding, required alert or the user's manual investigation choice. Automatic investigations and the optional OpenAI Decisions endpoint remain outside this increment.
- Reuse the existing provider transport, encrypted keys, isolated AI queue, accounting ledger and cancellation/retention controls. Select a separately priced/tested triage model under each provider. Recurring calls additionally require explicit Automation provider selection/consent; previous manual-analysis consent never activates triage.
- Use OpenAI Responses and Anthropic Messages strict structured output with a four-value decision, short reason and scoped citations. Transmit a numeric synopsis without names, fingerprints or log excerpts. Enforce 4,000 estimated input tokens, 1,000 output tokens and a 15-second deadline from enqueue. Refusal, unknown outcome, validation failure, active service work or budget denial leaves local rules available; there is no paid replay or silent vendor fallback.
- Admit the optional task and local recommendation in one transaction. Serialize all AI accounting with a workspace advisory lock; take service/automation/settings/finding scope locks before it for triage. Use service NO KEY UPDATE locks so manual AI foreign-key checks remain compatible. No transaction spans provider HTTP.
- Revalidate scope/evidence/settings at dispatch and publication and request in-flight cancellation on revocation. Store references to generated results, not copied model prose in activity, so deletion and provider erasure apply consistently. Cache only successful, retained, versioned scoped requests; unknown reservations survive UTC rollover and cleanup.
- Fixture tests cover protocol/schema, consent, limits, cancellation and accounting. They do not establish live model usefulness or account availability; these require explicit deployment tests and no production provider was enabled during development.

## 2026-10-11 — AI triage review fixes

- Guard manual AI dispatch validation and result publication with a shared workspace row lock before accounting. Mapping/source edits take that row exclusively; the shared guard remains compatible with admission's foreign-key checks. Avoid taking a manual service lock before this guard, since editors hold the workspace row before waiting for a service. Triage keeps its existing service/rule scope locks. Release all transaction locks before HTTP.
- Keep Automation's selected provider in controlled form state. Delayed provider options may replace the unavailable placeholder without resetting consent or unsaved edits; intentional selection of Local rules only still disables recurring triage.

## 2026-10-11 — Triage lock ordering

- Extend the shared workspace scope guard to recommendation evaluation and triage dispatch/publication, before service/rule locks and AI accounting. This prevents the three-way cycle where triage holds a service while waiting for accounting, an editor holds the workspace while waiting for that service, and an accounting audit insert waits for the workspace. Keep the guard compatible with FK checks and release all transaction locks before provider HTTP.
- Exercise actual evaluation/worker and service-edit paths with a held accounting transaction. Observe database lock waits before attempting the accounting audit insert; verify admission, dispatch and publication complete without losing the generated report or settled usage. The regression fails with PostgreSQL `40P01` in all three phases before the fix.


## 2026-10-11 — Opt-in automatic investigation admission

- Add `auto_investigate`, a separate tested investigation provider and explicit recurring consent. Off remains the default; Recommendations only preserves manual investigations. Do not enable the user's installation during implementation.
- Keep admission separate from local evaluation and triage in a durable River automation job. Wait for triage when configured; accept investigate, skip other valid advice, fall back to local rules on unavailable triage, and respect explicit cancellation. Notifications remain independently routed.
- Reuse Slice B's one-call analysis of existing evidence. Build a deterministic report from the triggering scan's validated, consistent evidence window; do not recollect telemetry or implement Slice D tools. Reject inconsistent or inadequate snapshots.
- Atomically persist the report, cost reservation, AI job, admission decision, audit event, report-ready event and separate automatic cooldown. Use the existing shared workspace/service/policy/settings/finding lock order before accounting and a unique scoped batch-to-AI link. Skips neither consume the automatic cooldown nor leave provisional reports.
- Recheck automation eligibility at AI dispatch/publication and during HTTP; mapping/privacy/source edits, lifecycle/recovery and policy revisions can revoke work. Preserve the existing deterministic report and incurred accounting if generated content is withheld. Unknown paid outcomes are not replayed.
- Surface the applied action and reason separately from local recommendation/model advice. Defaults remain conservative; live provider usefulness and cost evaluation remain deployment gates.


## 2026-10-11 — Automatic admission review fixes

- Preserve explicit cancel/delete intent independently of terminal AI state. Completion can win the state transition without overriding a later stop request. Admission checks that marker, triage cache reuse excludes it, and the same accounting transaction cancels active automatic children through all referring batches. Read batch links without acquiring batch locks after accounting; retain completed history and actual/unknown charges.
- Automation policy saves take workspace scope, policy and accounting locks, then cancel active triage/automatic work in the same commit as the new revision. The worker's final persisted-state check now observes an Off save that commits after dispatch intent. Existing manual analysis/capability tests remain independent; no transaction spans provider HTTP.
- Serialize bounded batch/AI history deletes with a transaction advisory lock before either table is locked. Their circular `ON DELETE SET NULL` relationships otherwise deadlock when separate replicas delete opposite sides concurrently. Content expiry and unknown-reservation retention rules are unchanged.
- Use forward migration 011 for the cancellation marker. Opt-in requirements and Off defaults remain unchanged. Reproduce completion/cancellation races and the retention deadlock, and exercise admission/revocation in both database-observed lock orders before recording verification in progress.


## 2026-10-11 — Automatic-to-manual result reuse

- Let manual analysis reuse completed automatic results with matching workspace, provider and AI policy revisions, evidence/request digest, retained report and expiry. Both use the same analysis report contract. Keep the original automatic purpose and usage record; reuse creates no reservation, job or provider call.
- Keep cross-purpose reuse limited to completed results. Active automatic tasks retain their automation cancellation semantics and continue to exclude a second task for the service. Explicit fresh requests bypass completed results and create ordinary manual analysis subject to existing consent and budget checks.
- Reusing a result or requesting fresh manual analysis never enables automation. Off and recurring-consent requirements are unchanged. Cover both providers, Off after completion, fresh requests and invalidated results through the real API and fixture workers.

## 2026-10-11 — Slice D bounded tools and follow-up

- Preserve the single-invocation path and its consent/cache contract. Explicit manual `tools` or scoped follow-up enters the step engine; automatic tools require a separate default-false `tools_enabled` policy field in addition to automatic mode and recurring consent. No installation is enabled during implementation.
- Use a strict provider-independent action/report envelope over the existing OpenAI Responses and Anthropic Messages structured-output APIs. The Go dispatcher accepts only evidence retrieval, mapped Prometheus counts and mapped Loki patterns, with no model-supplied arguments. Tools cannot widen the original service or incident/comparison windows or send notifications.
- Store ordered `ai_steps` under the existing workspace-scoped `ai_runs` ledger through forward migration 012. Preserve active run states and the service uniqueness constraint; collecting/reporting progress is represented by step kind and accepted final report. Keep the deterministic run and its unique completion events intact.
- Persist per-step intent before external IO and commit accepted output with the next River job. No paid attempt is replayed after uncertain intent. Reserve conservative cumulative cost/token ceilings and tool/telemetry allowances before steps. Use separate AI/tool queues, the existing provider semaphore, scope-before-accounting locks and run-before-step row locks. The 180-second enqueue deadline remains deliberately stricter than the original proposal.
- Follow-up creates a linked AI run over the original deterministic scope, with at most three recent question/answer summaries, bounded sanitized questions and at most 24-hour/source expiry. Tools remain off unless explicitly requested. New service/window scope requires a new investigation. Completed tool results support manual cache reuse only while all original/additional evidence remains valid.
- Add generated-content cleanup to existing policy/cancel/expiry paths; deleting an ancestor purges dependent transcript content. Keep charged/uncertain usage and progress metadata. Versioned fixture evaluations make no live quality claim; live validation remains deferred at the user's request.


## 2026-10-11 — Slice E signed inbound investigation admission

- Use a dedicated `inbound-v1` endpoint with HMAC-SHA256(timestamp + dot + exact body), ±5-minute timestamps, strict bounded JSON and per-binding external-ID/digest deduplication. Only this signature-authenticated endpoint bypasses browser session/Origin/CSRF; configuration/history remain administrator-only. Native Alertmanager and outbound Sherlock envelopes are rejected.
- A disabled-by-default binding fixes workspace/service/origin and its encrypted secret, request rate and daily investigation cap. Incoming text grants no model/tool/query/recipient authority. Global automatic mode, recurring/service/monitoring/demo consent, tested provider and positive AI budgets remain required; tools retain separate consent.
- Snapshot binding, mapping, automation, AI-policy and provider revisions at receipt arrival. Absent consent records a terminal skip, never a replayable backlog. Scope checks surround telemetry/provider boundaries and publication. Binding edits cancel pending work; a collection watcher aborts in-flight HTTP promptly on revocation. Persist actual/unknown paid usage even when output is withheld.
- Commit receipt and admission job together; commit deterministic report and AI continuation together. A separate single-worker inbound queue uses the existing bounded deterministic collector followed by the existing deep engine (one structured call without tool consent). Explicit signed investigation requests do not add the detector recommendation/advisory-triage stage. Never adopt another manual run for inbound cancellation ownership.
- Serialize workspace/service/automation/binding/detector scope before shared AI accounting and receipt/run locks; service NO KEY UPDATE locks remain compatible with existing admission foreign-key checks. No transaction spans HTTP. Binding daily capacity and the shared automatic service cooldown are consumed at collection enqueue, even if later evidence/AI budget checks fail.
- Bound admission to 15 minutes, collection to existing 90 seconds/12 attempts, and AI to existing step/time/token/cost limits. Keep raw bodies out of storage; retain a digest and 30-day receipt metadata, with sanitized history-only titles expiring after 24 hours. The model receives collected evidence, never the external title. Include inbound secrets in record-bound encryption, key rotation, deletion and actual dump/restore regression coverage.
- Local fixture and real-source checks establish protocol, durability, scope and boundedness. Live sender/provider usefulness and account behavior remain deferred deployment gates; no consent defaults were enabled.


## 2026-10-11 — Slice E monitoring revocation review

- Monitoring toggles intentionally preserve service mapping revisions and finding identity. Therefore, disabling monitoring must durably cancel pending inbound receipts in the same service-save transaction; re-enabling monitoring cannot restore old permission. Preserve completed investigations/AI results and their accounting.
- Keep that cancellation on receipt rows only while the editor owns the exclusive workspace scope lock. Do not acquire AI accounting or child run locks in this transaction: existing provider configuration/audit writes may already own accounting while waiting for the workspace foreign-key guard. Workers stop through receipt state and their existing finalization path.
- Recheck receipt state/deadline in the final telemetry request-budget update, provider preflight query and automatic-tool request-budget update. Provider preflight also checks current service monitoring/mapping revision. This closes the gap between earlier validation and HTTP dispatch without holding a transaction across the network.
- Reject NUL in external titles as a schema/client error before persistence; PostgreSQL text cannot store it, and a malformed external value must not surface as a retryable server error.

## 2026-10-11 — Inbound collection AI-consent dispatch boundary

- Before initial telemetry collection there is no AI run for provider/policy editors to cancel. The final collection request-budget update therefore rechecks the receipt's AI-policy and provider revisions, current AI enablement/provider availability, and the automation revision selecting that provider. A committed disable/re-enable cannot restore the receipt's original consent.
- Keep these checks in the dispatch statement's database snapshot, without adding scope/accounting locks or holding a transaction across HTTP. Manual collections without an inbound receipt retain their existing behavior. Denied requests consume no telemetry attempt; existing finalization marks the revoked receipt cancelled.
