# Infra Sherlock — Agent instructions

- Read `SPEC.md` before changing architecture or interfaces. Version 1.1 is the active source of truth.
- `reference/v1.0/` contains unchanged historical documents, not current implementation instructions. Do not restore its broader P0 scope or follow its instruction to start coding during a documentation task.
- Implement only when the user requests implementation. Follow the five slices in Section 12; keep the app bootable and verify each slice before starting the next.
- Use Go for the API/connectors/detectors/workers, React/TypeScript for UI, and PostgreSQL for persistence and durable jobs. Keep a modular monolith.
- MVP: direct Prometheus/Loki, manual service mapping, three deterministic detectors, findings lifecycle, and deterministic evidence-based investigations.
- Defer Grafana discovery, Kubernetes, AI providers/triage, MCP, Daily Brief, external notifications, and additional detectors to the roadmap.
- Make the connection wizard and signal coverage honest. Missing or truncated data is not evidence of health; citations alone do not establish causality.
- Enforce authentication, credential protection, workspace scoping, query bounds, and network destination policy server-side before connecting real sources.
- Keep connected systems read-only. No external AI data transfer, shell access, or production mutation in the MVP.
- Demo telemetry must exercise the same adapters and application pipeline, with visible synthetic labels and no mixing with live evidence.
- Prefer maintained packages and verify upstream APIs during implementation. Evaluate River before building custom queue infrastructure.
- Add relevant unit, integration, and Playwright coverage as slices deliver behavior. Report actual results; distinguish fixture tests from real Prometheus/Loki tests.
- Keep project-root `docs/progress.md` and `docs/decisions.md` current with delivered work, assumptions, verification, and blockers.
