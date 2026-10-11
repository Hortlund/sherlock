# Signed inbound alerts

Slice E lets an external monitoring system request a bounded investigation of an existing Sherlock service. Configure it under **Settings → Inbound alerts**. This is separate from [outbound notifications](notifications.md); it is a generic `inbound-v1` protocol, not native Alertmanager ingestion or a Slack/Discord bot.

## Opt-in and scope

Create a binding with a name, one existing service and its exact live/demo origin, a dedicated random signing secret (32–256 UTF-8 bytes), a request limit and a daily investigation limit. Bindings start disabled. The secret is encrypted, never returned by the API, and participates in installation key rotation and backup/restore. Leave the secret blank when editing to preserve it. Service and origin are immutable; use another binding to change them.

Enabling a binding alone does not enable investigations. Dispatch also requires:

- **Settings → Automation → Automatic investigations**, explicit recurring consent, that service selected and monitored, and demo permission when applicable.
- An enabled, successfully tested investigation provider, workspace AI processing enabled, positive per-run/daily budgets and prices, and provider consent for that service/origin.
- Remaining binding daily capacity, no active investigation/AI task for the service, and an expired shared automatic-investigation cooldown.

Automatic investigations remain Off by default. Automatic read-only tools require their own default-false consent in Automation. Creating or editing a binding changes neither setting. The external payload cannot select a workspace, source, query, model, tool or notification recipient.

Alerts received while consent is absent are recorded as skipped and never replay when settings change. A receipt snapshots binding, service mapping, automation policy, AI policy and provider revisions. Later changes invalidate pending work even if a setting is disabled and re-enabled before a worker wakes. Disabling/deleting a binding cancels pending work. Turning service monitoring off durably cancels pending receipts, even if monitoring is re-enabled before a worker wakes; completed reports and their accounting remain intact. Final collection, provider and automatic-tool dispatch checks enforce receipt cancellation. The final collection check also revalidates AI-policy/provider revisions and availability before consuming an HTTP attempt, since no AI run exists yet at this stage. An already dispatched request may have an ambiguous outcome; incurred/uncertain AI usage remains accounted for.

## Exact request and signing example

Send `POST` to the binding's displayed `/api/v1/inbound/alerts/{binding-id}` endpoint with `Content-Type: application/json`. The body is limited to 16 KiB. Only these fields are accepted:

```json
{"version":"inbound-v1","event_id":"monitor-checkout-001","start":"2026-10-11T12:00:00Z","end":"2026-10-11T12:15:00Z","title":"Elevated checkout errors"}
```

Use current incident dates when sending. `event_id` is 1–128 ASCII letters, digits, dots, underscores, colons or hyphens, starting with a letter/digit. `title` is optional and limited to 512 UTF-8 bytes without NUL characters. Start/end are RFC 3339 timestamps. The incident must last 5–60 minutes, end within the last seven days, and not end in the future. Incident plus the configured baseline must total at most two hours. Unsupported windows return 422; Sherlock never silently investigates a different window.

The signature is lowercase hexadecimal HMAC-SHA256 over **the ASCII Unix timestamp, a dot, then the exact UTF-8 request body**. Use the signing secret's UTF-8 bytes directly, without hex/base64 decoding. Headers:

```text
X-Sherlock-Timestamp: <canonical decimal Unix seconds>
X-Sherlock-Signature: v1=<64 lowercase hexadecimal characters>
```

Timestamps must be within five minutes of server time in either direction. This endpoint uses signature authentication and needs no browser session, Origin or CSRF token. All configuration/history APIs retain session and mutation protections.

This Python standard-library example first creates a body file; run the second snippet to send or retry the exact same bytes. Set `SHERLOCK_INBOUND_URL` to the displayed endpoint and `SHERLOCK_INBOUND_SECRET` to your configured secret in the sender's environment. Do not include those values in source control.

```python
# Prepare once per incident. Keep this file unchanged for retries.
import datetime as dt
import json
import uuid
from pathlib import Path

end = dt.datetime.now(dt.timezone.utc).replace(microsecond=0) - dt.timedelta(seconds=5)
alert = {
    "version": "inbound-v1",
    "event_id": "monitor-" + uuid.uuid4().hex,
    "start": (end - dt.timedelta(minutes=15)).isoformat(),
    "end": end.isoformat(),
    "title": "Elevated checkout errors",
}
Path("alert.json").write_bytes(json.dumps(alert, separators=(",", ":")).encode("utf-8"))
```

```python
# Sign afresh on each retry, but retain the event ID and exact body bytes.
import hashlib
import hmac
import os
import time
import urllib.request
from pathlib import Path

body = Path("alert.json").read_bytes()
timestamp = str(int(time.time()))
signature = hmac.new(
    os.environ["SHERLOCK_INBOUND_SECRET"].encode("utf-8"),
    timestamp.encode("ascii") + b"." + body,
    hashlib.sha256,
).hexdigest()
request = urllib.request.Request(
    os.environ["SHERLOCK_INBOUND_URL"], data=body, method="POST",
    headers={
        "Content-Type": "application/json",
        "X-Sherlock-Timestamp": timestamp,
        "X-Sherlock-Signature": "v1=" + signature,
    },
)
with urllib.request.urlopen(request, timeout=15) as response:
    print(response.status, response.read().decode("utf-8"))
```

## Responses, retries and limits

| Response | Meaning |
| --- | --- |
| 202 `{ "id": "...", "reused": false }` | Verified receipt committed; processing or skip outcome is visible in receipt history. It does not promise AI dispatch. |
| 202 with `reused: true` | The same event ID and exact bytes already have a receipt. No extra job or investigation is created. |
| 400 | Invalid schema, unsupported fields or an outbound/native Alertmanager envelope. |
| 401 | Missing/invalid/stale signature, unavailable/disabled binding, or configuration changed during authentication. |
| 409 | Event ID was previously used with different exact bytes, including whitespace changes. |
| 413 / 415 / 422 | Body too large / wrong content type / unsupported incident window. |
| 429 | Binding request limit exceeded; retry after the supplied 60-second delay. |
| 500 | Transaction failed; retry identical bytes with a fresh timestamp/signature. |

Concurrent identical requests converge on one receipt. Receipt and initial River job commit together. The fixed-window per-binding rate limit is 1–60 requests/minute (UI default 10); accepted receipts, identical retries and conflicting IDs consume it. Invalid signatures/schema/window submissions create no receipt. Daily capacity is 1–100 started investigations per UTC day (UI default 10). A started collection consumes its daily slot and the service's existing automatic cooldown even if collection or later AI admission fails. AI cost limits are shared with all other analysis, including uncertain reservations.

A separate single-worker admission queue starts the existing deterministic investigation engine for the exact incident and configured comparison window. Collection has the existing persisted 90-second deadline and 12-HTTP-attempt cap. It collects and validates Sherlock telemetry before admitting AI; inadequate evidence cannot start paid analysis. It never adopts an unrelated manual investigation. This explicit inbound request does not run the optional recommendation/advisory-triage stage used by detector events.

A completed deterministic report and its AI continuation job commit atomically. The existing bounded exploration engine then performs one structured analysis call when automatic tools are disabled, or the approved tool loop when separately enabled. Its existing 180-second deadline, cumulative budgets, consent checks, cancellation and no-paid-replay handling apply; see [AI operations](ai.md). The inbound admission horizon is 15 minutes from receipt. Restart resumes durable work within the original limits; expired/exhausted work does not become a backlog.

## History, privacy and operations

Receipt history shows accepted/skipped/failed work, reasons and linked deterministic investigation/AI status. External titles are sanitized untrusted context for history only; they are never sent to the model or treated as telemetry. Sherlock records external origin and rejects its own outbound envelope. Existing notification rules determine any report notifications; a sender cannot choose their destination.

Raw inbound bodies are not stored. A SHA-256 digest supports exact-body deduplication. Sanitized titles expire after 24 hours (hidden immediately, then purged); receipt metadata and event-ID deduplication are retained for 30 days. Active linked work is excluded from receipt pruning. Investigation/evidence/AI content retains its own existing retention rules. Deleting a binding erases its stored secret and receipt titles while retaining bounded receipt history. Old backups can contain prior encrypted secrets; replace/revoke a signing secret at the sender and binding to invalidate them.

Live upstream sender/provider validation remains a deployment check. The local suite uses synthetic source/provider responses, signed HTTP requests, real PostgreSQL/River, browser interactions, key rotation and dump/restore. It sends no paid provider requests or real external notifications.
