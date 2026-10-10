# Infra Sherlock — Build handoff

Read [SPEC.md](SPEC.md) in full before implementation. Version 1.1 defines the focused MVP. The [original handoff](reference/v1.0/CODEX_START_HERE.md) and [original spec](reference/v1.0/SPEC.md) are historical reference only.

**Current stage: all five MVP slices implemented and locally verified on 2026-10-10. Read project-root `docs/progress.md` for actual checks and limits, and `README.md` to run the app.**

## Goal

Build a self-hosted Go + React/TypeScript + PostgreSQL product with one complete journey:

**Connect Prometheus/Loki → map a service → scan → see a finding → investigate → inspect evidence.**

The MVP includes three deterministic detectors: new exception fingerprint, warning burst, and error-rate regression. Investigations use deterministic rules and reports. No AI credentials or external model calls are required or supported in this release.

## Implementation instructions

1. Inspect the project and active instructions. Application code belongs at the project root; this kit remains the canonical specification location.
2. Execute the five slices in **SPEC.md Section 12**, in order: secure foundation; connections/mapping; detection/findings; investigations; real-source verification/release.
3. Keep a modular Go monolith with PostgreSQL-backed durable jobs and React assets served by the application. Evaluate River before creating custom queue infrastructure.
4. Apply credential encryption, authentication/scoping, network policy, and bounded queries before using real sources. Keep all upstream operations read-only.
5. Implement concrete Prometheus/Loki adapters and editable signal mappings. Do not assume metric names, label conventions, complete samples, or sufficient baselines.
6. Use reproducible demo telemetry through the same adapters and pipeline. Do not seed completed reports as a substitute for working detection/investigation.
7. Add relevant unit, integration, and Playwright checks as functionality is delivered. Verify actual local Prometheus/Loki behavior before release, and report exactly what ran.
8. Keep project-root `docs/decisions.md` and `docs/progress.md` current. At each slice report delivered behavior, acceptance results, commands/tests, and blockers.
9. Keep Grafana discovery, Kubernetes, AI, MCP, Daily Brief, notifications, and advanced detectors outside this build. Consult Section 13 for the retained roadmap.
10. Prefer a complete tested slice to a broad collection of placeholders. A documentation request does not authorize starting implementation.

## First complete user story

After the documented local setup, I start Compose, bootstrap an admin, and select Demo. Synthetic checkout-service telemetry flows through the collectors and detectors to produce a database-timeout finding. I click Investigate and inspect its observations, source evidence, and unknowns without a cloud account. I can then connect real Prometheus and/or Loki, map a service, and follow the same journey with supported signals.

This full story is delivered across slices 1–4; it is not falsely presented as complete in the initial skeleton.
