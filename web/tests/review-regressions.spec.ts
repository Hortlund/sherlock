import { test, expect } from "@playwright/test";

const session = {
  user: { id: "admin", email: "owner@example.com" },
  workspace: { id: "workspace", name: "Review regression checks" },
  csrf_token: "test-csrf",
  expires_at: "2099-01-01T00:00:00Z",
};
const service = {
  id: "service", name: "Checkout", environment: "test", origin: "live",
  prometheus_id: "metrics", loki_id: "logs", revision: 1, monitoring: false,
  mapping: { labels: { app: "checkout" }, metric: "http_requests_total", error_labels: { status: "500" }, error_pattern: "error", warning_pattern: "warn", store_excerpts: true },
};

for (const terminal of ["completed", "cancelled"]) {
  test(`progress fetches the final ${terminal} event after an earlier poll`, async ({ page }) => {
    let state = "collecting", eventReads = 0;
    const errors: string[] = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.route("**/api/v1/**", async route => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown;
      if (path === "/api/v1/me") body = session;
      else if (path.endsWith("/cancel")) { state = "cancelled"; body = { ok: true }; }
      else if (path.endsWith("/events")) {
        const captured = state;
        eventReads++;
        // Offset the events timer from the run timer. The stale implementation
        // stops the next events poll before the terminal event can be fetched.
        if (eventReads === 1) await new Promise(resolve => setTimeout(resolve, 350));
        body = { items: [{ id: 1, stage: "collecting", message: "Collecting evidence" }, ...(captured === "collecting" ? [] : [{ id: 2, stage: captured, message: `Run ${captured}.` }])] };
      } else if (path === "/api/v1/investigations/run") {
        body = { id: "run", kind: "investigation", service_id: "service", state, snapshot: service, start: "2026-10-10T10:00:00Z", end: "2026-10-10T10:30:00Z", requests: 2, result: {} };
      } else body = { items: [] };
      await route.fulfill({ json: body });
    });
    await page.goto("/investigations?id=run");
    await expect(page).toHaveTitle("Sherlock — Reliability investigations");
    const progress = page.getByRole("list", { name: "Investigation progress" });
    await expect(progress).toContainText("Collecting evidence");
    if (terminal === "cancelled") await page.getByRole("button", { name: "Cancel investigation" }).click();
    else state = "completed";
    await expect(progress).toContainText(`Run ${terminal}.`);
    await expect(page.getByRole("button", { name: "Cancel investigation" })).toHaveCount(0);
    expect(eventReads).toBeGreaterThan(1);
    expect(errors).toEqual([]);
    if (terminal === "cancelled") {
      await page.setViewportSize({ width: 390, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    }
    await page.screenshot({ path: `/tmp/sherlock-review-${terminal}.png`, fullPage: true });
  });
}

test("mapping editor preserves legacy labels and saves independent source selectors", async ({ page }) => {
  let saved: Record<string, any> | undefined;
  const errors: string[] = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.route("**/api/v1/**", async route => {
    const path = new URL(route.request().url()).pathname;
    let body: unknown;
    if (path === "/api/v1/me") body = session;
    else if (route.request().method() === "POST") {
      saved = route.request().postDataJSON();
      body = { ...service, ...saved, revision: 2 };
    } else if (path === "/api/v1/services") body = { items: [{ ...service, ...saved }] };
    else if (path === "/api/v1/connections") body = { items: [
      { id: "metrics", name: "Metrics", kind: "prometheus", enabled: true, origin: "live" },
      { id: "logs", name: "Logs", kind: "loki", enabled: true, origin: "live" },
    ] };
    else body = { items: [] };
    await route.fulfill({ json: body });
  });
  await page.goto("/services");
  await page.getByRole("button", { name: "Edit mapping" }).click();
  await expect(page.getByLabel("Prometheus service labels")).toHaveValue("app=checkout");
  await expect(page.getByLabel("Loki service labels")).toHaveValue("app=checkout");
  await page.getByLabel("Prometheus service labels").fill("job=checkout");
  await page.getByRole("button", { name: "Save mapping" }).click();
  await expect(page.getByRole("button", { name: "Save mapping" })).toHaveCount(0);
  expect(saved?.mapping.prometheus_labels).toEqual({ job: "checkout" });
  expect(saved?.mapping.loki_labels).toEqual({ app: "checkout" });
  await page.getByRole("button", { name: "Edit mapping" }).click();
  await expect(page.getByLabel("Prometheus service labels")).toHaveValue("job=checkout");
  await expect(page.getByLabel("Loki service labels")).toHaveValue("app=checkout");
  expect(errors).toEqual([]);
  await page.screenshot({ path: "/tmp/sherlock-review-selectors.png", fullPage: true });
});

for (const kind of ["prometheus", "loki"] as const) {
  test(`${kind}-only canonical mapping can be edited, saved and reopened`, async ({ page }) => {
    const canonical = (value: any) => ({
      ...value,
      mapping: {
        ...value.mapping,
        ...(kind === "loki"
          ? { prometheus_labels: undefined, metric: "", error_labels: null }
          : { loki_labels: undefined, error_pattern: "", warning_pattern: "" }),
      },
    });
    let current = canonical({
      ...service,
      prometheus_id: kind === "prometheus" ? "metrics" : "",
      loki_id: kind === "loki" ? "logs" : "",
      mapping: { ...service.mapping, labels: undefined, prometheus_labels: { job: "checkout" }, loki_labels: { app: "checkout" } },
    });
    let saves = 0;
    const errors: string[] = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.route("**/api/v1/**", async route => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown;
      if (path === "/api/v1/me") body = session;
      else if (route.request().method() === "POST") {
        saves++;
        current = canonical({ ...current, ...route.request().postDataJSON() });
        body = current;
      } else if (path === "/api/v1/services") body = { items: [current] };
      else if (path === "/api/v1/connections") body = { items: [
        { id: "metrics", name: "Metrics", kind: "prometheus", enabled: true, origin: "live" },
        { id: "logs", name: "Logs", kind: "loki", enabled: true, origin: "live" },
      ] };
      else body = { items: [] };
      await route.fulfill({ json: body });
    });
    await page.goto("/services");
    for (let i = 1; i <= 2; i++) {
      await page.getByRole("button", { name: "Edit mapping" }).click();
      if (kind === "loki") await expect(page.getByLabel("Error response labels")).toHaveValue("");
      else {
        await expect(page.getByLabel("Error log pattern")).toHaveValue("");
        await expect(page.getByLabel("Warning log pattern")).toHaveValue("");
      }
      await page.getByLabel("Service name", { exact: true }).fill(`${kind} renamed ${i}`);
      await page.getByRole("button", { name: "Save mapping" }).click();
      await expect(page.getByRole("button", { name: "Save mapping" })).toHaveCount(0);
      expect(saves).toBe(i);
      expect(current.name).toBe(`${kind} renamed ${i}`);
    }
    expect(current.mapping[kind === "loki" ? "loki_labels" : "prometheus_labels"])
      .toEqual(kind === "loki" ? { app: "checkout" } : { job: "checkout" });
    expect(errors).toEqual([]);
  });
}
