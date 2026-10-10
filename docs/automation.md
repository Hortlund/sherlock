# Proactive recommendations

Sherlock can recommend a manual investigation when a detector-backed signal changes meaningfully. Local Go rules remain the default. Optional OpenAI or Anthropic triage can add an advisory assessment after a recommendation; investigations always start manually, and triage sends no notifications. Existing factual notification rules continue independently.

## Enable it

1. Enable **Scheduled monitoring** for the relevant services on **Services**.
2. Open **Settings → Automation** and choose **Recommendations only**.
3. Select the services to include. An empty selection includes none. For the local demo, also enable **Include simulated demo findings**.
4. Keep or adjust the cooldown and growth multiplier, then save.
5. Inspect **Proactive activity** after a new finding or meaningful change. Events group for 60 seconds before evaluation; worker scheduling can add a few seconds. Expand an entry for the rule, decision, reason and next eligible time. Follow the finding link and choose **Investigate** to start a run manually.

Existing findings establish a fresh comparison on their next observation after a settings change. Enabling recommendations does not backfill an existing incident. The stable demo may therefore show no new recommendation until a new occurrence or meaningful change is observed. Manual scans can also contribute finding changes, but recommendations still require scheduled monitoring to be enabled.

Choose **Off** to pause recommendations. Activity may still record skipped meaningful changes, explaining that recommendations are off. There is no automatic-investigation mode in this release.

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
- `POST /api/v1/automation/policy` accepts the full policy and current revision. Stale revisions return 409. `mode` accepts only `off` and `triage_only`; the latter means local recommendations with optional advisory AI when `triage_provider_id` is set. An empty provider ID keeps processing local. Investigations remain manual in both modes.
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

Still planned: optional OpenAI Decisions, live model-usefulness evaluation, and explicitly enabled automatic investigations with admission controls. The implemented adapters use OpenAI Responses and Anthropic Messages structured output. Neither model can start an investigation, alter a finding or suppress a factual notification.
