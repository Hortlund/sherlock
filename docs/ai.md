# OpenAI and Anthropic analysis

Phase 2 Slice B adds an optional structured analysis of an already completed investigation. Both providers use the same report shape: observations, hypotheses with supporting/contradicting evidence, unknowns, and manual checks. The deterministic report remains available. Optional OpenAI/Anthropic proactive triage is also available; see [automation setup](automation.md). Opt-in automatic admission reuses bounded analysis of triggering evidence. OpenAI Decisions, new telemetry tools, report conversations and inbound triggers remain planned.

## Set up a provider

1. Open **Settings → AI providers**. Enable workspace processing and set positive daily and per-run USD budgets. Daily limits reset at midnight UTC; unresolved reservations carry forward.
2. Add OpenAI or Anthropic. Enter an API key, an exact model ID available to your account that supports structured output, standard input/output prices in USD per million tokens, and their effective date. There are no assumed model IDs, prices or unlimited budgets. API keys are encrypted and write-only; a blank key on edit preserves the stored credential.
3. Select the services allowed to send data. **No services selected means no service data may leave.** Demo evidence needs the separate demo checkbox. Counts only is the default. Excerpts require both provider permission and the service's existing excerpt-storage setting.
4. Enable the provider and click **Test openai** or **Test anthropic**. Confirm this bounded, potentially billable request. The test sends synthetic evidence only. Inspect the completed test and refresh AI status; the capability must be **ready**. Unsupported models/schema, invalid keys, refusals or missing usage leave it unavailable for analysis. A successful test checks the response contract, not reasoning quality.
5. Open a completed investigation, select the provider under **Investigate with AI**, and click **Preview AI payload**. Review the exact JSON body, model and estimated reservation. **Send to AI provider** explicitly authorizes that request. This manual-analysis flow never enables background calls. Optional recurring triage requires separate Automation consent and a tested triage model.

Switching providers discards the current preview, including any response still in flight. Preview the selected provider again before sending.

Model/configuration changes require a new synthetic test and invalidate older previews. Older investigations with changed mappings, unavailable sources or expired evidence need a fresh deterministic investigation first. Ordinary scans, factual notifications, and reports work without a provider.

## Data and provider requests

OpenAI uses `POST https://api.openai.com/v1/responses` with strict `text.format` JSON schema, `store:false`, and `max_output_tokens:2048`. Anthropic uses `POST https://api.anthropic.com/v1/messages` with `anthropic-version:2023-06-01`, `output_config.format`, and `max_tokens:2048`. These formats follow the [OpenAI structured-output guide](https://developers.openai.com/api/docs/guides/structured-outputs), [Responses migration guide](https://developers.openai.com/api/docs/guides/migrate-to-responses), and [Anthropic structured-output guide](https://platform.claude.com/docs/en/build-with-claude/structured-outputs).

The payload contains up to 12 existing evidence IDs, service ID/origin, time windows, completeness/coverage, sample counts and numeric observations. It excludes service display names, source URLs, selectors, labels, query strings, credentials and raw upstream responses. Opted-in excerpts are at most two patterns per observation, redacted again using the service's current policy and limited to 512 UTF-8 bytes each. They are omitted if necessary to fit the context estimate. Text may still contain sensitive information: review the preview before release.

Endpoints are fixed, TLS verified, public-address checks apply, and redirects/environment proxies/private telemetry allowlists are not inherited. There is no custom model endpoint, tool execution, web browsing, automatic provider fallback, or model-directed notification. Instructions in excerpts are untrusted. Server enforcement prevents tool or scope escalation; no claim is made that arbitrary model prose is immune to prompt injection or factually correct.

Output must match the common schema and cite only the available evidence for this investigation. Unknown/expired/cross-scope citations or invalid structure withhold generated content. Generated text is redacted before storage, displayed as plain text, and never used as an action. Citations establish scope, not causality. Raw provider responses, hidden reasoning and error bodies are not persisted. Safe request IDs and reported token totals are retained.

OpenAI `store:false` is not a promise of zero provider-side retention. Provider/account terms govern transmitted data; cancellation and local deletion cannot recall it.

## Execution and accounting

At dispatch, the worker rebuilds and verifies the compact payload against the admitted digest, then sends those verified bytes. PostgreSQL JSONB formatting cannot inflate the request measured by admission. Both manual analysis and triage take a shared workspace row guard against mapping/privacy edits before other scope locks and accounting. Recommendation evaluation takes the same guard before its service lock and optional AI admission. Triage also serializes with its service and rule settings. This order prevents service edits, triage and AI accounting writes from deadlocking through workspace foreign-key checks. An edit committed during provider HTTP invalidates the result at publication. No scope lock is held across provider HTTP.

- Separate River AI queue: two workers globally per process, one in-flight request per provider configuration across replicas. One active analysis per service. Notifications and telemetry keep their own queues.
- One paid attempt per run, with a 150-second HTTP timeout and a 180-second deadline **from enqueue**, including queue wait. No transaction spans provider HTTP. River's stuck-job rescue threshold is four minutes, above the AI worker's 190-second timeout.
- Input admission uses serialized request UTF-8 bytes plus a 1,024-token overhead allowance, capped at 12,000. This includes instructions and schema. It is a conservative estimate, **not a certified bound on hidden provider overhead**. Output is capped at 2,048 tokens, including reasoning usage when the provider reports it as output usage. Models that cannot complete within this budget fail the capability test; no hidden automatic increase occurs.
- Prices and date are snapshotted per invocation. Amounts in the API/database are integer micro-USD; configured prices are micro-USD per million tokens. Each input/output cost component rounds upward. No automatic prompt caching is requested; any reported Anthropic cache token components count conservatively as standard input.
- A workspace advisory lock serializes reservations against the daily/per-run allowance. The daily total includes all outstanding or ambiguous reservations, even from prior days. Actual known usage settles the configured estimate. If reported usage exceeds the estimate/cap, account for it, withhold the report and require a new capability test. The budget is admission control, not a guarantee about the provider invoice. Configure provider account spending controls as appropriate.
- Network loss, interrupted dispatch, 408/5xx, missing or malformed usage keep the reservation. HTTP rejection such as 401/429 settles zero. Refused/truncated/invalid reports still account for known usage. There is no automatic paid retry. A worker recovering after persisted dispatch intent marks the outcome unknown instead of calling again.
- For a finished uncertain run older than four minutes, inspect its time/request ID in the provider account, then use **Reconcile verified provider charge** to record the verified USD amount. This is an audited administrator assertion, not automatic billing reconciliation. Costs are attributed to the invocation's original UTC day.
- Successful unexpired reports can be reused without another call; lookup includes evidence/request digest, provider revision, policy revision and prompt/schema content, and revalidates current evidence. Completed automatic analysis is also eligible for manual reuse under these same checks, even after Automation is switched Off; its original purpose and accounting remain intact. **Request fresh analysis** explicitly bypasses a saved success but never an active equivalent run or budget/privacy checks. Active automatic tasks remain governed by automation policy and block a second manual task until they finish.

Cancellation stops queued work and requests cancellation of active HTTP. Disablement/provider edits cancel outstanding runs; disabling workspace AI or editing/deleting a provider also removes its local generated content. A final check prevents publishing stale or revoked results. A request already transmitted may still be charged.

## Retention and credentials

Input copies are removed at every terminal outcome. Generated reports expire no later than their earliest source evidence; API reads withhold expired text even before the minute cleanup. Synthetic test reports expire after 24 hours. **Delete AI content** removes a local report immediately. Settled operational records age out after 30 days; uncertain metadata/reservations remain until reconciled so cleanup cannot silently release money. Use **Load older AI runs** in Settings or investigation history to reach earlier records, including unresolved synthetic tests. History uses stable cursor pagination in pages of 50.

Provider keys use record/workspace-bound encryption, participate in the existing atomic key rotation and backup/restore, and are erased when a provider is deleted. Backups retain whatever existed when taken; protect and age them separately. Never paste keys into logs, source files or this documentation.

## Verification and live deployment check

Local unit/contract, PostgreSQL and Playwright tests use a fixture transport. Browser fixtures are compiled only into a test executable and require an isolated `e2e_` schema/database; the production binary has no fake-provider switch. Fixture reports/costs are visibly labeled. No real provider call was made during implementation.

For each real account/model, configure it using the steps above, run the synthetic capability test, preview a small allowed investigation, confirm the analysis, inspect citations and compare recorded token usage with the provider account. Then cancel a deliberately running request if needed to verify account behavior. Keep the first per-run/daily budgets small. Record account-specific failures before enabling it more broadly. No usefulness/accuracy claim or unattended AI rollout is established by local fixtures.


## Separate triage role

An optional triage model and its own input/output prices share the encrypted provider key and effective price date with the analysis role. The roles have independent capability results; editing the provider resets both, cancels active work and clears earlier generated content. Preview/test triage in Settings before opting in under Automation. Triage uses the same structured-output endpoints with a smaller decision schema, 4,000 estimated input / 1,000 output tokens and a 15-second enqueue-to-completion deadline. It never enables tools or starts investigations. Its `triage` and `triage_test` records share budgets, cancellation, usage reconciliation, key lifecycle and retention with analysis records.

API additions: `POST /api/v1/ai/providers/{id}/triage-preview` returns a synthetic request without sending it; `POST /api/v1/ai/providers/{id}/triage-test` requires `confirm:true` and reserves a potentially paid test. Provider configuration adds `triage_model`, `triage_input_price`, `triage_output_price` and read-only `triage_capability`. Automation selects `triage_provider_id` (empty means local rules only).

Official protocol references checked for this implementation: [OpenAI structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs?api-mode=responses) and [Anthropic structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs). Live model/account availability and output usefulness still require deployment evaluation; fixture success does not establish those.


## Automatic admission

Automation can explicitly opt into recurring investigation analysis with a separately selected, tested investigation provider. It reuses this single-invocation engine, budget ledger, privacy/excerpt controls, cancellation and uncertain-outcome accounting. Runs have purpose `automatic` and link to a deterministic report built from the qualifying scan evidence. They make no telemetry or tool calls. See [automatic setup and policy](automation.md#opt-in-automatic-investigations). Existing manual or triage consent does not enable automatic investigations.
