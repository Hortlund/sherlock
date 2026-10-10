# Codex build prompt — Infra Sherlock

You are implementing **Infra Sherlock**, a self-hosted, connector-first AI reliability intelligence product. Read `SPEC.md` in full before coding. It is the binding product and engineering specification.

## Goal

Build a real, runnable product that starts with Docker Compose and presents a setup UI allowing users to connect their existing Grafana/Prometheus/Loki/Kubernetes stacks and remote read-only MCP servers, discover services, run proactive detectors, investigate live/application/infrastructure issues, and view evidence-linked findings. Use OpenAI Decisions optionally for low-cost typed triage and a separate investigation provider for deeper reasoning. The product must work in demo/deterministic mode without external API keys.

## Operating instructions

1. Inspect the repository first. If empty, initialize a Go API/worker, React/TypeScript frontend, PostgreSQL migrations, Docker Compose and Makefile according to the spec.
2. Execute the **implementation slices in Section 15 in order**, keeping the repository bootable after every slice. Do not skip foundational security and persistence work to demo agent output.
3. Start with Slice 1: actual containers, health checks, seeded demo page and tests; complete it fully before beginning Slice 2.
4. Prefer concrete integrations and fixtures over mock-only interfaces. Maintain a working end-to-end path, not a wall of placeholders.
5. Implement read-only defaults and enforce all MCP/tool/credential restrictions **server-side**. No direct `kubectl exec`, shell execution, or production changes.
6. Add contract tests and Playwright tests for the connection wizard and investigation flow. Never claim external tests passed if they weren't run.
7. Verify current official OpenAI Decisions, MCP and vendor API specs before depending on details; abstract providers/connectors so releases can evolve.
8. Use straightforward idiomatic Go and TS. Keep a modular monolith, not microservices. PostgreSQL-backed jobs; avoid Redis/Kafka until needed.
9. Maintain `docs/decisions.md`, `docs/progress.md`, and `AGENTS.md`, recording assumptions, completed slices, commands executed, actual test status, and blockers.
10. At the end of each implementation step summarize modified files, acceptance criteria, verified commands/tests, and next smallest remaining work. If time is limited, stop at a complete, tested vertical slice rather than generating partial fake functionality.

## First user story

As a developer, I can run `docker compose up --build`, open `http://localhost:8080`, bootstrap an admin account, choose a clearly labeled demo environment, see a simulated checkout-service warning, click Investigate, and inspect the original evidence in the resulting report — without configuring a cloud account or API key. Then I can open Connections and add/test a supported real source.

**Begin coding now. Do not merely create a plan.**
