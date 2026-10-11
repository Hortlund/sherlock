# Operating Sherlock

## Connection and query bounds

The server supports up to 100 source configurations and 100 mapped services. Source URLs may use HTTP/HTTPS, optional path prefixes, bearer/basic authentication, Loki tenant headers, and a private PEM CA. Source type is immutable; create another source to change it. Credentials are encrypted with AES-256-GCM and bound to workspace/record IDs. Editing a source invalidates the mapping versions of its associated services. Disabling stops new requests; deleting also erases its stored encrypted credential. Previously persisted evidence remains readable until expiry. Backups can still contain old encrypted secrets; revoke the corresponding upstream token when removing access.

Private destinations need an explicit deployment allowlist. Set `SHERLOCK_ALLOWED_HOSTS=demo,prometheus.internal,loki.internal` and/or narrow `SHERLOCK_ALLOWED_CIDRS` in `.env`, then recreate the app. Host exceptions allow private addresses; loopback requires an explicit CIDR. Metadata/link-local, multicast and unspecified addresses remain blocked. Every connection resolves and validates DNS, then dials the validated address. Redirects and inherited HTTP proxies are disabled. HTTPS always verifies the certificate and hostname. No arbitrary telemetry queries are accepted from the browser.

Bounds: 12 seconds/request, 2 MiB response, 2 requests/source and 16 total; 4 job workers (source slots wait within the caller deadline rather than dropping busy requests); 2,000 log lines/window; 100 fingerprints/severity; 512-byte redacted excerpts; 12 outbound attempts and 90 seconds per run. Private pattern redaction is configurable per mapping. Redaction v2 matches built-in and custom patterns against the full original line, merges overlapping matches, then limits the sanitized UTF-8 excerpt to 512 bytes. If a pattern exceeds 4,096 matches, the entire line is withheld from fingerprint groups and the observation is marked incomplete; log detectors abstain for either incomplete comparison window. Built-in redaction preserves the original field-name and email markers so existing v1 fingerprints retain their lifecycle identity. Custom overlaps hide the whole matched region. Previously stored evidence is not rewritten. Disabling excerpts stores only opaque SHA-256 fingerprints and counts, not normalized log text. Redaction is best effort; don't select highly sensitive streams when their retention is inappropriate.

Metrics and logs have independent `prometheus_labels` and `loki_labels` exact-match selectors. Legacy `labels` still supplies either missing selector, including in queued snapshots; the editor converts these to explicit selectors without changing finding identity.

Counter mappings use `increase` with error labels on the same request counter. Queries require a fresh sample within two minutes of the window end and at least 80% of one sample/minute across the window; slower scrapes abstain. These guards do not prove complete coverage between samples. Logs require nonempty complete windows with at least ten entries spanning 80% of each window for absence/burst comparisons. Capped samples are lower bounds. Missing errors series are insufficient signal rather than automatically zero errors.

The initial rules are: new fingerprint ≥3 occurrences; warning count ≥10 and ≥3× duration-normalized comparison rate; request proportion increases by ≥2 percentage points and ≥2× with ≥100 requests/window. All emit only with coverage guards. Warning findings are `notice`; exception/error-proportion findings are `warning`. Neither severity claims an outage. Settings exposes thresholds, detector enablement, cadence/windows, retention and recurrence cooldown. Settings are snapshotted per job; later changes do not rewrite a finding's thresholds.

## Jobs and progress

River's MPL-2.0 open-source core provides durable job execution. Stored schedule cursors transactionally enqueue due scans; the one-minute pump coalesces missed intervals. Monitoring disablement and mapping/source revisions are checked before each request. Manual scans work without enabling periodic monitoring. Active requests have deadlines; investigation cancellation also notifies River to cancel the worker context. Four workers prevent a failing source from monopolizing execution.

Progress uses authenticated polling as the SSE reconnect fallback. `GET /api/v1/investigations/{id}/events?after=<event-id>` returns persisted events in ascending ID order. The UI polls the run/event resources once per second while active, fetches terminal progress once before stopping, and reloads the same report after navigation/restart. No streaming infrastructure is required. A recovered attempt retains the original deadline and request count. If a crash outlasts its 90-second deadline, the run ends explicitly as failed and the user can start another. Investigation launch retries reuse an active run only when its service, mapping revision, incident window and settings match. Conflicting requests return HTTP 409 `active_run_conflict`; finish or cancel the existing investigation before starting another. Overview scan freshness allows the configured cadence plus five minutes of scheduling/execution grace.

Job results and finding writes are idempotent; missed scans are not backfilled without bounds.

## Evidence retention

Default seven days; configurable 1–30 days for new evidence. Once per minute, up to 1,000 expired documents are erased. Tombstones retain IDs, provenance, window and expiry so citations resolve to HTTP 410 `expired`. Excerpts are unavailable from the API immediately at expiry, even before cleanup. Counts and report summaries remain in finding/report history. Signal mapping edits preserve history and establish separate detector identity. Renames, environment display changes, monitoring toggles, and unchanged saves preserve the revision and finding lifecycle. Enabling/disabling monitoring remains checked at collection and finding-write boundaries. Auth/session cleanup and River job cleanup also run automatically.

## Notification delivery

See [notification operations](notifications.md) for destination setup, transport restrictions, quiet hours, retries, unknown outcomes, signature verification and payload expiry. The separate River notification queue has two workers. Deletion erases the destination URL/signing secret; rotate/revoke upstream credentials separately to invalidate copies in old backups.

## Backup and restore

Run from the repository root:

```sh
scripts/backup.sh /secure/location/sherlock-backup-2026-10-10
```

`pg_dump` provides a consistent database snapshot while the application is running. Store the dump and `installation.env` together in protected backup storage. Never publish either file. Keep the application version/source with the backup. Restore to an **empty, separate installation** using the same application revision and a distinct Compose project/port; stop its app before restoring. Copy `installation.env` to its `.env` with mode 0600, choose unused local ports, and start only Postgres:

```sh
docker compose up -d --wait postgres
docker compose exec -T postgres pg_restore -U sherlock -d sherlock --no-owner --no-acl --exit-on-error < /secure/location/sherlock-backup-2026-10-10/database.dump
docker compose up --build -d --wait app
```

Do not restore over an existing initialized database. Verify `/readyz`, login, service mappings, evidence, and a source test. Restored River jobs remain durable; expired run deadlines fail explicitly. The test suite executes a real pg_dump/pg_restore of an isolated installation, then opens it with the original encryption key and checks the restored session, audit history, encrypted notification URL/signing secret, inbound signing secret/receipt, and provider API key, and pending notification/AI jobs. Restored unexpired notification, inbound admission and undispatched AI jobs may resume processing/sending; isolate outbound connectivity during a restore rehearsal to avoid real messages or paid calls. AI invocations with a persisted dispatch intent are not replayed; their uncertain reservations remain.

## Encryption-key rotation

Back up first. Stop **all** app processes; leave Postgres running. Generate a new base64-encoded 32-byte key into a mode-0600 file using a trusted local secret generator, and retain both old/new keys until verification. Do not pass keys as command-line values.

```sh
docker compose stop app
docker compose run --rm --no-deps --user 0 --entrypoint sherlock-admin -v /absolute/path/new-key.txt:/run/new-key:ro app --new-key-file /run/new-key
```

The command decrypts and validates every live credential before transactionally re-encrypting credentials and the installation sentinel. It prints no key values. After success, replace only `SHERLOCK_ENCRYPTION_KEY` in `.env` with the new file's value and recreate the app. Do not restart with the old key. If interrupted after the transaction commits but before editing `.env`, recover by applying the retained new key. If the transaction fails, keep the old environment. Rotation includes connection credentials, live notification destination URLs/signing secrets, live inbound binding signing secrets, and live OpenAI/Anthropic keys. Test a source and explicitly selected notification receiver/provider after restarting; retain the prior key only as required for decrypting older backups. The integration suite verifies successful rotation, old-key rejection and rollback on invalid old keys.

## Release checks

`make lint test e2e real-sources` covers the local acceptance gates. Real-source tests use isolated Prometheus v3.2.1 and Loki v3.4.2 containers, seeded time series/logs and real query engines, then remove their own test volumes. They do not contact production. CI is configured separately; local success does not claim a remote CI run.

Operational JSON is authenticated at `/api/v1/system`; overview indicates stale/missing scans. `/healthz` and `/readyz` expose only process/database readiness. Logs and audit entries omit source response bodies and credentials. Default UI origin is loopback. A remote deployment requires an HTTPS reverse proxy and exact configured origin, with no direct access to the app's development HTTP port.

## AI provider operations

See [AI setup, budgets, unknown outcomes and retention](ai.md). AI consent defaults off, each service is explicitly allowed, and demo egress is separately opt-in. A synthetic provider test is an explicit potentially billable action. All provider wire/worker/browser checks in the local suite use fixtures; actual API accounts require their own bounded smoke test.

## Inbound monitoring alerts

See [inbound setup and exact signing](inbound.md). Bindings start disabled, use their own request/daily limits and require existing automatic investigation consent. Automatic tools remain a separate opt-in. Receipt history records skipped or revoked work without replaying it later.
