import { test, expect } from "@playwright/test";
import { mkdir } from "node:fs/promises";

test.describe.configure({ mode: "serial" });

test("secure setup, persisted session, login, and password change on desktop and mobile", async ({
  page,
}) => {
  const runtimeErrors: string[] = [];
  page.on("pageerror", (e) => runtimeErrors.push(e.message));
  const output = "/tmp/sherlock-foundation-qa";
  await mkdir(output, { recursive: true });
  await page.goto("/");
  await expect(page).toHaveTitle("Sherlock — Reliability investigations");
  await expect(
    page.getByRole("heading", { name: "Create your workspace" }),
  ).toBeVisible();
  await page.screenshot({
    path: `${output}/setup-desktop.png`,
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("button", { name: "Create workspace" }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: `${output}/setup-mobile.png`,
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1536, height: 1024 });
  await page.getByLabel("Setup token").fill("incorrect-setup-token");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("initial e2e password");
  await page.getByRole("button", { name: "Create workspace" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "setup token is incorrect",
  );
  await page
    .getByLabel("Setup token")
    .fill("local-e2e-setup-token-not-for-real-deployments");
  await page.getByRole("button", { name: "Create workspace" }).click();
  await expect(
    page.getByRole("heading", { name: "Your workspace is ready." }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Overview", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: `${output}/workspace-desktop.png`,
    fullPage: true,
    animations: "disabled",
  });
  const response = await page.request.get("/api/v1/me");
  const text = await response.text();
  expect(text).not.toContain("initial e2e password");
  expect(text).not.toContain("setup_token");
  expect(text).not.toContain("password_hash");
  const cookie = (await page.context().cookies()).find(
    (c) => c.name === "sherlock_session",
  )!;
  expect(cookie.httpOnly).toBe(true);
  expect(cookie.sameSite).toBe("Strict");
  const denied = await page.request.post("/api/v1/auth/logout", {
    headers: { Origin: "http://127.0.0.1:18080" },
    data: {},
  });
  expect(denied.status()).toBe(403);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(
    page.getByRole("heading", { name: "Welcome back" }),
  ).toBeVisible();
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page.getByLabel("Password", { exact: true }).fill("wrong e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Email or password is incorrect",
  );
  await page
    .getByLabel("Password", { exact: true })
    .fill("initial e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Overview", exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page
    .getByLabel("Current password", { exact: true })
    .fill("initial e2e password");
  await page
    .getByLabel("New password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Update password" }).click();
  await expect(
    page.getByRole("heading", { name: "Password updated" }),
  ).toBeVisible();
  await page.screenshot({
    path: `${output}/password-mobile.png`,
    fullPage: true,
    animations: "disabled",
  });
  await page
    .getByRole("link", { name: "Sign in with your new password" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Welcome back" }),
  ).toBeVisible();
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Overview", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  expect(runtimeErrors).toEqual([]);
});

test("demo sources pass real HTTP adapters and produce inspectable evidence", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Connections", exact: true }).click();
  await page.getByRole("button", { name: "Try the synthetic demo" }).click();
  await expect(
    page.getByRole("heading", { name: "Services", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("checkout-service", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Preview signals" }).click();
  await expect(
    page.getByRole("heading", { name: "Signal preview" }),
  ).toBeVisible();
  await expect(page.getByText("20 log lines", { exact: true })).toBeVisible();
  await page.getByText("log patterns", { exact: true }).click();
  await expect(page.getByText("12 occurrences", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Connections", exact: true }).click();
  const metrics = page.getByRole("row").filter({ hasText: "Demo metrics" });
  await metrics.getByRole("button", { name: "Test", exact: true }).click();
  await expect(metrics.getByText("connected", { exact: true })).toBeVisible();
  const logs = page.getByRole("row").filter({ hasText: "Demo logs" });
  await logs.getByRole("button", { name: "Test", exact: true }).click();
  await expect(logs.getByText("connected", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "Add connection", exact: true })
    .click();
  await page.screenshot({
    path: "/tmp/sherlock-foundation-qa/connections-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await logs.getByRole("button", { name: "Disable", exact: true }).click();
  await expect(logs.getByText("disabled", { exact: true })).toBeVisible();
  await expect(
    logs.getByRole("button", { name: "Test", exact: true }),
  ).toBeDisabled();
  await logs.getByRole("button", { name: "Enable", exact: true }).click();
  await expect(logs.getByText("untested", { exact: true })).toBeVisible();
});

test("scans compare both windows and findings retain lifecycle state", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Services", exact: true }).click();
  await page.getByRole("button", { name: "Run scan", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Scan completed", exact: true }),
  ).toBeVisible({ timeout: 15000 });
  await expect(page.getByText("new exception", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Review findings" }).click();
  await expect(
    page.getByRole("heading", { name: "Request error proportion increased" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      name: "Error pattern new in comparison window",
    }),
  ).toBeVisible();
  const error = page
    .getByRole("article")
    .filter({ hasText: "Request error proportion increased" });
  await error.getByRole("button", { name: "View evidence" }).click();
  await expect(
    error.getByText("Current: 0.120 · Comparison: 0.001"),
  ).toBeVisible();
  await error.getByRole("button", { name: "Acknowledge", exact: true }).click();
  await expect(error.getByText("acknowledged", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "/tmp/sherlock-foundation-qa/findings-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    error.getByRole("button", { name: "Resolve", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "/tmp/sherlock-foundation-qa/findings-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
  await error.getByRole("button", { name: "Resolve", exact: true }).click();
  await expect(error.getByText("resolved", { exact: true })).toBeVisible();
  await page.reload();
  await expect(
    page
      .getByRole("article")
      .filter({ hasText: "Request error proportion increased" })
      .getByText("resolved", { exact: true }),
  ).toBeVisible();
});

test("investigation publishes grounded report with persistent progress and evidence", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Findings", exact: true }).click();
  await page
    .getByRole("article")
    .filter({ hasText: "Error pattern new in comparison window" })
    .getByRole("button", { name: "Investigate", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "What the evidence shows" }),
  ).toBeVisible({ timeout: 15000 });
  await expect(
    page.getByText("Underlying cause is undetermined.", { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByText("Mapped request error proportion was 12.00%", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "What remains unknown" }),
  ).toBeVisible();
  await expect(
    page.getByRole("list", { name: "Investigation progress" }),
  ).toContainText("reporting");
  await page
    .getByRole("button", { name: "Inspect supporting evidence (2)" })
    .last()
    .click();
  await expect(
    page.getByRole("heading", { name: "Supporting evidence" }),
  ).toBeVisible();
  await page.screenshot({
    path: "/tmp/sherlock-foundation-qa/investigation-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "What the evidence shows" }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "/tmp/sherlock-foundation-qa/investigation-mobile.png",
    fullPage: true,
    animations: "disabled",
  });
});

test("seeded overview loads within the local two-second target", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page
    .getByLabel("Password", { exact: true })
    .fill("replacement e2e password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByText("Active findings", { exact: true }),
  ).toBeVisible();
  const times: number[] = [];
  for (let i = 0; i < 20; i++) {
    const begin = Date.now();
    await page.goto("/");
    await expect(
      page.getByText("Active findings", { exact: true }),
    ).toBeVisible();
    times.push(Date.now() - begin);
  }
  times.sort((a, b) => a - b);
  console.log(
    `Seeded overview p95 on this local test run: ${times[18]}ms (20 navigations)`,
  );
  expect(times[18]).toBeLessThan(2000);
  await page.context().storageState({ path: "/tmp/sherlock-foundation-qa/notification-session.json" });
  await page.context().storageState({ path: "/tmp/sherlock-foundation-qa/ai-session.json" });
});
