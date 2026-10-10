# Notifications

Go + PostgreSQL/River deliver factual alerts independently of AI. Configure them in **Settings → Notifications**. Slack incoming webhooks, Discord webhooks, and signed generic JSON receivers are supported. This is outbound delivery; chat commands, replies, AI enrichment, and inbound alert triggers remain later Phase 2 slices.

## Connect a destination

1. Create an incoming webhook in your chosen Slack channel or Discord channel, or prepare a generic HTTPS receiver.
2. Add a destination in Settings. Paste the full webhook URL. Generic receivers also require a separate random signing secret of 32–256 UTF-8 bytes. Store the same secret securely at the receiver.
3. Select events, severities, services and origins. Defaults select new findings/severity increases, both severities, all services, and **live data only**. Explicitly select **Allow simulated demo alerts** to try a demo scan. Completed investigations use notice severity. Manual resolution is not evidence of recovery.
4. Leave grouping enabled for a 60-second window (up to ten events per service/destination/batch), or select immediate delivery. Optional quiet hours use the configured IANA timezone, including DST transitions.
5. Enable and save. **Preview** shows a synthetic platform payload without sending. **Send test** asks for confirmation and posts one labeled synthetic message; it bypasses quiet hours and origin/event filters but retains transport limits. At most three tests per destination per minute.
6. Inspect delivery history. Configure before scanning; old findings are not sent automatically. For an existing demo finding, a subscribed manual resolution or completed investigation provides a new event.

Secrets are encrypted and never returned to the browser. When editing, leave secret fields blank to keep them. Every edit advances the destination revision, cancels queued deliveries, and applies only to future events. Delete erases the encrypted secret and cancels queued messages. Already dispatched requests cannot be recalled; old backups may still contain encrypted secrets, so also revoke the platform URL/key when necessary.

Messages contain bounded service names, factual finding titles, IDs, and links. They contain **no log excerpts**, credentials, AI text, or report prose. This first slice fixes excerpt release to off. Reports remain behind Sherlock authentication. Remote recipients need a reachable HTTPS `SHERLOCK_ORIGIN`; localhost links work only on the machine running Sherlock.

## Delivery behavior and limits

- New findings, severity increases, manual resolutions, and completed investigations commit an event and River outbox job in the domain transaction. Ordinary scan refreshes emit nothing.
- Two dedicated notification workers and one in-flight request per destination across app replicas. Maximum one request/second/destination; a 429 can extend that interval. Telemetry retains its separate four workers.
- Unique destination/event identities prevent duplicate logical deliveries. Grouped events are rechecked before sending. Snoozed/dismissed/resolved findings cannot send an old creation/escalation alert; acknowledgement suppresses an unsent creation alert. A severity increase can notify an acknowledged finding. Explicit report-ready subscriptions notify completed investigations.
- Quiet hours defer automatic messages, including immediate rules. Messages expire at the earlier of 24 hours from event creation and their source evidence retention. Long quiet periods can therefore cancel a message. Test messages expire after 24 hours.
- Eight network attempts maximum, with exponential backoff from five seconds plus jitter. Honor `Retry-After` and Discord's `retry_after`. Retry network failures, 429 and 5xx; other non-2xx responses fail visibly.
- Each attempt is committed before dispatch. A timeout, lost acknowledgement, or crash leaves an **unknown** attempt outcome. Recovery/retry may duplicate an accepted external message. A failed delivery can be manually retried only while its original expiry/attempt budget and unchanged enabled destination permit it.
- Retry bodies and IDs stay unchanged; signatures/timestamps are refreshed. If an already-frozen batch loses eligibility, the whole batch is cancelled rather than silently changing its bytes.
- Slack requires HTTP 200 with `ok`. Discord uses `wait=true` and requires a returned message ID. Generic receivers acknowledge with any 2xx. Slack sends new messages only. No automatic user/channel mentions; Slack unfurls are disabled. Receiver response bodies and bearer URLs never appear in errors/history.
- Transport: 10-second timeout, 32 KiB outgoing body, 64 KiB response, no redirects, no inherited proxy, and verified TLS. Operational delivery/attempt metadata lasts 30 days; event/body content expires sooner. Cleanup runs in bounded batches through the existing maintenance pump.

## Private/local receivers

Webhook network settings are separate from telemetry allowlists. Public HTTPS needs no exception. Add narrow deployment exceptions to `.env` only when needed, then recreate the app:

```dotenv
SHERLOCK_NOTIFICATION_HOSTS=alerts.internal
SHERLOCK_NOTIFICATION_CIDRS=
SHERLOCK_NOTIFICATION_HTTP_HOSTS=
```

The host exception permits private addresses; loopback requires an explicit CIDR. An unencrypted local fixture additionally needs its exact hostname in `SHERLOCK_NOTIFICATION_HTTP_HOSTS` (no scheme or port). For a host-run fixture, for example, use CIDR `127.0.0.0/8` and HTTP host `127.0.0.1`. Inside Docker, loopback refers to the app container; configure a reachable receiver hostname/address instead. Do not copy the isolated test settings into a remote deployment.

Metadata/link-local, multicast and unspecified addresses stay blocked even with an exception. DNS results are validated and the validated address is dialed. Slack URLs must use `hooks.slack.com/services/...`; Discord URLs must use `discord.com/api[/vN]/webhooks/...`, with optional numeric `thread_id`. No endpoint override or insecure TLS switch is available.

## Signed generic receiver contract

POST body (version 1; IDs shown below are placeholders):

```json
{
  "version": 1,
  "delivery_id": "delivery-uuid",
  "events": [{
    "id": "stable-event-uuid",
    "type": "finding.created",
    "at": "2026-10-10T12:00:00Z",
    "service_id": "service-uuid",
    "finding_id": "finding-uuid",
    "origin": "demo",
    "severity": "warning",
    "summary": "[SIMULATED] finding.created · warning · checkout-service · Request error proportion increased",
    "url": "https://sherlock.example/findings"
  }]
}
```

`run_id` is present for investigation completion; `finding_id` for finding events. Synthetic tests use type `test`, origin `demo`, and omit resource IDs. Receivers must not treat test events as incidents. A batch contains 1–10 events.

Headers:

- `X-Sherlock-Delivery-ID`: stable delivery UUID, matching the envelope.
- `X-Sherlock-Timestamp`: decimal Unix seconds, refreshed each attempt.
- `X-Sherlock-Signature`: `v1=` plus lowercase hex HMAC-SHA256 over `timestamp + "." + exact_body_bytes` using the signing secret as UTF-8 bytes.

Verify against the raw body before parsing JSON. Reject timestamps outside a five-minute window in either direction and compare signatures in constant time. Example verification logic (Python standard library):

```python
import hashlib
import hmac
import time

def verify(secret: str, timestamp: str, signature: str, body: bytes) -> bool:
    if len(body) > 32 * 1024 or len(timestamp) > 20 or not timestamp.isascii() or not timestamp.isdecimal():
        return False
    if abs(time.time() - int(timestamp)) > 300:
        return False
    signed = timestamp.encode("ascii") + b"." + body
    expected = "v1=" + hmac.new(secret.encode("utf-8"), signed, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected.encode("ascii"), signature.encode("utf-8"))
```

After verification, validate version/schema and matching header/envelope delivery IDs. Persist a deduplication record for each event ID **atomically with enqueueing your own work** before acknowledging with 2xx. Duplicate accepted events should also return 2xx. Timestamp verification alone does not prevent replay within five minutes; keep event dedup records for at least the 24-hour retry horizon (30 days is a useful operational default). Apply your own body/rate limits. This envelope is not an inbound Sherlock API or native Alertmanager schema.

## Verification scope

Local tests use isolated receivers and emulated Slack/Discord response contracts. They exercise actual Go HTTP dispatch for generic webhooks, signatures, database transactions, worker recovery, browser controls, retention, encryption rotation, and pg_dump/pg_restore. No production channel is contacted by tests. Live Slack/Discord channel smoke tests require your selected webhook URLs and remain a deployment check. See [progress](progress.md) for the checks actually run.
