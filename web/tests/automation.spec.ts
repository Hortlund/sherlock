import { test, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";

const service = {
  id: "checkout",
  name: "checkout-service",
  environment: "demo",
  origin: "demo",
  revision: 1,
  monitoring: true,
};
const at = "2026-10-11T10:00:00Z";
async function fixture(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  let policy = {
    mode: "off",
    services: [] as string[],
    allow_demo: false,
    cooldown_minutes: 30,
    growth_multiplier: 2,
    revision: 0,
  };
  const activity = (id: string, state: string) => ({
    id,
    service_id: service.id,
    service_name: service.name,
    origin: "demo",
    state,
    reason:
      state === "recommended"
        ? "Investigation recommended. No investigation or AI call was started."
        : "Service recommendation cooldown is active.",
    policy_revision: 1,
    mapping_revision: 1,
    created_at: at,
    due_at: at,
    evaluated_at: at,
    next_eligible_at: at,
    items: [
      {
        finding_id: "old-finding",
        title: "Request errors increased",
        trigger: "growth",
        recommendation: state === "recommended" ? "investigate" : "observe",
        reason:
          state === "recommended"
            ? "Meaningful detector-backed change; manual investigation recommended."
            : "Service recommendation cooldown is active.",
        observed_at: at,
        measurement: {
          value: 0.24,
          unit: "error_proportion",
          minimum_delta: 0.05,
        },
      },
    ],
  });
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    let body: unknown = { items: [], next_cursor: "" };
    switch (url.pathname) {
      case "/api/v1/me":
        body = {
          user: { id: "admin", email: "owner@example.com" },
          workspace: { id: "workspace", name: "Automation test workspace" },
          csrf_token: "fixture",
          expires_at: "2099-01-01T00:00:00Z",
        };
        break;
      case "/api/v1/services":
        body = {
          items: [service, { ...service, id: "other", name: "other-service" }],
        };
        break;
      case "/api/v1/automation/policy":
        if (route.request().method() === "POST") {
          expect(route.request().headers()["x-csrf-token"]).toBe("fixture");
          policy = {
            ...route.request().postDataJSON(),
            revision: policy.revision + 1,
          };
        }
        body = policy;
        break;
      case "/api/v1/automation/activity":
        body =
          url.searchParams.get("service_id") === "other"
            ? { items: [], next_cursor: "" }
            : url.searchParams.get("cursor")
              ? { items: [activity("older", "skipped")], next_cursor: "" }
              : {
                  items: [activity("latest", "recommended")],
                  next_cursor: "latest",
                };
        break;
      case "/api/v1/findings":
        if (url.searchParams.get("id")) {
          expect(url.searchParams.get("id")).toBe("old-finding");
          expect(url.searchParams.get("state")).toBe("");
          expect(url.searchParams.get("cursor")).toBe("");
        }
        body = {
          items: [
            {
              id: "old-finding",
              service_id: service.id,
              origin: "demo",
              state: "acknowledged",
              severity: "warning",
              title: "Request errors increased",
              description: "Stored finding reached from proactive history.",
              details: {
                current: 0.24,
                baseline: 0.01,
                evidence_ids: [],
                thresholds: {},
              },
              first_seen: at,
              last_seen: at,
              snoozed_until: null,
              feedback: "",
            },
          ],
          next_cursor:
            url.searchParams.get("id") || url.searchParams.get("cursor")
              ? ""
              : "older-findings",
        };
        if (url.searchParams.get("state") === "resolved")
          body = { items: [], next_cursor: "" };
        break;
    }
    await route.fulfill({ json: body });
  });
  await mkdir("/tmp/sherlock-automation", { recursive: true });
  return errors;
}

test("recommendation settings persist, history pages and links reach the finding", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.goto("/settings#automation");
  await expect(page).toHaveTitle(/Sherlock/i);
  await expect(
    page.getByRole("heading", { name: "Automation", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Mode", exact: true })
    .selectOption("triage_only");
  await page
    .getByRole("checkbox", { name: "checkout-service · Simulated" })
    .check();
  await page.getByLabel("Include simulated demo findings").check();
  await page.getByLabel("Recommendation cooldown (minutes)").fill("45");
  await page.getByLabel("Minimum growth multiplier").fill("3");
  await page.getByRole("button", { name: "Save automation settings" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Automation settings saved",
  );
  await page.reload();
  await expect(
    page.getByRole("combobox", { name: "Mode", exact: true }),
  ).toHaveValue("triage_only");
  await expect(
    page.getByLabel("Recommendation cooldown (minutes)"),
  ).toHaveValue("45");
  await expect(page.getByLabel("Minimum growth multiplier")).toHaveValue("3");
  await expect(
    page.getByRole("checkbox", { name: "checkout-service · Simulated" }),
  ).toBeChecked();
  await page.locator(".automation-entry summary").click();
  await expect(page.getByText("24.00% errors", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Load older activity" }).click();
  await expect(page.locator(".automation-entry")).toHaveCount(2);
  await page.locator(".automation-entry summary").last().click();
  await expect(page.locator(".automation-entry").last()).toContainText(
    "cooldown is active",
  );
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "/tmp/sherlock-automation/desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.getByLabel("Filter by service").selectOption("other");
  await expect(
    page.getByRole("heading", { name: "No proactive activity yet" }),
  ).toBeVisible();
  await page.getByLabel("Filter by service").selectOption("checkout");
  await page.locator(".automation-entry summary").click();
  await page.getByRole("link", { name: "Request errors increased" }).click();
  await expect(page).toHaveURL(/\/findings\?id=old-finding$/);
  await expect(
    page.getByText("Stored finding reached from proactive history."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Hide evidence" }),
  ).toBeVisible();
  expect(await page.locator("vite-error-overlay").count()).toBe(0);
  expect(errors).toEqual([]);
});

test("automation mobile controls and expanded history fit the viewport", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/settings#automation");
  await expect(
    page.getByRole("heading", { name: "Automation", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Mode", exact: true })
    .selectOption("triage_only");
  await page.getByRole("button", { name: "Save automation settings" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Automation settings saved",
  );
  await page
    .getByRole("combobox", { name: "Mode", exact: true })
    .selectOption("off");
  await Promise.all([
    page.waitForResponse(
      (r) =>
        r.url().endsWith("/automation/policy") &&
        r.request().method() === "POST",
    ),
    page.getByRole("button", { name: "Save automation settings" }).click(),
  ]);
  await page.reload();
  await expect(
    page.getByRole("combobox", { name: "Mode", exact: true }),
  ).toHaveValue("off");
  await page.locator(".automation-entry summary").click();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "/tmp/sherlock-automation/mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await page
    .getByRole("link", { name: "Review monitoring thresholds" })
    .click();
  await expect(page).toHaveURL(/#monitoring$/);
  await page.goBack();
  await expect(
    page.getByRole("heading", { name: "Automation", exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("finding history links ignore retained list filters and cursors", async ({
  page,
}) => {
  const errors = await fixture(page);
  await page.goto("/findings?id=old-finding");
  await expect(
    page.getByRole("heading", { name: "Request errors increased" }),
  ).toBeVisible();
  await page.getByRole("link", { name: "All findings" }).click();
  await page
    .getByRole("combobox", { name: "State", exact: true })
    .selectOption("resolved");
  await expect(
    page.getByRole("heading", { name: "No findings in this view." }),
  ).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/id=old-finding$/);
  await expect(
    page.getByRole("heading", { name: "Request errors increased" }),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "State", exact: true }),
  ).toHaveValue("");
  await expect(
    page.getByRole("combobox", { name: "State", exact: true }),
  ).toBeDisabled();
  await page.goForward();
  await expect(
    page.getByRole("combobox", { name: "State", exact: true }),
  ).toHaveValue("resolved");
  await expect(
    page.getByRole("heading", { name: "No findings in this view." }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "State", exact: true })
    .selectOption("");
  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page.getByRole("button", { name: "First page" })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/id=old-finding$/);
  await expect(
    page.getByRole("heading", { name: "Request errors increased" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "First page" })).toHaveCount(0);
  await page.screenshot({
    path: "/tmp/sherlock-automation/history-finding.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.goForward();
  await expect(page.getByRole("button", { name: "First page" })).toBeVisible();
  expect(errors).toEqual([]);
});
