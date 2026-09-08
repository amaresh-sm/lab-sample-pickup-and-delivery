import { expect, test } from "@playwright/test";

test("renders the Bangalore PMTiles map without external requests", async ({ page }, testInfo) => {
  const browserErrors: string[] = [];
  const blockedRequests: string[] = [];

  page.on("console", (message) => {
    if (message.type() === "error") {
      browserErrors.push(message.text());
    }
  });
  page.on("pageerror", (error) => browserErrors.push(error.message));

  await page.route("**/*", async (route) => {
    const url = route.request().url();
    const allowed =
      url.startsWith("http://127.0.0.1:4173/") ||
      url.startsWith("data:") ||
      url.startsWith("blob:");
    if (!allowed) {
      blockedRequests.push(url);
      await route.abort("blockedbyclient");
      return;
    }
    await route.continue();
  });

  try {
    await page.goto("/", { waitUntil: "domcontentloaded" });
    await expect(page.locator("html")).toHaveAttribute("data-map-ready", "true", {
      timeout: 45_000,
    });
    await expect(page.locator(".maplibregl-canvas")).toBeVisible();
    await expect(page.locator(".sample-point-marker")).toBeVisible();
    await expect(page.locator(".maplibregl-ctrl-attrib-inner")).toContainText(
      "OpenStreetMap contributors",
    );

    const overlayExists = await page.evaluate(() =>
      Boolean(window.arenaMap.getSource("sample-point") && window.arenaMap.getLayer("sample-point")),
    );
    expect(overlayExists).toBe(true);
    expect(blockedRequests).toEqual([]);
    expect(browserErrors).toEqual([]);
  } finally {
    await testInfo.attach("browser-console.log", {
      body: Buffer.from(browserErrors.length > 0 ? browserErrors.join("\n") : "no browser errors\n"),
      contentType: "text/plain",
    });
  }
});
