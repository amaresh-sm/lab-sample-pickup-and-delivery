import { expect, test, type Page } from "@playwright/test";

async function mockApplication(page: Page): Promise<void> {
  // The role the mocked server currently reports as active for this session.
  let selectedRole = "administrator";
  await page.route("**/map-assets/style.json", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        version: 8,
        glyphs: "/map-assets/fonts/{fontstack}/{range}.pbf",
        sources: {},
        layers: [],
      }),
    }),
  );
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/authenticate-user") {
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            sessionToken: "test-token",
            userId: "admin-user",
            username: "admin",
            fullName: "Operations Admin",
            roles: ["administrator", "dispatcher", "requester"],
            // The server reports which role the session is working under. This
            // multi-role account has been placed in administrator for the test.
            activeRole: selectedRole,
          },
        }),
      });
    }
    // Role selection is server state on the session, so the client switches role
    // by asking the server and adopting only the role it confirms.
    if (path === "/api/select-active-role") {
      const body = route.request().postDataJSON() as { role?: string };
      selectedRole = String(body.role ?? selectedRole);
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            activeRole: selectedRole,
            roles: ["administrator", "dispatcher", "requester"],
          },
        }),
      });
    }
    if (path === "/api/ui/bootstrap") {
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            user: {
              userId: "admin-user",
              username: "admin",
              fullName: "Operations Admin",
              roles: ["administrator", "dispatcher", "requester"],
              activeRole: selectedRole,
              roleSelectionRequired: false,
            },
            operations: {
              samples: [],
              workers: [],
              locations: [],
              routes: [],
              handoffs: [],
              delays: [],
              exceptions: [],
              assignmentCountSpread: 0,
              ineligibleAssignmentCount: 0,
            },
          },
        }),
      });
    }
    if (path === "/api/read-operations-state") {
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            samples: [],
            workers: [],
            locations: [],
            routes: [],
            handoffs: [],
            delays: [],
            exceptions: [],
            assignmentCountSpread: 0,
            ineligibleAssignmentCount: 0,
          },
        }),
      });
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: {} }),
    });
  });
}

test("Vue administrator dashboard preserves deterministic verifier controls", async ({
  page,
}) => {
  await mockApplication(page);
  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByTestId("authenticated-user")).toBeVisible();
  await expect(page.getByLabel("Requester ID", { exact: true })).toHaveCount(1);
  await expect(page.getByTestId("request-progress")).toBeVisible();
  await expect(page.getByTestId("work-history")).toBeVisible();
  await expect(page.getByLabel("Assignment ID", { exact: true })).toHaveCount(1);
  await expect(page.getByLabel("Handoff ID", { exact: true })).toHaveCount(1);
  await expect(page.getByLabel("Location ID", { exact: true })).toHaveCount(1);
  await expect(page.getByLabel("Audit query", { exact: true })).toHaveCount(1);
  await expect(page.getByTestId("live-map")).toBeVisible();
  await expect(page.locator(".maplibregl-canvas")).toBeVisible();
  await expect(page.locator(".map-error")).toHaveCount(0);
});

test("multi-role users switch work context through the server", async ({ page }) => {
  await mockApplication(page);
  // The selection is server state, so the client must ask the server for it
  // rather than remembering a local preference.
  const selections: string[] = [];
  await page.route("**/api/select-active-role", async (route) => {
    const body = route.request().postDataJSON() as { role?: string };
    selections.push(String(body.role ?? ""));
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          activeRole: body.role,
          roles: ["administrator", "dispatcher", "requester"],
        },
      }),
    });
  });

  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByTestId("active-role").selectOption("requester");

  await expect(page.getByRole("link", { name: "Requests" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Live operations" })).toHaveCount(0);
  // No administrator bypass: acting as a requester hides administrator-only
  // navigation even though the account holds the administrator role.
  await expect(page.getByRole("link", { name: "Administration" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Audit trail" })).toHaveCount(0);
  await expect(page.getByText("Requester", { exact: true }).first()).toBeVisible();
  expect(selections).toEqual(["requester"]);
});

test("workflow forms send OpenAPI-valid non-null custody payloads", async ({
  page,
}) => {
  await mockApplication(page);
  const captured: Record<string, Record<string, unknown>> = {};
  for (const path of [
    "/api/ingest-presealed-sample",
    "/api/initiate-handoff",
    "/api/record-handling-checkpoint",
  ]) {
    await page.route(`**${path}`, async (route) => {
      captured[path] = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          data: path === "/api/initiate-handoff"
            ? { handoffId: "00000000-0000-4000-8000-000000000020" }
            : { status: "saved", compliant: true },
        }),
      });
    });
  }

  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("link", { name: "My work" }).click();

  await page.getByLabel("Sample type", { exact: true }).fill(
    "00000000-0000-4000-8000-000000000001",
  );
  await page.getByRole("button", { name: "Register sealed sample" }).click();
  await expect.poll(() => captured["/api/ingest-presealed-sample"]).toBeTruthy();
  expect(captured["/api/ingest-presealed-sample"]).not.toHaveProperty(
    "alreadySealed",
  );
  // The holder is now always the caller/current session user -- there is no
  // separate initialHolderId field for the candidate to fill in, and the
  // backend ignores it entirely (see openapi.contract.json, which has no
  // such property on this request body). The payload still carries the
  // caller's requesterId, which is what the new self-attesting design uses.
  expect(captured["/api/ingest-presealed-sample"]).not.toHaveProperty(
    "initialHolderId",
  );
  expect(captured["/api/ingest-presealed-sample"].requesterId).toBe(
    "admin-user",
  );

  const fromAssignment = page.getByLabel("From assignment");
  const toAssignment = page.getByLabel("To assignment");
  await expect(fromAssignment).toHaveAttribute("required", "");
  await expect(toAssignment).toHaveAttribute("required", "");
  await page.getByLabel("Handoff sample ID").fill(
    "00000000-0000-4000-8000-000000000002",
  );
  await page.getByLabel("Receiver ID").fill(
    "00000000-0000-4000-8000-000000000003",
  );
  await fromAssignment.fill("00000000-0000-4000-8000-000000000004");
  await toAssignment.fill("00000000-0000-4000-8000-000000000005");
  await page.getByRole("button", { name: "Initiate handoff" }).click();
  await expect.poll(() => captured["/api/initiate-handoff"]).toBeTruthy();
  expect(captured["/api/initiate-handoff"].fromAssignmentId).toBe(
    "00000000-0000-4000-8000-000000000004",
  );
  expect(captured["/api/initiate-handoff"].toAssignmentId).toBe(
    "00000000-0000-4000-8000-000000000005",
  );

  const checkpointLocation = page.getByLabel("Checkpoint location ID");
  await expect(checkpointLocation).toHaveAttribute("required", "");
  await page.getByLabel("Checkpoint sample ID").fill(
    "00000000-0000-4000-8000-000000000002",
  );
  await checkpointLocation.fill("00000000-0000-4000-8000-000000000006");
  await page.getByRole("button", { name: "Record handling checkpoint" }).click();
  await expect
    .poll(() => captured["/api/record-handling-checkpoint"])
    .toBeTruthy();
  expect(captured["/api/record-handling-checkpoint"].locationId).toBe(
    "00000000-0000-4000-8000-000000000006",
  );
  expect(captured["/api/record-handling-checkpoint"]).not.toHaveProperty(
    "assignmentId",
  );
});

test("requester reload restores owned samples and ordered route progress", async ({
  page,
}) => {
  await mockApplication(page);
  const sampleId = "00000000-0000-4000-8000-000000000101";
  const journeyId = "00000000-0000-4000-8000-000000000102";
  await page.route("**/api/authenticate-user", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          sessionToken: "requester-token",
          userId: "requester-user",
          username: "requester",
          fullName: "Request Owner",
          roles: ["requester"],
          // The server reports the role the session is working under, and an
          // account holding exactly one role is placed in it at sign-in. The
          // client drives navigation off this, never off a local guess.
          activeRole: "requester",
        },
      }),
    }),
  );
  await page.route("**/api/ui/bootstrap", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          user: {
            userId: "requester-user",
            username: "requester",
            fullName: "Request Owner",
            roles: ["requester"],
            activeRole: "requester",
            roleSelectionRequired: false,
          },
          requesterSamples: [{
            sampleId,
            requestId: "00000000-0000-4000-8000-000000000103",
            status: "in_transit",
            serviceability: "serviceable",
            requestedAt: "2026-07-22T09:00:00Z",
            deliveryDeadline: "2026-07-22T13:00:00Z",
            activeJourneyId: journeyId,
            origin: { latitude: 12.9716, longitude: 77.5946 },
          }],
        },
      }),
    }),
  );
  await page.route(`**/api/read-sample-state/${sampleId}`, (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          sampleId,
          activeJourneyId: journeyId,
          status: "in_transit",
          deliveryDeadline: "2026-07-22T13:00:00Z",
        },
      }),
    }),
  );
  await page.route("**/api/read-sample-lifecycle", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          sampleId,
          activeJourneyId: journeyId,
          status: "in_transit",
          completedLegIds: ["00000000-0000-4000-8000-000000000104"],
          timeline: [],
        },
      }),
    }),
  );
  await page.route("**/api/read-journey-plan", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          journeyId,
          estimatedDeliveryAt: "2026-07-22T12:00:00Z",
          legs: [{}, {}],
          orderedStops: [
            {
              stopId: "00000000-0000-4000-8000-000000000105",
              locationId: "origin",
              stopType: "pickup",
              completedAt: "2026-07-22T09:10:00Z",
            },
            {
              stopId: "00000000-0000-4000-8000-000000000106",
              locationId: "laboratory",
              stopType: "delivery",
              estimatedAt: "2026-07-22T12:00:00Z",
            },
          ],
        },
      }),
    }),
  );

  await page.goto("/");
  await page.getByLabel("Username").fill("requester");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("link", { name: "Requests" }).click();
  await expect(page.getByText("My requests")).toBeVisible();
  await page.getByRole("button", { name: `Track sample ${sampleId}` }).click();
  await expect(page.getByText("1 / 2 legs")).toBeVisible();
  await expect(page.getByText("Ordered journey")).toBeVisible();
  await expect(page.getByText("laboratory", { exact: true })).toBeVisible();
});

test("operations map exposes service coverage and sample holder", async ({
  page,
}) => {
  await mockApplication(page);
  const operations = {
    samples: [{
      sampleId: "sample-map",
      status: "in_transit",
      currentHolderId: "holder-map",
      latestLocation: { latitude: 12.9716, longitude: 77.5946 },
      locationStale: false,
    }],
    workers: [],
    locations: [{
      locationId: "location-map",
      name: "Coverage hub laboratory",
      active: true,
      coverageKm: 5,
      handlingMinutes: 10,
      coordinate: { latitude: 12.99, longitude: 77.62 },
      // A single site providing several functions at once.
      functions: { collection_point: true, hub: true, laboratory: true },
    }],
    routes: [],
    handoffs: [],
    delays: [],
    exceptions: [],
    assignmentCountSpread: 0,
    ineligibleAssignmentCount: 0,
  };
  await page.route("**/api/read-operations-state", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ data: operations }),
    }),
  );
  await page.route("**/api/ui/bootstrap", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          user: {
            userId: "admin-user",
            username: "admin",
            fullName: "Operations Admin",
            roles: ["administrator", "dispatcher", "requester"],
            activeRole: "administrator",
            roleSelectionRequired: false,
          },
          operations,
        },
      }),
    }),
  );

  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("link", { name: "Live operations" }).click();
  await expect(page.getByText("Service coverage")).toBeVisible();
  const canvas = page.locator(".maplibregl-canvas");
  const box = await canvas.boundingBox();
  expect(box).not.toBeNull();
  await canvas.click({
    position: {
      x: Math.floor((box?.width ?? 2) / 2),
      y: Math.floor((box?.height ?? 2) / 2),
    },
  });
  await expect(page.getByText(/holder holder-map/)).toBeVisible();
});

test("sign out remains available at the tablet breakpoint", async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 850 });
  await mockApplication(page);
  await page.goto("/");
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("secret");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
});
