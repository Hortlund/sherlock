import { test, expect } from "@playwright/test";
import { readFile, rm } from "node:fs/promises";

test("AI providers: setup, preview, report, fallback, cancellation and budgets", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  page.on("dialog", (d) => d.accept());
  const sessionPath = "/tmp/sherlock-foundation-qa/ai-session.json";
  const state = JSON.parse(await readFile(sessionPath, "utf8"));
  await page.context().addCookies(state.cookies);
  await rm(sessionPath);
  await page.goto("/settings#ai-settings");
  await expect(page).toHaveTitle("Sherlock — Reliability investigations");
  const settings = page.locator(".ai-settings");
  await expect(
    settings.getByRole("heading", { name: "AI providers", exact: true }),
  ).toBeVisible();
  await expect(settings).toContainText("SIMULATED PROVIDER FIXTURE");
  await settings.getByLabel("Allow external AI processing").check();
  await settings.getByLabel("Daily AI budget (USD)").fill("1");
  await settings.getByLabel("Per-run AI budget (USD)").fill("0.1");
  await settings.getByRole("button", { name: "Save AI policy" }).click();
  const me = await (await page.request.get("/api/v1/me")).json();
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(`/api/v1${path}`, {
      data,
      headers: {
        "X-CSRF-Token": me.csrf_token,
        Origin: "http://127.0.0.1:18080",
      },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const { items: services } = await (
    await page.request.get("/api/v1/services")
  ).json();
  const service = services.find((v: { origin: string }) => v.origin === "demo");
  const providers: Record<string, string> = {};
  for (const kind of ["openai", "anthropic"]) {
    await settings.getByRole("button", { name: "Add AI provider" }).click();
    await settings
      .getByLabel("AI provider", { exact: true })
      .selectOption(kind);
    await settings.getByLabel("Analysis model ID").fill("fixture-success");
    await settings.getByLabel("Provider API key").fill("fixture-only");
    await settings
      .getByLabel("Input price (USD per million tokens)", { exact: true })
      .fill("1");
    await settings
      .getByLabel("Output price (USD per million tokens)", { exact: true })
      .fill("2");
    await settings
      .locator(`input[name="services"][value="${service.id}"]`)
      .check();
    await settings.getByLabel("Allow simulated demo evidence").check();
    await settings.getByLabel("Enable this AI provider").check();
    await settings.getByRole("button", { name: "Save AI provider" }).click();
    await settings
      .getByRole("button", { name: `Test ${kind}`, exact: true })
      .click();
    await expect
      .poll(async () => {
        const data = await (
          await page.request.get("/api/v1/ai/settings")
        ).json();
        const provider = data.providers.find(
          (p: { kind: string }) => p.kind === kind,
        );
        providers[kind] = provider.id;
        return provider.capability;
      })
      .toBe("ready");
    await settings.getByRole("button", { name: "Refresh AI status" }).click();
    await expect(
      settings
        .locator(".ai-provider")
        .filter({ hasText: kind === "openai" ? "OpenAI" : "Anthropic" }),
    ).toContainText("Capability: ready");
  }
  await settings
    .getByRole("button", { name: "Edit openai", exact: true })
    .click();
  await expect(settings.getByLabel("Provider API key")).toHaveValue("");
  await settings
    .getByRole("button", { name: "Cancel provider editing" })
    .click();
  await settings.screenshot({
    path: "/tmp/sherlock-foundation-qa/ai-settings-desktop.png",
  });
  const investigation = await post(
    `/services/${service.id}/investigations`,
    {},
  );
  await page.goto(`/investigations?id=${investigation.id}`);
  await expect(
    page.getByRole("heading", { name: "What the evidence shows" }),
  ).toBeVisible({ timeout: 15000 });
  const analysis = page.locator(".ai-analysis");
  async function send(kind: string) {
    await page
      .getByRole("button", { name: "AI analysis", exact: true })
      .click();
    await analysis
      .getByLabel("Analysis provider")
      .selectOption(providers[kind]);
    await analysis.getByRole("button", { name: "Preview AI payload" }).click();
    await expect(
      analysis.getByRole("heading", { name: "Review data before sending" }),
    ).toBeVisible();
    await expect(analysis.locator("pre")).not.toContainText("fixture-only");
    await expect(analysis.locator("pre")).toContainText("json_schema");
    await analysis.getByRole("button", { name: "Send to AI provider" }).click();
  }
  for (const kind of ["openai", "anthropic"]) {
    await send(kind);
    await expect(analysis.locator(".ai-report")).toContainText(
      "Fixture analysis:",
      { timeout: 15000 },
    );
    await analysis
      .getByRole("button", { name: "Inspect AI evidence (1)", exact: true })
      .click();
    await expect(
      analysis.getByRole("heading", { name: "AI supporting evidence" }),
    ).toBeVisible();
  }
  await analysis.screenshot({
    path: "/tmp/sherlock-foundation-qa/ai-report-desktop.png",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await analysis.screenshot({
    path: "/tmp/sherlock-foundation-qa/ai-report-mobile.png",
  });
  await page.setViewportSize({ width: 1536, height: 1024 });
  // Real API/worker changes; only the outbound transport is simulated in the test binary.
  for (const [model, terminal] of [
    ["fixture-invalid", "fallback"],
    ["fixture-refusal", "refused"],
    ["fixture-wait", "cancelled"],
  ]) {
    const cfg = await (await page.request.get("/api/v1/ai/settings")).json();
    const provider = cfg.providers.find(
      (p: { kind: string }) => p.kind === "openai",
    );
    await post(`/ai/providers/${provider.id}`, {
      provider: { ...provider, model },
    });
    await post(`/ai/providers/${provider.id}/test`, { confirm: true });
    await expect
      .poll(
        async () =>
          (
            await (await page.request.get("/api/v1/ai/settings")).json()
          ).providers.find((p: { kind: string }) => p.kind === "openai")
            .capability,
      )
      .toBe("ready");
    await page.reload();
    await send("openai");
    if (terminal === "cancelled") {
      await expect(analysis.getByRole("status")).toContainText(
        "Provider is analyzing",
        { timeout: 15000 },
      );
      await analysis.locator(".ai-history").first().locator("summary").click();
      await analysis
        .getByRole("button", { name: "Cancel AI analysis" })
        .click();
    }
    await expect(
      analysis.locator(".ai-history").first().locator("summary"),
    ).toContainText(terminal, { timeout: 15000 });
    await expect(analysis).toContainText(
      "The deterministic report remains available in the Report tab.",
    );
    await page.getByRole("button", { name: "Report", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "What the evidence shows" }),
    ).toBeVisible();
  }
  // Reusing an older successful result must display that result, not the latest failed attempt.
  await send("anthropic");
  await expect(analysis.locator(".ai-report")).toContainText(
    "Fixture analysis:",
  );
  const cfg = await (await page.request.get("/api/v1/ai/settings")).json();
  await post("/ai/policy", { ...cfg.policy, daily_limit: 1, run_limit: 1 });
  await page.reload();
  await send("anthropic");
  await expect(analysis.getByRole("alert")).toContainText(
    "AI budget exhausted",
  );
  expect(errors).toEqual([]);
});
