# Proactive recommendations and automatic investigations

Sherlock can recommend a manual investigation when a detector-backed signal changes meaningfully. Local Go rules remain the default. Optional OpenAI or Anthropic triage can add an advisory assessment after a recommendation; investigations remain manual in Recommendations only mode. A separate opt-in mode admits bounded automatic analysis; triage itself sends no notifications. Existing factual notification rules continue independently.

## Enable it

1. Enable **Scheduled monitoring** for the relevant services on **Services**.
2. Open **Settings → Automation** and choose **Recommendations only**.
3. Select the services to include. An empty selection includes none. For the local demo, also enable **Include simulated demo findings**.
4. Keep or adjust the cooldown and growth multiplier, then save.
5. Inspect **Proactive activity** after a new finding or meaningful change. Events group for 60 seconds before evaluation; worker scheduling can add a few seconds. Expand an entry for the rule, decision, reason and next eligible time. Follow the finding link and choose **Investigate** to start a run manually.

Existing findings establish a fresh comparison on their next observation after a settings change. Enabling recommendations does not backfill an existing incident. The stable demo may therefore show no new recommendation until a new occurrence or meaningful change is observed. Manual scans can also contribute finding changes, but recommendations still require scheduled monitoring to be enabled.

Choose **Off** to pause recommendations. Activity may still record skipped meaningful changes, explaining that recommendations are off. Off also prevents new automatic investigation dispatch; previously transmitted requests may still incur charges.

## Rules and bounds

| Rule or limit | Behavior |
| --- | --- |
| New occurrence | Eligible once for each finding occurrence, including a new mapping revision |
| Severity change | Eligible when deterministic severity changes |
| Growth | Current value must exceed the last evaluated observation by both the multiplier and the detector's absolute minimum |
| Error proportion | Fraction of requests; absolute increase uses Monitoring's error-delta threshold |
| New-pattern/warning counts | Sampled lines per minute; absolute increase uses minimum occurrences/warnings divided by the sampled window length |
| Comparison changed | A window or detector threshold change resets the baseline and records “More data needed” |
| Grouping | One pending batch per service and policy/mapping revision; latest observations replace pending item measurements without extending the 60-second window |
| Recovery | A newer complete scan without the same qualifying signal clears its pending recommendation; incomplete or disabled detector coverage cannot establish recovery |
| Cooldown | 30 minutes by default, configurable from 5 to 1440; shared across the service and persisted across restart |
| Growth multiplier | 2× by default, configurable from 1.5× to 10× |
| Queue | One local automation worker per process; service-row locking serializes evaluations across workers/replicas |
| Freshness/retries | 15-second evaluation timeout, at most ten job attempts; both the batch and its scan observation must be no more than 15 minutes old |
| History | Most recent first, 50 entries per page, service filter, retained for 30 days |

These rules describe changes in collected samples, not outage probability or predicted failures. Acknowledged findings can still warrant investigation; snoozed, dismissed and resolved findings are skipped. Missing, incomplete, expired or wrong-mapping evidence produces no recommendation. Policy changes, excluded services/demo data, disabled monitoring, mapping changes and active cooldowns all have explicit skip reasons.

Skipped observations advance comparison checkpoints. Expiration of a cooldown alone does not replay the same change. Checkpoints and service cooldowns survive history cleanup; ordinary unchanged scans produce no new batches. Any policy save increments its revision, invalidates older queued batches, and seeds existing findings on their next observation. Evaluation reads current Monitoring settings: disabled detectors are skipped, and changed windows/thresholds require a fresh comparison. Monitoring saves serialize with evaluation, including the first save. Cadence and retention changes do not invalidate comparisons.

A healthy scan preserves the original activity measurement and records why it no longer qualifies. It does not resolve the finding; resolution remains manual. An older delayed scan cannot undo a newer recovery, while a later qualifying scan can make the pending item eligible again. Skips never start or extend recommendation cooldowns. Restarting an old queued scan does not make its historical observation fresh.

## API and storage

All routes require the workspace session; writes require the normal CSRF header:

- `GET /api/v1/automation/policy` returns defaults or saved settings, including `revision`.
- `POST /api/v1/automation/policy` accepts the full policy and current revision. Stale revisions return 409. `mode` accepts `off`, `triage_only` and `auto_investigate`. Automatic mode requires `automatic_consent: true` and `investigation_provider_id`; the selected investigation model must be tested and workspace AI/budgets/prices enabled. `triage_provider_id` independently opts into optional AI triage. Without it, local rules decide admission.
- `GET /api/v1/automation/activity?service_id=…&cursor=…` returns `items` and `next_cursor`. The optional cursor belongs to the same workspace and service filter. Missing/out-of-scope cursors return an empty page.
- `GET /api/v1/findings?id=…` supports links to findings beyond the first list page. The UI ignores retained list filters/cursors while opening an exact finding, including through browser Back/Forward.

Migration 007 adds policy, checkpoints, cooldowns, batches and items; migration 008 adds the recovery timestamp without modifying the original migration. The finding update, pending item, recovery marker and River job commit atomically. Evaluation and checkpoint/cooldown updates also commit together. Duplicate delivery returns the existing terminal result. Maintenance finalizes expired/exhausted batches and removes old history; records contain measurements and evidence IDs rather than copied log excerpts. Standard PostgreSQL backup/restore includes these tables and jobs; this feature stores no new secrets.

## Optional AI triage

1. In **Settings → AI providers**, configure the provider's optional **Triage model ID**, its input/output prices, and the shared price date. The triage model may differ from the analysis model. Service/demo consent applies to both roles. Workspace AI processing and positive daily/per-run budgets are required.
2. Use **Preview … triage sample** to inspect the exact synthetic request format without sending data. Then explicitly confirm **Test … triage**. Its separate capability must become ready; analysis capability alone does not enable triage. Test calls can incur charges.
3. In **Settings → Automation**, select the provider and check **Allow recurring AI triage for qualifying recommendations**. Save with Recommendations only enabled. Existing policy revision rules prevent backlog replay. Clear the checkbox or select Local rules only to stop new triage; Off pauses the whole recommendation flow.
4. Expand proactive activity to compare the local rule decision with **AI advice**, its reason, model and accounting. The model can advise `investigate`, `observe`, `needs_attention`, or `need_more_data`. These are advisory labels, not automatic actions or incident probabilities. The local recommendation and its cooldown remain unchanged, including when AI advises observe.

Only eligible, coalesced local recommendations can enqueue triage. The payload contains numeric current/comparison signals, window lengths, detector/severity identifiers, timestamps and opaque service/finding/evidence IDs. Counts use the same normalized units as the local rules. It includes no service names, log fingerprints, labels, excerpts, selectors, URLs or user-authored text. At most eight signals and twelve evidence IDs fit the bounded summary; oversized summaries fall back to local rules without a call.

Triage uses the existing isolated two-worker AI queue and per-provider serialization. Each task has a **15-second deadline from enqueue**, a conservative **4,000-token input allowance** (serialized request bytes plus 1,024 overhead), and a **1,000-output-token ceiling**. Queue time consumes that deadline. The existing shared ledger reserves estimated cost against daily/per-run limits and includes unresolved reservations from previous days. No automatic paid retry or silent provider switch occurs. Refusal, invalid schema/citations, timeout, missing capability, concurrent AI work or unavailable budget leaves the local recommendation available with a visible reason.

Dispatch and result publication recheck automation revision, selected service/demo consent, monitoring/mapping, detector settings, finding lifecycle, complete retained evidence and source access, plus provider/workspace AI policy. Newer recovery or changed observations invalidate pending AI assessments. In-flight cancellation is requested when scope changes; transmitted data cannot be recalled. **Cancel AI triage** is available while queued/analyzing. Unknown outcomes retain reservations and can be reconciled in AI usage history.

Successful assessments can be reused only for the same scoped evidence/request, model/provider revision, prompt/schema and automation/AI policy revisions, after current eligibility checks. Ordinary unchanged scans already produce no task. Generated content expires at the earlier of 24 hours or source evidence expiry. Input copies are removed at terminal states. History references the original AI record, so deletion, provider erasure and retention also remove its generated reason from activity. Operational metadata remains subject to existing 30-day retention and unresolved-accounting rules.

Migration 009 adds triage capability and links between AI accounting and activity; no new credentials or service are introduced. Local recommendation publication, budget admission and the River job commit atomically. Evaluation and triage workers take a shared workspace row guard before service/rule locks and the AI accounting advisory lock, preserving the workspace-before-service order used by mapping edits. Shared guards remain compatible with accounting writes' foreign-key checks, and no transaction spans provider HTTP.

## Next part of Slice C

Still planned: optional OpenAI Decisions and live model-usefulness evaluation. Opt-in automatic admission is described below. The implemented adapters use OpenAI Responses and Anthropic Messages structured output. Neither model can start an investigation, alter a finding or suppress a factual notification.


## Opt-in automatic investigations

Automatic investigations default to **Off**; upgrading does not enable them. In **Settings → AI providers**, configure and test an investigation model, select allowed services/demo origin, review excerpt release, and set positive daily and per-run budgets and prices. In **Settings → Automation**, choose **Automatic investigations**, select that provider and the services, and check **Allow recurring automatic investigations and paid AI analysis**. Demo processing requires both provider and automation demo consent. No per-event confirmation is needed after this explicit opt-in.

The existing change gate, 60-second grouping and recommendation cooldown apply. A separate durable admission job waits for optional triage. Valid `investigate` advice proceeds; `observe`, `needs_attention` and `need_more_data` skip automatic analysis without changing local recommendations, findings or factual alerts. Unavailable/refused/invalid triage falls back to local rules under the remaining shared budget. Cancelling or deleting triage stops pending admission even if triage completed just before the request. It also cancels any queued or in-flight automatic analysis derived from that triage, including cached references. Cancelled assessments are excluded from future cache reuse; completed history and incurred usage remain. Unresolved active triage never overlaps automatic analysis. A triage task that remains active beyond its deadline plus 30 seconds skips admission.

Admission rechecks policy revision, recurring consent, service/demo selection, scheduled monitoring, mapping, detector configuration, finding lifecycle, recovery and complete retained evidence no older than 15 minutes. Provider service consent, tested capability and budgets also apply. One active AI task per service is enforced across manual, triage and automatic work. Automatic investigation cooldown is separately persisted, using the same configured 5–1440 minute duration (default 30), and starts only after successful admission. Skipped work is not replayed when a budget or cooldown becomes available.

This increment analyzes the **triggering scan's stored evidence**. It builds a deterministic report from a consistent incident/comparison window, then enqueues the existing Slice B analysis: one provider attempt, 12,000 estimated input tokens, 2,048 output tokens, and a 180-second deadline from enqueue. Permitted excerpts follow existing provider/service settings. No new telemetry collection, tools or follow-up loop is included. Mixed windows or inadequate evidence cause a visible skip. The deterministic report remains available if AI fails.

The report, AI reservation and job, admission link, audit event and cooldown commit atomically. A unique workspace/batch link plus locked terminal admission state prevents duplicate runs under restart or concurrent delivery. Unknown charges remain reserved and paid requests are never automatically replayed. Mapping/privacy, automation, monitoring, source, finding and recovery eligibility are rechecked before dispatch, during the request and before publishing generated content. Revocation withholds generated content while preserving incurred usage accounting.

Activity shows **Automatic admission** separately from local recommendations and AI advice, with launched/skipped/pending status, reason, linked investigation, AI state, model, accounting and cancellation. Choose **Off** or **Recommendations only** to stop future automatic dispatch; any automation settings save invalidates work from older revisions and atomically cancels queued/in-flight triage and automatic AI tasks. Manual analysis and capability tests are unaffected. Policy saves take the shared workspace guard, policy lock and accounting lock in that order; cancellation shares the admission accounting lock, so both race orderings preserve the stop request. A saved consent checkbox alone cannot start work outside Automatic investigations mode.

Migration 010 adds scoped batch/run links, admission state/job metadata and the separate investigation cooldown without rewriting earlier migrations. Migration 011 adds durable cancellation intent without modifying previously applied migrations. Retention finalizes expired/exhausted admission jobs and serializes linked batch/AI history deletes across replicas to avoid circular foreign-key deadlocks. Deterministic report-ready notifications use existing administrator routing; model output never selects recipients or suppresses factual alerts. No new credentials are stored.

Live provider/notification smoke tests and versioned usefulness, factual-error, missed-case, cost and latency measurements remain rollout gates before enabling unattended AI in a deployment. Fixture tests validate contracts and control flow, not live model quality.
