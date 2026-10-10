import { test, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

const output = "/tmp/sherlock-redesign";
const service = {
  id: "checkout",
  name: "checkout-service",
  environment: "demo",
  origin: "demo",
  revision: 1,
};
const at = "2026-10-10T10:30:00Z";
const report = {
  summary:
    "The mapped signals changed during this window. Underlying cause is undetermined.",
  observations: [
    "Mapped request error proportion was 12.00%, compared with 0.10% in the comparison window.",
    "Error pattern new in comparison window: payment gateway timeout (12 occurrences).",
    "Warning frequency increased during the current window.",
  ].map((text, i) => ({
    text,
    at,
    window: i === 1 ? "comparison" : "current",
    evidence_ids: [`evidence-${i}`],
  })),
  unknowns: [
    "These observations do not establish a root cause.",
    "No deployment, trace, or infrastructure evidence was collected.",
  ],
  checks: [
    "Compare this window with recent deployments.",
    "Check the payment gateway's latency and error rate.",
    "Inspect a sample of affected requests.",
  ],
  adequate: true,
};
async function fixture(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  await mkdir(output, { recursive: true });
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown = { items: [], next_cursor: "" };
    if (path === "/api/v1/me")
      body = {
        user: { id: "admin", email: "owner@example.com" },
        workspace: { id: "workspace", name: "Design test workspace" },
        csrf_token: "fixture",
        expires_at: "2099-01-01T00:00:00Z",
      };
    if (path === "/api/v1/services") body = { items: [service] };
    if (path === "/api/v1/ai/settings")
      body = {
        providers: [],
        policy: { enabled: false, daily_limit: 0, run_limit: 0, revision: 1 },
        spent_today: 0,
        reserved: 0,
        fixture: true,
      };
    if (path === "/api/v1/settings")
      body = {
        cadence_minutes: 5,
        current_minutes: 15,
        baseline_minutes: 60,
        retention_days: 7,
        cooldown_hours: 24,
        min_occurrences: 3,
        min_warnings: 10,
        warning_multiplier: 3,
        min_traffic: 100,
        error_delta: 0.05,
        error_multiplier: 3,
        disabled: [],
      };
    if (path === "/api/v1/system")
      body = { database: "ready", queued: 0, running: 0, failed: 0 };
    if (path === "/api/v1/investigations/run")
      body = {
        id: "run",
        kind: "investigation",
        service_id: service.id,
        snapshot: service,
        state: "completed",
        start: "2026-10-10T10:15:00Z",
        end: at,
        requests: 6,
        result: { report },
      };
    if (path.endsWith("/events"))
      body = {
        items: [
          {
            id: 1,
            stage: "completed",
            message: "Evidence report published.",
            at,
          },
        ],
      };
    if (path.includes("/evidence/"))
      body = {
        id: path.split("/").pop(),
        origin: "demo",
        mapping_revision: 1,
        observation: {
          kind: "request_count",
          value: 1000,
          complete: true,
          start: at,
          end: at,
          query: 'sum(increase(http_requests_total{service="checkout"}[15m]))',
          notes: "Simulated evidence fixture",
        },
      };
    await route.fulfill({ json: body });
  });
  return errors;
}
async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
}

test("settings sections, deep links and keyboard-safe provider drawer", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.goto("/settings#ai-settings");
  const sections = page.getByRole("navigation", { name: "Settings sections" });
  await expect(
    sections.getByRole("link", { name: "AI providers" }),
  ).toHaveAttribute("aria-current", "page");
  await expect(page.getByLabel("Current password")).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "Before you begin" }),
  ).toBeVisible();
  await page.screenshot({
    path: `${output}/settings-desktop.png`,
    fullPage: true,
    animations: "disabled",
  });
  const configure = page.getByRole("button", { name: "Configure Anthropic" });
  await configure.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel("AI provider", { exact: true })).toHaveValue(
    "anthropic",
  );
  await dialog.getByRole("button", { name: "Cancel provider editing" }).focus();
  await page.keyboard.press("Tab");
  await expect(
    dialog.getByRole("button", { name: "Close editor" }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(configure).toBeFocused();
  await sections.getByRole("link", { name: "Monitoring", exact: true }).click();
  await expect(page.getByLabel("Scan cadence (minutes)")).toBeVisible();
  await expect(configure).toHaveCount(0);
  await page.goBack();
  await expect(configure).toBeVisible();
  await page.reload();
  await expect(configure).toBeVisible();
  await page.getByRole("link", { name: "Skip to content" }).focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/#ai-settings$/);
  await expect(page.getByRole("main")).toBeFocused();
  await sections.getByRole("link", { name: "System", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "System health" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("mobile navigation and full-width editors remain reachable", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/settings#ai-settings");
  await expect(
    page.getByRole("button", { name: "Configure OpenAI" }),
  ).toBeVisible();
  const menu = page.getByRole("button", { name: "Open navigation" });
  await menu.click();
  await expect(
    page.getByRole("link", { name: "Services", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(menu).toBeFocused();
  await menu.click();
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("link", { name: "AI providers" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Before you begin" }),
  ).toBeVisible();
  await noOverflow(page);
  await page.screenshot({
    path: `${output}/settings-mobile.png`,
    fullPage: true,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "Configure OpenAI" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("Analysis model ID")).toBeVisible();
  await noOverflow(page);
  await page.screenshot({
    path: `${output}/provider-drawer-mobile.png`,
    animations: "disabled",
  });
  await dialog.getByRole("button", { name: "Cancel provider editing" }).click();
  await expect(dialog).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("investigation views retain context and place evidence beside its observation", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.goto("/investigations?id=run");
  await expect(
    page.getByRole("heading", { name: "What the evidence shows" }),
  ).toBeVisible();
  await page.screenshot({
    path: `${output}/investigation-desktop.png`,
    fullPage: true,
    animations: "disabled",
  });
  const observation = page.locator(".report-observation").first();
  await observation
    .getByRole("button", { name: "Inspect supporting evidence (1)" })
    .click();
  await expect(
    observation.getByRole("heading", { name: "Supporting evidence" }),
  ).toBeVisible();
  await observation.locator("summary").click();
  await expect(observation.locator(".query")).toContainText(
    "http_requests_total",
  );
  await page.getByRole("button", { name: "AI analysis", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "What the evidence shows" }),
  ).not.toBeVisible();
  await expect(page.locator(".ai-analysis")).toBeVisible();
  await page.reload();
  await expect(page.locator(".ai-analysis")).toBeVisible();
  await page.getByRole("button", { name: "Run activity", exact: true }).click();
  await expect(
    page.getByRole("list", { name: "Investigation progress" }),
  ).toContainText("Evidence report published");
  await page.getByRole("button", { name: "Report", exact: true }).click();
  for (const [width, height, name] of [
    [847, 900, "tablet"],
    [390, 844, "mobile"],
  ] as const) {
    await page.setViewportSize({ width, height });
    await noOverflow(page);
    await expect(
      page.getByRole("heading", { name: "What remains unknown" }),
    ).toBeVisible();
    await page.screenshot({
      path: `${output}/investigation-${name}.png`,
      fullPage: true,
      animations: "disabled",
    });
  }
  expect(errors).toEqual([]);
});
