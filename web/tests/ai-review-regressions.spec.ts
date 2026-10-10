import { test, expect } from "@playwright/test";

const session = {
  user: { id: "admin", email: "owner@example.com" },
  workspace: { id: "workspace", name: "AI regression fixtures" },
  csrf_token: "fixture-csrf",
  expires_at: "2099-01-01T00:00:00Z",
};
const service = {
  id: "service",
  name: "Checkout",
  environment: "test",
  origin: "live",
  revision: 1,
};
const settings = {
  policy: {
    enabled: true,
    revision: 1,
    daily_limit: 1000000,
    run_limit: 100000,
  },
  providers: ["openai", "anthropic"].map((kind) => ({
    id: kind,
    kind,
    model: `${kind}-fixture`,
    enabled: true,
    capability: "ready",
    services: [service.id],
    revision: 1,
    input_price: 1000000,
    output_price: 2000000,
    price_date: "2026-10-10",
    allow_demo: false,
    allow_excerpts: false,
  })),
  spent_today: 0,
  reserved: 1000,
  fixture: true,
};
const investigation = {
  id: "run",
  kind: "investigation",
  service_id: service.id,
  state: "completed",
  snapshot: service,
  start: "2026-10-10T10:00:00Z",
  end: "2026-10-10T10:30:00Z",
  requests: 2,
  result: {
    report: {
      summary: "Fixture evidence",
      observations: [],
      unknowns: [],
      checks: [],
      adequate: true,
    },
  },
};
function common(path: string) {
  if (path === "/api/v1/me") return session;
  if (path === "/api/v1/ai/settings") return settings;
  if (path === "/api/v1/services") return { items: [service] };
  if (path === "/api/v1/investigations/run") return investigation;
  if (path === "/api/v1/settings") return { disabled: [] };
  return { items: [], next_cursor: "" };
}

test("late provider previews cannot overwrite the current selection", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  let release!: () => void;
  const delayed = new Promise<void>((resolve) => {
    release = resolve;
  });
  let submitted: { provider_id: string; preview_digest: string } | undefined;
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown = common(path);
    if (path.endsWith("/ai/preview")) {
      const id = route.request().postDataJSON().provider_id;
      if (id === "openai") await delayed;
      body = {
        provider_id: id,
        model: `${id}-fixture`,
        payload: { model: `${id}-fixture` },
        preview_digest: `${id}-digest`,
        reservation: 1000,
        input_bound: 100,
        output_limit: 80,
        expires_at: "2099-01-01T00:00:00Z",
      };
    } else if (path.endsWith("/ai")) {
      submitted = route.request().postDataJSON();
      body = { id: "new-run" };
    }
    await route.fulfill({ json: body });
  });
  try {
    await page.goto("/investigations?id=run");
    const analysis = page.locator(".ai-analysis");
    const select = analysis.getByLabel("Analysis provider");
    await select.selectOption("openai");
    const pending = page.waitForRequest((request) =>
      request.url().endsWith("/ai/preview"),
    );
    await analysis.getByRole("button", { name: "Preview AI payload" }).click();
    await pending;
    await select.selectOption("anthropic");
    await analysis.getByRole("button", { name: "Preview AI payload" }).click();
    await expect(analysis.locator("pre")).toContainText("anthropic-fixture");
    const oldResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/ai/preview") &&
        response.request().postDataJSON().provider_id === "openai",
    );
    release();
    await (await oldResponse).finished();
    // Wait through rendering before asserting that the older response was ignored.
    await page.evaluate(
      () =>
        new Promise((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(resolve)),
        ),
    );
    await expect(analysis.locator("pre")).toContainText("anthropic-fixture");
    await analysis.getByRole("button", { name: "Send to AI provider" }).click();
    await expect
      .poll(() => submitted)
      .toMatchObject({
        provider_id: "anthropic",
        preview_digest: "anthropic-digest",
      });
    expect(errors).toEqual([]);
  } finally {
    release();
  }
});

test("older unknown charges can be loaded and reconciled from Settings", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const base = {
    provider_id: "openai",
    investigation_id: "",
    purpose: "test",
    model: "fixture-model",
    state: "failed",
    reason: "Fixture",
    accounting: "settled",
    reserved: 1000,
    charged: 0,
    input_tokens: null,
    output_tokens: null,
    created_at: "2026-10-10T10:00:00Z",
    started_at: "2026-10-10T10:00:00Z",
    finished_at: "2026-10-10T10:01:00Z",
    expires_at: "2099-01-01T00:00:00Z",
    report: null,
  };
  const first = Array.from({ length: 50 }, (_, i) => ({
    ...base,
    id: `newer-${i}`,
  }));
  const older = {
    ...base,
    id: "old-unknown",
    model: "older-unknown-model",
    state: "unknown",
    accounting: "unknown",
  };
  let reconciled: unknown;
  const cursors: string[] = [];
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    let body: unknown = common(url.pathname);
    if (url.pathname === "/api/v1/ai/runs") {
      const cursor = url.searchParams.get("cursor") ?? "";
      cursors.push(cursor);
      body =
        cursor === "newer-49"
          ? { items: [older], next_cursor: "" }
          : { items: first, next_cursor: "newer-49" };
    } else if (url.pathname === "/api/v1/ai/runs/old-unknown/reconcile") {
      reconciled = route.request().postDataJSON();
      older.accounting = "reconciled";
      older.charged = 42;
      body = { ok: true };
    }
    await route.fulfill({ json: body });
  });
  await page.goto("/settings#ai-settings");
  const ai = page.locator(".ai-settings");
  await expect(ai.locator(".ai-history")).toHaveCount(50);
  await ai.getByRole("button", { name: "Load older AI runs" }).click();
  await expect(ai.locator(".ai-history")).toHaveCount(51);
  expect(cursors).toContain("newer-49");
  const old = ai
    .locator(".ai-history")
    .filter({ hasText: "older-unknown-model" });
  await old.locator("summary").click();
  page.once("dialog", (d) => d.accept("0.000042"));
  await old
    .getByRole("button", { name: "Reconcile verified provider charge" })
    .click();
  await expect(old).toContainText("Accounted $0.000042");
  expect(reconciled).toEqual({ confirm: true, cost: 42 });
  await expect(ai.locator(".ai-history")).toHaveCount(51);
  await expect(
    ai.getByRole("button", { name: "Load older AI runs" }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await old.screenshot({ path: "/tmp/sherlock-ai-history-review-mobile.png" });
  expect(errors).toEqual([]);
});
