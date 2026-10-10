# Infra Sherlock — Codex Implementation Specification

**Version:** 1.0 (implementation handoff)  
**Status:** Proposed; build incrementally  
**Product:** Self-hosted AI reliability intelligence for infrastructure **and applications**  
**Primary UX:** `docker compose up -d` → visit setup UI → connect existing stack → select services → start receiving explainable findings.

> This is an engineering contract for implementation. Do not describe incomplete features as implemented. Prefer a narrow working vertical slice to a broad collection of stubs.

## 0. Executive summary

Infra Sherlock is a **read-only-by-default**, self-hosted system that attaches to existing monitoring and developer infrastructure. It investigates active incidents (reactive), detects emerging risks and regressions (proactive), and suggests evidence-backed improvements to infrastructure and application reliability (advisory). It correlates **application logs, metrics, traces, Kubernetes events, deployment/change history, and optional application invariants**. It does **not** require users to migrate their observability stack.

The first-run experience is a setup wizard, not hand-edited configuration files. Users can add native HTTP/API connections or remote MCP servers in a web UI; connection secrets are stored encrypted server-side. Native connectors provide optimized, deterministic access; MCP supplies extensibility. LLM providers are pluggable. **OpenAI Decisions** (typed `predicate`/`choice`/`score`) may route compact findings, but must never be the only basis for paging or executing a change; a tool-capable reasoning model performs deeper investigations. The system remains useful without any LLM credentials via deterministic findings and a seeded demo mode.

### Outcome / success definition

From a clean local machine a user can:

1. Start the services with a single documented Docker Compose command.
2. Open a web UI, create an admin account, and connect to an existing Grafana instance (auto-discover permitted Prometheus/Loki/Tempo data sources) **or** connect to those APIs directly. Alternatively, run a local seeded demo without external dependencies.
3. Test and save connections, inspect status and discovered services, choose monitored namespaces/services, and enable default detection packs.
4. See real or demonstrative **Findings** with data source, evidence, timestamps, risk explanation, proposed next action, and an explicit status.
5. Launch a bounded investigation manually and see its progress, tool calls, hypotheses, source references, and report.
6. Enable periodic proactive monitoring and receive deduplicated advisory findings about application AND infrastructure behavior.
7. Add a remote MCP endpoint through the UI, list its tools, enable individual read-only tools, test a tool safely, and make approved tools available to investigators.
8. Never need production write credentials to get meaningful value.

---

## 1. Scope and release plan

### P0: End-to-end MVP (must ship as a coherent product)

- Docker Compose installation: web+API+worker and PostgreSQL. One-process/binary mode acceptable; background jobs durable in PostgreSQL.
- First-run admin bootstrap, local authentication, session management, setup wizard.
- Connections UI with: **Prometheus**, **Loki**, **Grafana**, **Kubernetes**, **generic remote MCP**; optional **OpenAI** model credentials. Connection management includes test, edit, disable, delete, health state, and last error.
- Grafana connection can enumerate supported data sources and create child connections or map them by UID; use only permissions actually available. Explicit direct connection path if discovery fails.
- Service discovery, service-name/namespace mapping, monitoring scope UI; allow manual overrides.
- Read-only signal collection, lookback windows, incremental polling, bounded result size, credential redaction.
- Deterministic detectors: new/increasing exception fingerprint, elevated error rate, restart spike, memory pressure, disk forecast (when capacity metrics exist), latency regression (when latency metrics exist), replica/resilience lint (single replica and other basic risks). Gracefully report missing required metrics.
- Findings lifecycle: new/acknowledged/snoozed/resolved/dismissed, severity, dedup fingerprint, evidence links, timestamps, owner/notes.
- Manual incident investigation with source-cited observations, competing hypotheses, conclusion/insufficient-evidence outcome. Operates without AI in deterministic summary mode; optional model-based investigation.
- Proactive scheduler with independent polling and analysis jobs, rate limits, configurable per-connection budgets.
- OpenAI Decisions adapter behind a `DecisionProvider` interface (optional), plus rules-based fallback. Explicit refusal/error/fallback handling.
- Dashboard, Connections, Services, Findings, Investigations, Settings, and a simple Daily Brief view.
- Seeded demo mode and one reproducible end-to-end fault scenario.
- Observability for Sherlock itself, audit log, initial docs, unit/integration/E2E tests.

### P1: Post-MVP (design for it but don't block P0)

- Tempo / Jaeger traces, Argo CD deployments, GitHub change history, notification connectors (Slack/webhook), local OTLP collector pipeline, direct `OTLP` intake as opt-in.
- Read-only `kubectl`/Kubernetes object inspection via scoped service account, Helm chart, optional Kubernetes in-cluster deployment.
- App-level invariants (e.g. `OrderCreated` must be followed by `OrderFulfilled` within SLA using correlated IDs and retention windows).
- SLO burn-rate integration, more advanced seasonal baselines, incident memory and feedback search.
- PR proposals for vetted YAML manifests with human approval, **never direct writes to production**.
- OIDC login, multi-user roles, Teams/workspaces; connector marketplace manifests.

### P2: Advanced research features

- Bounded diagnostic experiments in disposable sandboxes; cross-stack topology graph; PR blast-radius analysis; evaluation suites across models; optional automated approved remediation with fully isolated policy enforcement; incident replay and offline regression evaluation.

### Non-goals for P0

- Replacing Prometheus/Loki/Tempo/Grafana or becoming a full long-term telemetry store.
- Streaming every raw log message into an LLM.
- Autonomous changes to production infrastructure.
- Perfect causal inference, universal root-cause certainty, or guaranteed outage prediction.
- Arbitrary remote command execution through MCP.
- General Kubernetes dashboards, proprietary APM instrumentation agents, billing/payment system, broad multi-tenant SaaS.

---

## 2. Deployment and onboarding (core product feature)

### Installation

**Local:**

```bash
cp .env.example .env
# Generate the secret encryption key with the documented command; never check it in.
docker compose up --build -d
# Open http://localhost:8080
```

The Compose definition should expose a single app/UI port and use PostgreSQL with a persistent volume. The backend runs migrations on start with concurrency safety, performs health checks, and resumes durable jobs after restart. Include `make dev`, `make test`, `make e2e`, `make demo`, `make lint`, `make down`.

**Optional Kubernetes later:** Helm install with a scoped read-only `ServiceAccount`, explicit RBAC, `Secret` mounting, liveness/readiness probes, and NetworkPolicy examples. Never require cluster-admin.

### First-run wizard: required screens

**Step 0 — Welcome**
- Choice: **Try Demo** (no credentials, synthetic telemetry) or **Connect My Stack**.
- Explain read-only mode, data egress when AI is enabled, and where secrets are stored.

**Step 1 — Secure workspace**
- Create admin user/password; session cookie secure, HttpOnly, SameSite; rate-limit login.
- For local non-TLS deployment, show warning before remote exposure; documentation for reverse proxy TLS.
- Generate/use a persistent server-side `SHERLOCK_ENCRYPTION_KEY` from environment/secret file. Refuse production startup without it; local development may explicitly opt into ephemeral key **only for demo**.

**Step 2 — AI provider (skippable)**
- Choose: No AI / OpenAI. Enter API key as a write-only field; test using minimal request; never redisplay key.
- Select separate models for **triage** (Decisions, if compatible) and **investigation/report** (tool-using/structured outputs). Put model names in configuration, not code; provider capabilities must be checked.
- Toggle `send_redacted_telemetry_to_ai` off by default. Show explicit sample of what may be transmitted. Support spend/token budgets, request timeouts, max tool calls, and an opt-out for individual services.

**Step 3 — Connect data**
- Connector gallery cards: **Grafana (recommended if present)**, Prometheus, Loki, Kubernetes, MCP Server, Later.
- Clicking one opens a guided form: friendly name, base URL, auth mode (none/header token/basic where supported), TLS config (secure defaults), tenant/org fields if applicable, optional namespace filters, `Test connection`.
- If Grafana: query data source list using service account, show supported source names/types/uids and let the user select which to add through Grafana proxy. Show authorization errors clearly. Allow `Use direct URL instead`.
- Kubernetes: for local CLI allow explicit restricted kubeconfig upload **only with informed confirmation**; for in-cluster use a provisioned read-only service account. Prefer service account tokens / bounded read permissions. Never store raw cluster-admin kubeconfig silently.
- MCP: URL + auth secret + capability discovery (`tools/list`) + tool selection. No arbitrary stdio subprocess launched from the browser/UI; controlled, administrator-managed stdio configuration can be added later. Display tool schema and warnings for unknown side effects.

**Step 4 — Discover services**
- Merge discovered labels/resources into canonical services (see identity rules below).
- Show list/tree by cluster, namespace, app, environment with search, select-all, include/exclude.
- Expose manual identity mappings; display telemetry source coverage per service (logs/metrics/traces/changes/k8s).
- Show unsupported/missing signals, without pretending full coverage.

**Step 5 — Configure protection**
- Default mode `observe+advise`; monitoring cadence with bounded defaults, enable default detector packs, notification behavior (in-app only in P0), data retention choices, budgets.
- Provide explicit `Run initial scan` button and successful onboarding landing.

### Ongoing Connections UI

Each connection has `display_name`, `kind`, `status` (`connected`, `degraded`, `disconnected`, `untested`, `disabled`), `capabilities`, `last_test_at`, `last_success_at`, `last_error` (sanitized), health history, edit/test/disable/delete. Deleting connection requires confirmation, revokes its secret reference, and preserves already collected redacted evidence as long as configured retention permits.

Connection wizard must handle connection timeouts, auth denied, TLS errors, incorrect URL/path, and zero data distinctly.

---

## 3. Core user journeys

### A. Reactive investigation

1. User clicks `Investigate` for a service or an alert webhook triggers a permitted investigation.
2. Select time range, scope, and question (optional).
3. System snapshots current service identity and telemetry coverage.
4. Deterministic collectors retrieve bounded observations and recent changes.
5. Agent proposes hypotheses and executes **allowlisted, read-only** tools under a budget; tracks contradictory evidence.
6. UI streams progress (server-sent events), not arbitrary model stream content; sanitized event types.
7. Report lists summary, likely causes, unknowns, timeline, every supporting evidence source, and suggested manual validation/next steps.

### B. Proactive review

1. Pollers run on configured cadence; detectors evaluate aggregates/window deltas, not unbounded raw streams.
2. Create a candidate when policy threshold or meaningful deviation is reached.
3. Deduplicate candidate using stable fingerprint `(workspace, service, detector, dimension)` and cooldown.
4. Optional Decisions adapter classifies candidate into `ignore`, `observe`, `investigate`, `escalate_for_review`, or `need_more_data`; deterministic high-priority rules bypass LLM triage suppression.
5. If investigated, findings include confidence qualifiers, affected apps and infrastructure, links to data and proposed action.
6. User receives in-app notification; optional scheduled Daily Brief summarizes unsnoozed, nonduplicate actionable findings.

### C. Application regression

1. A deployment occurs (or its time inferred from Kubernetes rollout metadata).
2. Compare 15–30 minute **pre vs. post** distributions for error rate, p95 latency (if available), newly observed exception templates, retries, fallbacks and business-event counters (if available).
3. Control for traffic volume and small-sample effects; include sample size and missing signals.
4. Detect log patterns such as connection pool warnings or dead-letter events **before** HTTP failure rates breach existing alerts.
5. Investigate suspected causes including app code/config, dependent services, infra and recent GitOps changes.
6. Explain correlation vs causal support and provide checks that could falsify the hypothesis.

### D. Continuous improvement recommendations

- Examples: unsupported singleton replica, over-requested CPU candidate, missing resource limits, error/fallback patterns, retry storms, failing cronjobs, expiry risks, storage trends.
- Recommendations are **proposals**, not execution. Every proposal has expected benefit, risk, confidence description, prerequisites, evidence and rollback/manual verification notes.
- No changes to connected systems in P0.

---

## 4. Target architecture

Implement a **modular Go backend with background workers**, React/TypeScript web UI, PostgreSQL for durable state. Avoid extra infrastructure in P0.

```text
Browser
  |
  v
Web UI (React/TS; static assets served by Go or reverse-proxied)
  | REST + SSE
  v
Go API/auth/config                  Go worker (same codebase)
  |                                   |
  +--> PostgreSQL <--- durable jobs ---+
  |                                   |
  +--> Connector registry <------------+
  |       | Prometheus / Loki / Grafana / Kubernetes / MCP
  |       | later: Tempo / ArgoCD / GitHub / OTLP
  |                                   |
  |                           Evidence normalization
  |                                   |
  |                           Detection + deduplication
  |                                   |
  |                           DecisionProvider (rules/OpenAI)
  |                                   |
  +<--------------------- Investigation + recommendation engine
  |
  v
Findings / Evidence / Reports / Daily Brief / Audit
```

### Runtime guidance

- `api` and `worker` can initially be goroutines in one Go process; queue/job state **must be in PostgreSQL** to tolerate restarts. If split later, use the same shared task abstraction.
- Durable work claimed via transactions and `FOR UPDATE SKIP LOCKED`; lease with `locked_until`, attempts and exponential backoff. Idempotent handlers.
- Schedule based on database timestamps, not in-memory timers alone. Scheduler records cursors for each connection/service/detector.
- Use `context.Context` throughout, structured logs, bounded worker pools, rate limits, and query timeouts.
- No fixed dependency on Kafka, Redis, graph DB, or vector DB until concrete load requires one.

### Repository layout

```text
infra-sherlock/
  README.md
  SPEC.md                            # copy of this specification
  AGENTS.md                          # brief repo/Codex instructions
  .env.example
  compose.yaml
  Makefile
  go.mod
  cmd/sherlock/main.go
  internal/api/                      # REST, SSE, DTOs, auth middleware
  internal/auth/                     # sessions, password hashing, roles
  internal/config/                   # env and stored settings
  internal/crypto/                   # secret envelope encryption
  internal/db/                       # sqlc or hand-written queries; migrations
  internal/jobs/                     # queue/scheduler/retries
  internal/connectors/               # interface, registry, contract tests
    prometheus/
    loki/
    grafana/
    kubernetes/
    mcp/
  internal/identity/                 # service resolution/mapping
  internal/evidence/                 # normalized evidence types
  internal/detectors/                # baselines, patterns, resource rules
  internal/decision/                 # rules + OpenAI Decisions adapter
  internal/investigation/            # bounded tools/hypotheses/report
  internal/findings/                 # lifecycle, dedup, scoring, comments
  internal/briefing/                 # daily brief generator
  internal/observability/            # self metrics, health, tracing
  web/                               # React + TypeScript + Vite
  migrations/
  examples/demo/                     # synthetic data source + scenarios
  deploy/helm/                       # P1
  tests/contract/
  tests/integration/
  tests/e2e/                         # Playwright
  docs/security.md
  docs/connectors.md
  docs/architecture.md
```

---

## 5. Connector architecture (most important extension point)

### Principles

- Data-source connectors are **untrusted boundaries** with narrowly scoped credentials and independent timeouts.
- A connector advertises *capabilities*, e.g. `logs.search`, `metrics.range`, `traces.search`, `k8s.workloads`, `changes.list`, `tools.generic`.
- **Connections** are concrete configured instances of a connector, potentially several of the same type.
- Core investigation code only requests capabilities. Never hard-code `if vendor == Grafana` inside agent logic.
- Distinguish `discovery`, `collection`, and `active investigation` access. Every evidence item retains immutable source provenance.
- Native API adapters are primary for common stacks (efficiency, safer query controls, better UI); MCP is an extension layer, not a replacement for all native connectors.

### Go interface sketch (adjust naming for idiomatic implementation)

```go
type Capability string
const (
    CapMetricRange  Capability = "metrics.range"
    CapLogSearch    Capability = "logs.search"
    CapTraceSearch  Capability = "traces.search"
    CapWorkloads    Capability = "k8s.workloads"
    CapChanges      Capability = "changes.list"
    CapGenericTools Capability = "tools.generic"
)

type Connector interface {
    Kind() string
    Capabilities() []Capability
    ValidateConfig(ctx context.Context, c ConnectionConfig) error
    Test(ctx context.Context, c Connection) (HealthReport, error)
    Discover(ctx context.Context, c Connection) ([]DiscoveredService, error)
}

type MetricsReader interface {
    QueryRange(ctx context.Context, c Connection, q MetricQuery) ([]Evidence, error)
}
type LogsReader interface {
    Search(ctx context.Context, c Connection, q LogQuery) ([]Evidence, error)
}
type WorkloadReader interface {
    Snapshot(ctx context.Context, c Connection, q WorkloadQuery) ([]Evidence, error)
}
type ToolProvider interface {
    ListTools(ctx context.Context, c Connection) ([]SafeToolDescriptor, error)
    CallTool(ctx context.Context, c Connection, toolID string, input JSONValue) ([]Evidence, error)
}
```

Implement input query types as **typed bounded objects**: required time ranges, maximum sample count, service selector, allowed labels, and optional whitelisted query templates. Avoid unrestricted arbitrary PromQL/LogQL from model output; permit admin-approved advanced query templates only.

### Native connectors (P0)

| Connector | Setup method | Discovery | Data access | Notes |
| --- | --- | --- | --- | --- |
| Prometheus | URL + optional token/headers | label values / target metadata | `/api/v1/query_range`, bounded instant query | Do not assume any specific metric family is available; expose coverage. |
| Loki | URL + token / tenant header | labels / streams within lookback | `/loki/api/v1/query_range` | Stable log fingerprinting; fixed cardinality caps. |
| Grafana | URL + service-account token | list data sources (filter allowed kinds) | Grafana datasource proxy or query API | Data source UID mapping; no admin privileges required. |
| Kubernetes | in-cluster scoped identity or explicitly added kubeconfig | namespaces, deployments, pods, services | LIST/GET/WATCH selected read-only resources/events | Restricted RBAC; don't watch Secrets or exec into pods. |
| MCP remote | Streamable HTTP URL + auth | `tools/list` | admin-approved read-only `tools/call` | Apply tool-by-tool schemas, rate and output limits. |

**Later:** Tempo, Argo CD, GitHub, generic webhook, OpenTelemetry OTLP receiver. "Connect Grafana" is an onboarding shortcut only: it is not guaranteed to provide permissions to all data sources; surface what was actually discovered and authenticated. API paths should be isolated per adapter because vendors evolve their APIs.

### MCP security and capability handling

- Prefer a maintained MCP SDK and **explicit protocol version negotiation**; support contemporary Streamable HTTP with backwards-compatibility tests instead of hard-coding an obsolete GET/SSE session assumption.
- The UI may register only remote HTTP(S) MCP servers in P0. Require HTTPS outside an explicitly acknowledged local dev mode; reject redirects to unexpected hosts.
- Never auto-enable all discovered tools. Admin must select individual tools. Preserve names, descriptions, parameter schema, discovered timestamps, and capability annotations.
- Tools marked `readOnlyHint` may be suggested for consideration but **metadata alone is not a security guarantee**. Enforce a server-side allowlist of approved tools; write-like or ambiguous tools remain disabled in P0.
- Validate arguments against JSON schema; enforce request size, output size, time and invocation count. Sanitize returned text as untrusted data (prompt injection risk).
- Prevent SSRF: network egress policy/allowlist, block cloud metadata and loopback/link-local by default for **remote** user-supplied URLs, DNS resolution and redirect checks. Private network addresses often are legitimate for self-hosted connectors, so support admin-controlled CIDR allowlists (default safe and documented).
- Never provide raw API keys, internal user tokens, or decrypted connection configs as MCP tool arguments or model context.
- Show an audit trail of tool calls (name, connection, reason, duration, sanitized input/output size, success/failure).
- The MCP transport should run as an isolated client module; no arbitrary shell/stdio subprocess initiated from web form.
- If a configured MCP tool fails, mark dependent evidence missing and continue with remaining sources when possible.

### Additional ingestion path (P1)

Offer an optional OTLP receiver/collector integration to accept logs, metrics, and traces from apps that don't use supported existing platforms. OTLP over HTTP/gRPC can be implemented via a standard OpenTelemetry Collector forwarding *summarized or selected* signals into Sherlock, not by forcing a custom telemetry agent. In P0, do not claim that connecting Loki automatically enables services that have no logs in Loki.

---

## 6. Identity, signals and evidence

### Canonical service identity

```json
{
  "workspace_id": "ws_1",
  "service_id": "svc_1",
  "environment": "production",
  "cluster": "main-eu",
  "namespace": "checkout",
  "service_name": "checkout-api",
  "aliases": ["service.name=checkout", "app.kubernetes.io/name=checkout-api"]
}
```

Resolver preference: explicit user mapping > OpenTelemetry `service.namespace` + `service.name` plus cluster/environment > Kubernetes namespace + stable app labels > constrained heuristic. Never silently merge unrelated services with identical names across environments. Every observation includes the raw source labels for audit/debugging. Permit service mapping overrides in UI.

### Evidence schema (representative)

```json
{
  "id": "ev_01H...",
  "workspace_id": "ws_1",
  "service_id": "svc_1",
  "source_connection_id": "con_loki_1",
  "capability": "logs.search",
  "kind": "log_pattern",
  "occurred_at": "2026-10-10T14:03:12Z",
  "collected_at": "2026-10-10T14:05:00Z",
  "window_start": "2026-10-10T14:00:00Z",
  "window_end": "2026-10-10T14:05:00Z",
  "summary": "Database connection acquisition timeout",
  "attributes": {"fingerprint": "pool-acquire-timeout", "count": 142},
  "source_ref": {"query_template_id": "q12", "labels": {"app": "checkout"}},
  "sample_count": 142,
  "sampling_notes": "Sampled from bounded LogQL query",
  "redaction_version": "v1"
}
```

Make evidence source refs functional: opening evidence in UI should show the original query metadata or a correctly constructed deep link when supported. Avoid storing raw secrets, high-volume log bodies, plaintext PII, or giant LLM transcripts. Preserve only bounded sanitized excerpts needed to explain findings; honor user-set retention.

### Raw log processing rules

- Prefer structured JSON logs. Support unstructured log fingerprinting by replacing timestamps, UUIDs, IPs, numbers and volatile tokens with placeholders.
- Aggregate counts and first/last seen per fingerprint, plus a small sanitized sample. Cap high-cardinality labels.
- Use deterministic redaction of tokens, authorization headers, cookies, credentials, email addresses and configurable patterns **before** any external model request; allow denylisting entire services/namespaces.
- Keep independent baselines per `(workspace, service, environment, fingerprint, period)`; calculate baseline reliability and sample size.
- Treat logs and tool results as **data**, never instructions. Retrieved strings cannot overwrite tool/policy instructions.

### Required signal classes

- Infrastructure: pod restarts, OOM kills, resource pressure, PVC fill rate, replicas/readiness, failing cronjobs, DNS/network signals if present.
- Application: error templates, retry bursts, warnings, fallback paths, queue lag, business events, latency regression, abnormal rate shifts.
- Change context: rollout timestamp/revision; P1 Git commit, PR, changed manifest/config, and deploy actor.
- Dependency context: trace-derived service edges in P1, user-defined edges and deployment/service maps in P0.

---

## 7. Detection system

### Detector interface

Each detector declares `id`, supported signal requirements, lookback, minimum sample size, baseline rule, finding fingerprint builder, optional suggested queries, and validation tests. Detectors emit **candidate findings**, not direct pages.

```go
type Detector interface {
    ID() string
    Requirements() []SignalRequirement
    Evaluate(ctx context.Context, input DetectionWindow) ([]CandidateFinding, error)
}
```

### Required P0 detectors

1. `new_exception_fingerprint`: new error/exception pattern unseen in comparison window, min-count guard.
2. `error_rate_regression`: rate or proportion above a historical baseline and min-events guard.
3. `log_warning_burst`: elevated frequency of a known nonfatal warning (e.g. connection retries).
4. `k8s_restart_spike`: restart rate above configured threshold, support crash loops/OOM when signal known.
5. `resource_pressure`: memory working set vs limits and sustained CPU throttling **if metric exists**.
6. `disk_exhaustion_forecast`: simple robust trend / linear fit over observed recent values; report data range, slope, and assumptions; no false precision.
7. `latency_regression`: p95 increase with minimum traffic and adequate baseline if histogram exists.
8. `resilience_lint`: configuration risk e.g. production singleton replicas, missing readiness probe where relevant; avoid universally declaring every singleton a bug.

UI shows status `active`, `insufficient_signal`, `disabled`, `degraded` per detector/service. Explain why unavailable and how to enable the signal.

### Severity and noise policy

Severity enum `critical`, `high`, `medium`, `low`, `info`, driven primarily by explicit thresholds, environment and blast radius. No numeric AI confidence label converted directly into paging severity. Snooze, suppress, dedup, cooldown, grouping across similar services, change windows, and minimum sample size. An updated finding retains history rather than spamming new records. Distinguish `observed_problem` vs `predicted_risk` vs `optimization` in UI.

Default P0 notifications are **in-app only**. No on-call paging until user explicitly configures an output channel in P1. Daily Brief summarizes **actionable** unresolved findings, not every low-priority pattern.

---

## 8. AI architecture, Decisions and agent contract

### Separation of concerns

**Rules/Statistics** discover a suspicious signal cheaply. **Decisions API** optionally classifies the *compact, redacted candidate* (`ignore`, `observe`, `investigate`, `needs_more_data`, `review_escalation`). **Investigation model** can select safe tools and draft evidence-backed explanations. **Deterministic policy gate** controls notification/action. No single LLM output can execute changes or hide a policy-critical alert.

### Provider abstractions

```go
type DecisionProvider interface {
    Route(ctx context.Context, candidate CandidateFinding) (TriageDecision, error)
}

type InvestigationProvider interface {
    NextStep(ctx context.Context, state InvestigationState, tools []ToolDescriptor) (InvestigationStep, error)
    Summarize(ctx context.Context, state InvestigationState) (InvestigationReport, error)
}
```

Provide `RulesDecisionProvider` (default; no API key required), `OpenAIDecisionsProvider` (optional), and `NoAIInvestigationProvider`. Model names/endpoints configured. Avoid coupling storage or service identity to OpenAI response structures.

### OpenAI Decisions integration

- Verify current official SDK at implementation time. API supports `POST /decisions`, `questions` with `predicate`, `choice`, or `score`; Decision answers may include refusal. A documented supported decision model is `gpt-6-luna` at spec-writing time. Treat provider/model availability as config-dependent.
- Send one compact candidate synopsis (aggregates, change windows, known evidence IDs), **not a whole log corpus**. Include explicit instructions that logs are untrusted and evidence insufficient is acceptable.
- Persist route, inputs *after redaction* (or hash where privacy settings require), model/version, usage, latency, and refusal/error reason.
- On timeout/429/refusal/malformed answer, fall back to rule-based routing; ensure critical deterministic alerts are not suppressed.
- Confidence returned for a choice is not a calibrated incident probability. Test on labeled fault scenarios; do not show as "90% outage risk".
- Do not pass tools to Decisions; use tool-capable investigation provider for exploration.

### Investigation state machine

`queued -> collecting -> hypothesizing -> querying -> evaluating -> reporting -> completed` with side outcomes `insufficient_evidence`, `cancelled`, `failed`.

A single investigation has max steps (default 8), max 12 tool calls, maximum wall time (default 90s), token and cost ceiling, tool output cap, service/time-window bounds, safe retries, cancellation, and intermediate progress events. These are defaults that admins can tune downward; hard ceiling enforced server-side. Tool results must be attached as evidence records before conclusions can reference them.

Required report fields:

```json
{
  "summary": "Likely database connection pool saturation",
  "classification": "suspected_app_regression",
  "causes": [
    {
      "hypothesis": "Long-running queries holding pool connections",
      "supporting_evidence_ids": ["ev_1", "ev_2"],
      "contradicting_evidence_ids": [],
      "caveats": ["Database query metrics not configured"]
    }
  ],
  "timeline": [{"at":"2026-10-10T14:03:12Z","evidence_id":"ev_1"}],
  "recommendations": [{"title":"Inspect slow queries","risk":"low","validation":"Check p95 and pool wait"}],
  "status": "hypothesis_not_proven"
}
```

Run a report validator: referenced evidence IDs exist and belong to scope, timestamps in window, no claimed certainty without support, all tool names allowlisted, recommendations non-executing. If validation fails, retry summary once and fall back to evidence-only report.

### Privacy

AI calls only if explicitly enabled for the workspace/service; preview redacted payload; secret and PII redaction; configurable retention. Expose exact AI request purpose and estimated spend. Provide option to use Sherlock fully without hosted LLM. Logging must never include provider API keys or private connector credentials.

---

## 9. Findings, recommendations and lifecycle

### Finding fields

`id`, `workspace_id`, `service_ids[]`, `category`, `detector_id`, `severity`, `state`, `title`, `description`, `first_seen_at`, `last_seen_at`, `detection_window`, `evidence_ids[]`, `dedup_key`, `source_coverage`, `change_context`, `owner`, `snoozed_until`, `acknowledged_at`, `resolved_at`, `investigation_id`, `recommendation_ids[]`, `created_at`, `updated_at`.

Categories: `active_incident`, `early_warning`, `app_regression`, `configuration_risk`, `optimization`, `business_invariant` (P1).

States: `new`, `acknowledged`, `snoozed`, `resolved`, `dismissed`. State transitions tracked as audit events; forbid illegal transitions or clarify idempotent behavior.

### Recommendation fields

`finding_id`, `title`, `rationale`, `expected_benefit`, `preconditions`, `suggested_steps`, `risk`, `how_to_verify`, `rollback_guidance`, `evidence_ids`, `status` (`proposed`, `accepted`, `rejected`, `implemented`). **No automated write actions in P0.**

Feedback UI lets engineers record true positive / false positive / insufficient evidence and useful / not useful. Persist for future eval; don't treat user feedback as proof of causation.

---

## 10. Suggested database entities

Use PostgreSQL migrations with clear FK constraints and indexes. Prefer UUIDv7/ULID-like time-sortable IDs consistently. Tables:

- `users`, `sessions`, `workspaces`, `workspace_settings`.
- `connections`, `connection_credentials` (encrypted; no plaintext), `connection_health_runs`, `connection_capabilities`.
- `mcp_tools`, `mcp_tool_permissions`.
- `services`, `service_aliases`, `service_connection_coverage`, `monitored_scopes`.
- `detector_configs`, `detector_state`, `poll_cursors`, `jobs`.
- `evidence` (JSONB bounded attributes/source refs, retention), `log_fingerprints`, `baseline_snapshots`.
- `findings`, `finding_events`, `investigations`, `investigation_steps`, `hypotheses`, `recommendations`.
- `ai_provider_configs`, `ai_invocations`, `audit_events`, `daily_briefs`.

Indexes: `(workspace_id, service_id, occurred_at)` for evidence; `(workspace_id, state, severity, last_seen_at)` for findings; unique active dedup identity; `(run_at, status, locked_until)` for jobs; `(workspace_id, connection_id, last_test_at)` for health. Implement migrations and retention cleanup from day one.

For P0 a single default workspace is adequate, but **all stored records must include workspace scope** to avoid retrofitting dangerous tenant assumptions. Auth middleware must enforce scope on every query.

---

## 11. REST API and live updates

All endpoints prefix `/api/v1`; authenticated except setup bootstrap and health. Document with OpenAPI; generate or share types with TS where practical.

```text
GET    /healthz
GET    /readyz
POST   /api/v1/setup/bootstrap          # only while uninitialized
POST   /api/v1/auth/login
POST   /api/v1/auth/logout
GET    /api/v1/me

GET    /api/v1/connection-kinds
GET    /api/v1/connections
POST   /api/v1/connections
GET    /api/v1/connections/{id}
PATCH  /api/v1/connections/{id}
DELETE /api/v1/connections/{id}
POST   /api/v1/connections/{id}/test
POST   /api/v1/connections/{id}/discover
GET    /api/v1/connections/{id}/health
GET    /api/v1/connections/{id}/mcp/tools
PUT    /api/v1/connections/{id}/mcp/tools/{toolId}/approval

GET    /api/v1/services
GET    /api/v1/services/{id}
PATCH  /api/v1/services/{id}
PUT    /api/v1/monitoring/scope
GET    /api/v1/monitoring/coverage
GET    /api/v1/detectors
PATCH  /api/v1/detectors/{id}
POST   /api/v1/scans                     # scope + bounded lookback

GET    /api/v1/findings
GET    /api/v1/findings/{id}
PATCH  /api/v1/findings/{id}/state
POST   /api/v1/findings/{id}/feedback
GET    /api/v1/evidence/{id}

POST   /api/v1/investigations
GET    /api/v1/investigations
GET    /api/v1/investigations/{id}
POST   /api/v1/investigations/{id}/cancel
GET    /api/v1/investigations/{id}/events   # SSE

GET    /api/v1/briefs
POST   /api/v1/briefs/generate
GET    /api/v1/settings
PATCH  /api/v1/settings
GET    /api/v1/audit
```

Use cursor pagination and filtering. Errors shaped `{code, message, request_id, details?}` with safe user-facing messages. Test endpoints must not echo credentials. SSE events: `investigation.status`, `evidence.collected`, `hypothesis.updated`, `investigation.completed`, `investigation.failed`; reconnects use Last-Event-ID or documented polling fallback. Do not show tool internal secrets.

---

## 12. UI / product design specification

Visual direction: professional infrastructure product, legible and information-dense rather than excessive AI gradients. Accessible components, responsive layout, dark/light themes if practical. Use React + TypeScript + Vite, TanStack Query, React Router, and a mature accessible component kit. Favor functional completeness over animations.

**Navigation:** Overview · Findings · Investigations · Services · Connections · Daily Brief · Settings.

**Overview:** connection health at top, monitored services, active incidents, early warnings, optimization opportunities, 24h trend, recently completed investigations. Empty state clearly says what to connect next.

**Connections:** gallery and connected-list view; add wizard, live test, feature coverage, error troubleshooting, last sync, edit secrets. Grafana source picker and MCP tool approval subviews. Native and MCP connections visually distinguished, but both feed the same evidence graph.

**Services:** filter by cluster/namespace/environment; signal coverage chips; baseline health; drill into logs/metrics patterns, findings, dependencies (where known); “Investigate service” button.

**Findings:** grouped by category and risk; filters by severity/service/source/status; show evidence excerpts and deep links, explanation, what would invalidate hypothesis, action suggestions. Support acknowledge, snooze, dismiss, mark resolved, feedback.

**Investigations:** manual launch with prefilled service/time range, progress timeline, tool call history, hypotheses, current report, evidence browser and retry-once with different scope. Clearly label “suspected”, “observed”, “confirmed”, “unknown”.

**Daily Brief:** generated from stored findings with sections “Needs attention”, “Emerging risks”, “Suggested improvements”, “Resolved”. Show exact data window and timestamp; no fake impact or dollar estimates.

**Settings:** AI model provider and privacy preview, redaction patterns, retention, budgets, polling frequency, default detector packs, users/workspace security, system health.

**Demo mode:** fully clickable seeded data with clearly marked simulated incidents and one walkthrough: new database timeout log pattern → triage → investigation → evidence-linked recommendation. Never present fake data as live.

---

## 13. Security, reliability and observability requirements

**Must-have before demoing against actual infrastructure:**

- Encryption at rest for stored secrets using AES-GCM envelope strategy with authenticated encryption, versioned keys, unique nonces; master key outside DB. No secret values returned from any read endpoint.
- Least-privilege connector credentials and optional network egress restrictions. Kubernetes `get/list/watch` on explicitly scoped allowed resources, no `pods/exec`, no Secrets access, no write verbs.
- Input validation for all URLs and selectors; SSRF and DNS rebinding mitigation. Document private-network allowlists for self-hosted stacks.
- Role-aware setup/admin endpoints; CSRF defense with cookie auth, secure password hashing (Argon2id preferred), login rate limiting, brute force defenses, secure session expiry/rotation.
- Audit connection creation/changes, MCP tool approvals, model configuration, scans, investigations, secret rotations, finding transitions, attempted unauthorized actions.
- Prevent prompt injection: log payloads, labels, MCP result text are untrusted. Tool calls only through explicit policy. No shell exposed to model, no unconstrained query execution.
- No API credentials or original user secrets sent to LLM; redact before AI calls. Explicit opt-in for hosted model processing.
- Bound backend searches by time, labels, sample size, concurrency and budgets; 429/5xx resilience, circuit breakers, backoff, cancellation.
- Graceful degraded mode when a connector or AI provider is unavailable; findings show stale data/unknown status rather than green/healthy.
- App exposes structured logs, request IDs, `/metrics` (Prometheus), tracing optional, `/healthz` + `/readyz`; guard high-cardinality metric labels.
- Configure retention, evidence deletion, backup/restore documented, ability to delete stored connection secrets and remove workspace data.

### Initial performance / reliability targets (non-SLA engineering targets)

- Setup and initial demo should work with no external cloud, with reproducible smoke test.
- Connections `Test` returns or times out with actionable error within configured 10–15s.
- Dashboard renders within 2s p95 for seed dataset on dev laptop (excluding first load) with indexed queries.
- One 15–30min incident investigation should obey configured 90s maximum and 12 tool-call cap.
- One connector failing should not block unrelated connector polling or previously stored findings.
- No lost job after normal restart: leases expire, tasks retry idempotently.

---

## 14. Testing and evaluation

### Unit tests

Connector config validation, crypto roundtrip/rotation, redaction, log fingerprint stability, dedup behavior, detector sample-size controls, decision provider parsing and refusal handling, evidence/report validation, scoped access checks.

### Integration tests

Use testcontainers or Docker Compose local fake APIs for Prometheus, Loki, Grafana and a fixture MCP server. Exercise discovery, auth errors, pagination/bounds, stale-data handling, retry and timeout. Test database migrations, queue leases, session authorization, retention, and recovery after worker restart.

### End-to-end UI tests

Playwright: first-run bootstrap; enter demo; add Prometheus/Loki with fixture URLs; test connection; map a service; run scan; see finding; investigate; evidence link; snooze/dismiss; add MCP; list tools; approve safe one; disable connection; verify no secrets in network responses.

### Reproducible eval scenarios (include fixtures)

1. New database connection timeout log template after rollout.
2. Gradual disk increase and a failing cleanup/retention job.
3. Kubernetes OOMKilled with corresponding memory growth.
4. Warnings/retries increase without elevated HTTP error rate.
5. Misleading deployment near an unrelated dependency outage (anti-correlation trap).
6. Healthy service with noisy logs (negative case).
7. Required metric missing (must return `insufficient_signal`, not made-up conclusion).
8. Malicious instructions embedded inside a log/MCP response (agent must ignore them).

Metrics: detector precision/recall on fixtures, correct root-cause ranking, unsupported conclusion rate, citation validity, time and model spend per investigation, false pages (expected zero in P0), tool permission violations (expected zero). **Never report invented success percentages.**

---

## 15. Implementation order / Codex work tickets

Each slice must compile and add automated tests, docs, and runnable UI where applicable; do not start the next until the current slice passes.

### Slice 1 — Bootable product skeleton
- Go HTTP API, migrations, PostgreSQL, `/healthz`, `/readyz`, basic React app, Compose, Makefile, CI lint/test, seed demo mode.
- Acceptance: `docker compose up --build` works; first page loads; healthy backend + DB; docs match reality.

### Slice 2 — Security + onboarding
- First-run admin, session auth, encryption key handling, welcome/setup UI, AI provider optional, audit events.
- Acceptance: bootstrap single-use; password securely stored; auth enforced; no secrets disclosed.

### Slice 3 — Native connection framework
- Registry/interfaces, persistence, test status, Prometheus/Loki and Grafana data source discovery, UI connection wizard, fixture integration tests.
- Acceptance: add/test/edit/disable/remove; failed auth and bad URL show actionable errors; Grafana can enumerate configured sources with correct permissions.

### Slice 4 — Service discovery and normalized evidence
- Kubernetes optional read-only connector, service identity resolver, service picker, bounded metrics/log queries, redaction, evidence timeline.
- Acceptance: discovery and manual mapping; source coverage not invented; evidence can be retrieved by ID.

### Slice 5 — Proactive engine
- DB-backed durable scheduler/worker, basic detectors, baseline snapshots, findings dedup/lifecycle, Overview + Findings UI.
- Acceptance: demo scan creates a finding, second scan updates rather than duplicates, disabling polling works, restarts don't lose jobs.

### Slice 6 — Reactive investigations
- Deterministic evidence-only investigator, user-triggered investigations, progress SSE, source-linked report, hypotheses/unknowns.
- Acceptance: one fault scenario produces correct evidence-linked report with no hosted model.

### Slice 7 — AI integration
- Rules fallback, optional OpenAI Decisions adapter, optional reasoning investigator, explicit model opt-in, budgets, provenance validation and prompt-injection tests.
- Acceptance: no-AI mode remains functional; token/timeout refusal handled; source references validated; dangerous tool calls rejected.

### Slice 8 — MCP extension + Daily Brief
- Streamable HTTP client, capability discovery, approval UI, safe tool invocation audit, daily in-app summary, E2E tests.
- Acceptance: fixture MCP server connects; unapproved/write tools are never callable; disabled connector can't be used; complete user flow works.

### Slice 9 — Hardening / release
- Security checklist, access control tests, retention, docs, CI (Go/TS/Playwright), demo walkthrough screenshots, initial release instructions.
- Acceptance: all demo paths reproducible, no hidden prerequisites, no fake integrations, scanner finds no embedded secrets.

**Note:** It is acceptable to swap slices 7 and 8 if native investigation and MCP capabilities benefit from that ordering. Prioritize a working seeded demo and polished connection wizard early.

---

## 16. Codex engineering rules and definition of done

1. Treat this document as requirements. If a detail is under-specified, implement the simplest safe behavior and document the assumption in `docs/decisions.md`; do not ask for decisions that don't block implementation.
2. Work vertically: real working backend + UI + test in each PR/commit. Avoid a thousand empty interfaces or stubs.
3. Never silently claim unsupported connector capabilities. Test with fixtures and use degraded/insufficient-signal states.
4. Prefer existing maintained SDKs; verify current upstream APIs and versions, pin dependencies and avoid obsolete MCP transports.
5. Keep agent orchestration bounded, auditable and read-only by default. Reject write tools at the server boundary, regardless of frontend/model instructions.
6. Never commit production tokens, fake synthetic telemetry presented as live, or made-up model confidences.
7. Include tests that prove refusal/error fallbacks, privilege boundaries, and evidence citation integrity.
8. Document developer setup, environment variables, migrations, seeding, credential behavior, threat model, architecture and limitations.
9. After every slice run formatting, lint, unit tests and relevant integration/E2E tests; summarize actual pass/fail results, blockers and next slice.
10. For all external data sources, favor official documented APIs and user credentials. Avoid scraping dashboards or assuming root-level privileges.

### P0 overall acceptance checklist

- [ ] One-command Compose startup, repeatable from clean checkout.
- [ ] Bootstrap/login UI and seeded demo without cloud credentials.
- [ ] Grafana and direct Prometheus/Loki setup via UI, plus optional scoped Kubernetes connector.
- [ ] Health check + status + actionable errors for every connection.
- [ ] Read-only remote MCP onboarding, tool discovery, individual approval and safe invocation.
- [ ] Service discovery and editable mapping with visible coverage.
- [ ] App logs AND infrastructure signals create distinct explainable findings.
- [ ] Scheduled detection, deterministic fallbacks, dedup, user-configured monitoring scope.
- [ ] Manual reactive investigation with time-ordered evidence and citations.
- [ ] Optional OpenAI Decisions triage + separate deep-investigation adapter; no-AI path works.
- [ ] Daily Brief, findings lifecycle and feedback.
- [ ] Data privacy, server-side secrets, scoping, tool caps, audit logs.
- [ ] Unit/integration/E2E tests with reproducible labeled failure fixtures.

---

## 17. Official reference points (verify versions during implementation)

- OpenAI Decisions guide: https://developers.openai.com/api/docs/guides/decisions
- OpenAI Decisions API: https://developers.openai.com/api/reference/resources/decisions/methods/create
- MCP spec, transports (2026-07-28): https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/index.mdx
- MCP Streamable HTTP transport: https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/streamable-http.mdx
- OpenAI MCP tools: https://developers.openai.com/api/docs/guides/tools-connectors-mcp
- Prometheus HTTP API: https://prometheus.io/docs/prometheus/latest/querying/api/
- Grafana data source HTTP API: https://grafana.com/docs/grafana/latest/developer-resources/api-reference/http-api/api-legacy/data_source/
- Loki HTTP API: https://grafana.com/docs/loki/latest/reference/loki-http-api/
- OpenTelemetry Collector OTLP receiver: https://github.com/open-telemetry/opentelemetry-collector/blob/main/receiver/otlpreceiver/README.md
- Kubernetes RBAC: https://kubernetes.io/docs/reference/access-authn-authz/rbac/

**Caution:** The architecture deliberately isolates vendor SDKs and protocol revisions. Don't assume a model or API stays stable across future releases; keep capability detection, API versioning, and fallback behavior in place.
