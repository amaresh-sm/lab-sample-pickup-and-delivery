import {
  expect,
  test,
  type Page,
} from "@playwright/test";

const username = process.env.ADMIN_USERNAME ?? "";
const password = process.env.ADMIN_PASSWORD ?? "";

async function signIn(page: Page): Promise<void> {
  await page.goto("/");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByTestId("authenticated-user")).toBeVisible();
}

test("public login is keyboard-accessible and does not expose a session", async ({
  page,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Welcome back" }),
  ).toBeVisible();
  await expect(page.getByLabel("Username")).toBeVisible();
  await expect(page.getByLabel("Password")).toHaveAttribute(
    "type",
    "password",
  );
  await expect(page.getByRole("button", { name: "Sign in" })).toBeEnabled();
  await expect(page.getByTestId("authenticated-user")).toHaveCount(0);
});

test("administrator dashboard exposes the deterministic operations controls", async ({
  page,
}) => {
  test.skip(!username || !password, "ADMIN_USERNAME and ADMIN_PASSWORD required");
  await signIn(page);
  await expect(page.getByLabel("Requester ID")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Create collection" }),
  ).toBeVisible();
  await expect(page.getByTestId("request-progress")).toBeVisible();
  await expect(page.getByTestId("work-history")).toBeVisible();
  await expect(page.getByLabel("Assignment ID")).toBeVisible();
  await expect(page.getByLabel("Handoff ID")).toBeVisible();
  await expect(page.getByLabel("Location ID", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Audit query")).toBeVisible();
});

test("live operations map uses only local map assets", async ({
  page,
  baseURL,
}) => {
  test.skip(!username || !password, "ADMIN_USERNAME and ADMIN_PASSWORD required");
  const externalMapRequests: string[] = [];
  const localPMTilesRequests: string[] = [];
  const applicationOrigin = new URL(
    baseURL ?? "http://127.0.0.1:8080",
  ).origin;
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      url.origin === applicationOrigin &&
      url.pathname.toLowerCase().endsWith(".pmtiles")
    ) {
      localPMTilesRequests.push(url.href);
    }
    if (
      url.origin !== applicationOrigin &&
      /(tile|map|sprite|glyph|pmtiles)/i.test(url.pathname)
    ) {
      externalMapRequests.push(url.href);
    }
  });
  await signIn(page);
  await page.getByRole("link", { name: "Live operations" }).click();
  await expect(page.getByTestId("live-map")).toBeVisible();
  await expect(page.locator(".maplibregl-canvas")).toBeVisible();
  await expect(page.locator(".map-error")).toHaveCount(0);
  await expect
    .poll(() =>
      page
        .getByTestId("visible-coordinate")
        .textContent()
        .then((value) => value ?? ""),
    )
    .toContain("{");
  await expect
    .poll(() => localPMTilesRequests.length)
    .toBeGreaterThan(0);
  expect(externalMapRequests).toEqual([]);
});
