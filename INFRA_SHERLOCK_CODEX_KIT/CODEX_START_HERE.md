# Infra Sherlock — Build handoff

Read [SPEC v1.2](SPEC.md), [AGENTS.md](AGENTS.md), and the [Phase 2 implementation plan](../docs/phase2-plan.md).

**Current stage:** the Go + React/TypeScript + PostgreSQL deterministic MVP is delivered. Phase 2 Slices A/B add outbound notifications and reactive OpenAI/Anthropic analysis; check the implementation plan and progress for verification. Decision-model triage, proactive AI, tools/follow-up and inbound triggers remain planned. See [AI setup](../docs/ai.md).

The unchanged [v1.1 handoff](reference/v1.1/CODEX_START_HERE.md) records the delivered MVP contract. The [v1.0 vision](reference/v1.0/SPEC.md) preserves broader ideas. Neither archive governs the next implementation.

## Next product journey

Connect Prometheus/Loki → detect a meaningful change → send a factual configured alert → optionally triage/investigate with OpenAI or Anthropic → inspect cited evidence and usage → ask a scoped follow-up.

OpenAI Decisions is optional compact triage; a separate reasoning-model role handles deep analysis. The existing deterministic report and no-key demo continue to work. The server controls eligibility, budgets, tools, and destinations.

## Phase 2 delivery order

1. Durable events and Slack/Discord/signed generic outbound notifications.
2. Provider configuration, privacy/budgets, and reactive structured AI reports.
3. Optional Decisions/structured classification and proactive change gates.
4. Bounded read-only investigation tools and report follow-up.
5. Signed inbound alert triggers bound to an existing service.

Use the existing River/Postgres architecture with separate queues. Preserve lifecycle/fingerprint compatibility. Extend encryption rotation and retention alongside new secret/content types. Verify each slice against the plan's acceptance cases and keep the existing demo bootable.

Read `../docs/progress.md` for actual verification, `../docs/decisions.md` for architecture choices, and `../README.md` for startup. Do not run paid model calls or send test messages to an external channel without the corresponding user-configured credentials, egress settings, and test destination.
