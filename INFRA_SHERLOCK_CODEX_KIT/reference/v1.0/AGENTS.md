# Infra Sherlock — Agent instructions

- Read `SPEC.md` before changing architecture or interfaces. It is the source of truth.
- Complete implementation slices vertically and in order; keep the app bootable and tests passing.
- Use Go for API/connectors/detectors/workers, React/TypeScript for UI, PostgreSQL for persistence/queue.
- Build a user-friendly connection wizard. Every capability must report real status and signal coverage.
- Defaults: read-only, minimal privileges, locally testable, user opt-in for external AI data transfer.
- Enforce MCP tool permissions, secrets, scoping, query bounds and SSRF controls in the server, never just the UI.
- No unbounded LLM access to logs, credentials, shell, production mutation, or unsupported claims of causality.
- Support fully working deterministic/no-AI demo path, as well as optional OpenAI Decisions triage.
- Prefer reliable maintained packages; verify upstream API specs when implementing integrations.
- Add unit, integration and Playwright E2E coverage for each slice. Report actual test results, never claim tests passed without execution.
- Keep `docs/progress.md` and `docs/decisions.md` up to date with delivered slices and assumptions.
