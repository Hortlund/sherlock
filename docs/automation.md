# Proactive recommendations

Sherlock can recommend a manual investigation when a detector-backed signal changes meaningfully. This first part of Slice C uses local Go rules: it makes no AI calls, starts no investigations, and sends no additional notifications. Existing factual notification rules continue independently.

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
- `POST /api/v1/automation/policy` accepts the full policy and current revision. Stale revisions return 409. `mode` accepts only `off` and `triage_only`; the latter means local recommendations, with no model triage yet.
- `GET /api/v1/automation/activity?service_id=…&cursor=…` returns `items` and `next_cursor`. The optional cursor belongs to the same workspace and service filter. Missing/out-of-scope cursors return an empty page.
- `GET /api/v1/findings?id=…` supports links to findings beyond the first list page. The UI ignores retained list filters/cursors while opening an exact finding, including through browser Back/Forward.

Migration 007 adds policy, checkpoints, cooldowns, batches and items; migration 008 adds the recovery timestamp without modifying the original migration. The finding update, pending item, recovery marker and River job commit atomically. Evaluation and checkpoint/cooldown updates also commit together. Duplicate delivery returns the existing terminal result. Maintenance finalizes expired/exhausted batches and removes old history; records contain measurements and evidence IDs rather than copied log excerpts. Standard PostgreSQL backup/restore includes these tables and jobs; this feature stores no new secrets.

## Next part of Slice C

Still planned: optional OpenAI/Anthropic structured triage and OpenAI Decisions, model-output evaluation and caching, and explicitly enabled automatic investigations with privacy, budget and concurrency admission. Existing manually requested AI analysis continues to use its own provider consent and limits.
