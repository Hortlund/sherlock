import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { createHmac } from "node:crypto";
import { readFile, rm } from "node:fs/promises";

test.use({ actionTimeout: 10000 });

test("notification setup, preview, demo alerts, failed delivery and bounded retry", async ({
  page,
}) => {
  test.setTimeout(60_000);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  let status = 204;
  const received: {
    signature: boolean;
    payload: {
      delivery_id: string;
      events: { id: string; origin: string; summary: string }[];
    };
  }[] = [];
  const secret = "fixture-signing-key-not-for-real-use";
  const receiver = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = Buffer.concat(chunks);
    const signature =
      "v1=" +
      createHmac("sha256", secret)
        .update(String(req.headers["x-sherlock-timestamp"]) + ".")
        .update(body)
        .digest("hex");
    received.push({
      signature: req.headers["x-sherlock-signature"] === signature,
      payload: JSON.parse(body.toString()),
    });
    res.setHeader("Retry-After", "2");
    res.writeHead(status);
    res.end();
  });
  await new Promise<void>((resolve) =>
    receiver.listen(19001, "127.0.0.1", resolve),
  );
  try {
    // Reuse the tested session: foundation already exercises the per-IP auth budget.
    const state = JSON.parse(
      await readFile(
        "/tmp/sherlock-foundation-qa/notification-session.json",
        "utf8",
      ),
    );
    await page.context().addCookies(state.cookies);
    await page.goto("/settings#notifications");
    await page
      .getByRole("button", { name: "Add destination", exact: true })
      .click();
    await page
      .getByLabel("Destination name", { exact: true })
      .fill("Fixture receiver");
    await page
      .getByLabel("Destination type", { exact: true })
      .selectOption("webhook");
    await page
      .getByLabel("Webhook URL", { exact: true })
      .fill("http://127.0.0.1:19001/fixture-secret");
    await page.getByLabel("Signing secret", { exact: true }).fill(secret);
    await page
      .getByLabel("Allow simulated demo alerts", { exact: true })
      .check();
    await page.getByLabel("Send events immediately", { exact: false }).check();
    await page.getByLabel("Enable this destination", { exact: true }).check();
    await page
      .getByRole("button", { name: "Save destination", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText("Destination saved");
    await page
      .getByRole("button", { name: "Preview Fixture receiver", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Synthetic payload preview" }),
    ).toBeVisible();
    await expect(page.locator(".notification-preview")).toContainText(
      "SIMULATED TEST",
    );
    expect(received).toHaveLength(0);
    await page.getByRole("button", { name: "Close preview" }).click();
    await page.getByRole("button", { name: "Edit Fixture receiver" }).click();
    await expect(page.getByLabel("Webhook URL", { exact: true })).toHaveValue(
      "",
    );
    await expect(
      page.getByLabel("Signing secret", { exact: true }),
    ).toHaveValue("");
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await page.getByRole("dialog").screenshot({
      path: "/tmp/sherlock-notifications-mobile.png",
      animations: "disabled",
    });
    await page
      .getByRole("button", { name: "Cancel editing destination" })
      .click();
    await page.setViewportSize({ width: 1536, height: 1024 });
    // Use the existing source adapter with a fresh service, so this scan creates new events.
    const me = await (await page.request.get("/api/v1/me")).json();
    const original = (await (await page.request.get("/api/v1/services")).json())
      .items[0];
    const saved = await page.request.post("/api/v1/services", {
      headers: {
        Origin: "http://127.0.0.1:18080",
        "X-CSRF-Token": me.csrf_token,
      },
      data: {
        name: "Notification demo",
        environment: original.environment,
        prometheus_id: original.prometheus_id,
        loki_id: original.loki_id,
        mapping: original.mapping,
        monitoring: false,
      },
    });
    expect(saved.status()).toBe(200);
    await page.getByRole("link", { name: "Services", exact: true }).click();
    await page.reload(); // Refresh the browser cache after out-of-band fixture creation.
    await page
      .getByRole("row")
      .filter({ hasText: "Notification demo" })
      .getByRole("button", { name: "Run scan", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Scan completed", exact: true }),
    ).toBeVisible({ timeout: 15000 });
    // Per-destination serialization may snooze the second send; River promotes
    // scheduled jobs every five seconds, beyond the default assertion timeout.
    await expect.poll(() => received.length, { timeout: 15000 }).toBe(2);
    expect(
      received.every(
        (r) =>
          r.signature &&
          r.payload.events.every(
            (e) => e.origin === "demo" && e.summary.includes("SIMULATED"),
          ),
      ),
    ).toBeTruthy();
    await page.getByRole("link", { name: "Settings", exact: true }).click();
    await page
      .getByRole("link", { name: "Notifications", exact: true })
      .click();
    const history = page.locator(".notification-history");
    await expect(
      history.locator("summary").filter({ hasText: "sent" }),
    ).toHaveCount(2);
    status = 401;
    page.on("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Send test to Fixture receiver" })
      .click();
    const latest = history.locator("details").first();
    await expect(latest.locator("summary")).toContainText("failed", {
      timeout: 10000,
    });
    await latest.locator("summary").click();
    await expect(latest).toContainText("HTTP 401");
    status = 429;
    await latest.getByRole("button", { name: "Retry delivery" }).click();
    await expect(latest.locator("summary")).toContainText("retrying", {
      timeout: 5000,
    });
    await expect
      .poll(
        () =>
          received.filter((r) =>
            r.payload.events[0].summary.includes("SIMULATED TEST"),
          ).length,
      )
      .toBe(2);
    status = 204;
    await expect(latest.locator("summary")).toContainText("sent", {
      timeout: 20000,
    });
    await expect(latest).toContainText("Attempt 3: acknowledged");
    const testSends = received.filter((r) =>
      r.payload.events[0].summary.includes("SIMULATED TEST"),
    );
    expect(testSends).toHaveLength(3);
    expect(new Set(testSends.map((r) => r.payload.delivery_id)).size).toBe(1);
    expect(testSends.every((r) => r.signature)).toBeTruthy();
    await page.locator("#notifications").screenshot({
      path: "/tmp/sherlock-notifications-desktop.png",
      animations: "disabled",
    });
    await page.getByRole("button", { name: "Delete Fixture receiver" }).click();
    await expect(
      page.getByRole("button", { name: "Send test to Fixture receiver" }),
    ).toHaveCount(0);
    expect(errors).toEqual([]);
  } finally {
    await rm("/tmp/sherlock-foundation-qa/notification-session.json", {
      force: true,
    });
    await new Promise<void>((resolve, reject) =>
      receiver.close((e) => (e ? reject(e) : resolve())),
    );
  }
});
