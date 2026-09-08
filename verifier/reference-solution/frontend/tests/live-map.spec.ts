/**
 * AC-64: the live map shows routes, locations (including laboratories) and
 * couriers -- proven against a REAL browser and a REAL application.
 *
 * The existing browser coverage only asserts that a MapLibre canvas exists, and
 * the mocked operations payload it uses carries `routes: []` and `workers: []`.
 * This spec instead seeds a live network over the API until
 * `read_operations_state` genuinely returns a non-empty `routes` array and a
 * courier with a recorded location, and then proves all three actually render:
 *
 *   locations  MapLibre's own hit test: clicking the laboratory's projected
 *              screen position opens the popup MapLibre only fires for a
 *              feature it has RENDERED at that pixel.
 *   couriers   the rendered pixels at the courier's projected position are the
 *              courier marker colour, and the coordinate readout carries the
 *              courier's live coordinate.
 *   routes     the rendered pixels on the route line's midpoint are the route
 *              colour, while a control point 30px to the side of the same line
 *              is not -- so the line, not a marker, produced them.
 *
 * Map assets stay local: the spec also fails if the page requests a tile,
 * sprite or glyph from any other origin (AC-83).
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { inflateSync } from "node:zlib";

const username = process.env.ADMIN_USERNAME ?? "";
const password = process.env.ADMIN_PASSWORD ?? "";
const seedPassword = "live-map-password-123456";

// The view LiveMap.vue opens with, and the colours its layers paint.
const MAP_CENTER = { longitude: 77.5946, latitude: 12.9716 };
const MAP_ZOOM = 10.4;
const ROUTE_COLOUR = [0xdf, 0x71, 0x4d] as const; // operations-routes-line
const WORKER_COLOUR = [0x14, 0x7d, 0x70] as const; // operations-points, kind=worker

// Seeded geography, chosen so every feature projects well inside the canvas and
// no two features overlap: the collection point sits at the map centre, the
// laboratory north-east of it, and the courier south-east, off the route line.
const PICKUP = { latitude: 12.9716, longitude: 77.5946 };
const LABORATORY = { latitude: 13.0200, longitude: 77.6400 };
const COURIER_AT = { latitude: 12.9380, longitude: 77.6500 };

type Bitmap = {
  width: number;
  height: number;
  pixel(x: number, y: number): [number, number, number];
};

/** Minimal 8-bit PNG reader, enough for a Playwright element screenshot. */
function decodePng(buffer: Buffer): Bitmap {
  if (buffer.readUInt32BE(0) !== 0x89504e47) throw new Error("not a PNG");
  let offset = 8;
  let width = 0;
  let height = 0;
  let bitDepth = 0;
  let colourType = 0;
  let interlace = 0;
  const parts: Buffer[] = [];
  while (offset + 8 <= buffer.length) {
    const length = buffer.readUInt32BE(offset);
    const type = buffer.toString("ascii", offset + 4, offset + 8);
    const data = buffer.subarray(offset + 8, offset + 8 + length);
    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      bitDepth = data[8];
      colourType = data[9];
      interlace = data[12];
    } else if (type === "IDAT") {
      parts.push(Buffer.from(data));
    } else if (type === "IEND") {
      break;
    }
    offset += 12 + length;
  }
  if (bitDepth !== 8 || interlace !== 0 || (colourType !== 2 && colourType !== 6)) {
    throw new Error(`unsupported PNG: depth ${bitDepth} colour ${colourType} interlace ${interlace}`);
  }
  const channels = colourType === 6 ? 4 : 3;
  const raw = inflateSync(Buffer.concat(parts));
  const stride = width * channels;
  const pixels = Buffer.alloc(height * stride);
  let previous = Buffer.alloc(stride);
  for (let y = 0; y < height; y += 1) {
    const filter = raw[y * (stride + 1)];
    const line = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1) + 0);
    const row = pixels.subarray(y * stride, (y + 1) * stride);
    line.copy(row, 0, 0, stride);
    for (let index = 0; index < stride; index += 1) {
      const left = index >= channels ? row[index - channels] : 0;
      const up = previous[index];
      const upLeft = index >= channels ? previous[index - channels] : 0;
      switch (filter) {
        case 0:
          break;
        case 1:
          row[index] = (row[index] + left) & 0xff;
          break;
        case 2:
          row[index] = (row[index] + up) & 0xff;
          break;
        case 3:
          row[index] = (row[index] + ((left + up) >> 1)) & 0xff;
          break;
        case 4: {
          const predicted = left + up - upLeft;
          const dl = Math.abs(predicted - left);
          const du = Math.abs(predicted - up);
          const dul = Math.abs(predicted - upLeft);
          const paeth = dl <= du && dl <= dul ? left : du <= dul ? up : upLeft;
          row[index] = (row[index] + paeth) & 0xff;
          break;
        }
        default:
          throw new Error(`unsupported PNG filter ${filter}`);
      }
    }
    previous = row;
  }
  return {
    width,
    height,
    pixel(x: number, y: number) {
      const index = y * stride + x * channels;
      return [pixels[index], pixels[index + 1], pixels[index + 2]];
    },
  };
}

/** MapLibre's Web Mercator projection for an unrotated, unpitched view. */
function project(
  point: { latitude: number; longitude: number },
  width: number,
  height: number,
): { x: number; y: number } {
  const worldSize = 512 * 2 ** MAP_ZOOM;
  const toWorld = (value: { latitude: number; longitude: number }) => ({
    x: ((180 + value.longitude) / 360) * worldSize,
    y:
      ((180 -
        (180 / Math.PI) *
          Math.log(Math.tan(Math.PI / 4 + (value.latitude * Math.PI) / 360))) /
        360) *
      worldSize,
  });
  const centre = toWorld(MAP_CENTER);
  const target = toWorld(point);
  return { x: target.x - centre.x + width / 2, y: target.y - centre.y + height / 2 };
}

function countColour(
  bitmap: Bitmap,
  centre: { x: number; y: number },
  radius: number,
  colour: readonly [number, number, number],
  tolerance: number,
): number {
  let matches = 0;
  for (let y = Math.max(0, Math.round(centre.y - radius)); y <= Math.min(bitmap.height - 1, Math.round(centre.y + radius)); y += 1) {
    for (let x = Math.max(0, Math.round(centre.x - radius)); x <= Math.min(bitmap.width - 1, Math.round(centre.x + radius)); x += 1) {
      const [red, green, blue] = bitmap.pixel(x, y);
      if (
        Math.abs(red - colour[0]) <= tolerance &&
        Math.abs(green - colour[1]) <= tolerance &&
        Math.abs(blue - colour[2]) <= tolerance
      ) {
        matches += 1;
      }
    }
  }
  return matches;
}

class Api {
  constructor(
    private readonly request: APIRequestContext,
    private token = "",
  ) {}

  async call(path: string, body: unknown): Promise<any> {
    const response = await this.request.post(`/api/${path}`, {
      data: body,
      headers: this.token ? { Authorization: `Bearer ${this.token}` } : {},
    });
    const payload = await response.json().catch(() => null);
    if (!response.ok()) {
      throw new Error(`${path} -> HTTP ${response.status()} ${JSON.stringify(payload)}`);
    }
    return payload.data;
  }

  async signIn(user: string, secret: string): Promise<Api> {
    const data = await this.call("authenticate-user", { username: user, password: secret });
    const session = new Api(this.request, data.sessionToken);
    if (!data.activeRole && data.roles.length > 0) {
      await session.call("select-active-role", { role: data.roles[0] });
    }
    (session as { userId?: string }).userId = data.userId;
    return session;
  }
}

/**
 * Seed a live network whose operations state has a real route and a real
 * courier position, and return what the browser assertions need.
 */
async function seedLiveNetwork(request: APIRequestContext) {
  const prefix = `map${Date.now()}`;
  const admin = await new Api(request).signIn(username, password);

  // Couriers already in the database would compete for the transport offer, so
  // park them first. Their capacity is passed back unchanged, which keeps any
  // work they already hold reserved.
  const existing = await admin.call("read-operations-state", {});
  for (const worker of existing.workers as any[]) {
    if ((worker.roles ?? []).includes("delivery_partner") && worker.available) {
      await admin.call("set-worker-availability", {
        workerId: worker.workerId,
        available: false,
        capacityUnits: worker.capacityUnits,
      });
    }
  }

  const sampleType = (
    await admin.call("create-sample-type", {
      name: `${prefix} Standard`,
      containerType: "sealed-tube",
      temperatureMinC: 2,
      temperatureMaxC: 8,
      qualifiedCollectorRequired: false,
      incompatibleSampleTypes: [],
    })
  ).sampleTypeId;
  const pickup = (
    await admin.call("create-location", {
      name: `${prefix} collection point`,
      active: true,
      coordinate: PICKUP,
      coverageKm: 5,
      handlingMinutes: 0,
      functions: ["collection_point", "transfer_point"],
    })
  ).locationId;
  const laboratoryName = `${prefix} city laboratory`;
  const laboratory = (
    await admin.call("create-location", {
      name: laboratoryName,
      active: true,
      coordinate: LABORATORY,
      handlingMinutes: 5,
      functions: ["laboratory"],
    })
  ).locationId;
  const connection = (
    await admin.call("create-connection", {
      fromLocationId: pickup,
      toLocationId: laboratory,
      travelMinutes: 40,
      costUnits: 15,
      cooled: true,
      active: true,
    })
  ).connectionId;
  await admin.call("configure-laboratory-acceptance", {
    laboratoryId: laboratory,
    sampleTypeId: sampleType,
    accepts: true,
  });
  for (const resourceId of [pickup, laboratory, connection]) {
    await admin.call("configure-capacity", {
      resourceId,
      capacityUnits: 50,
      cooledCapacityUnits: 25,
    });
  }

  const people: Record<string, string> = {};
  for (const [alias, roles] of [
    ["requester", ["requester"]],
    ["collector", ["collection_professional"]],
    ["courier", ["delivery_partner"]],
    ["lab", ["laboratory_team"]],
  ] as [string, string[]][]) {
    const created = await admin.call("create-user", {
      username: `${prefix}-${alias}`,
      initialPassword: seedPassword,
      email: `${prefix}-${alias}@example.test`,
      fullName: `${prefix} ${alias}`,
      phone: "555-0100",
      roles,
      qualifications: [],
      certifications: [],
      active: true,
    });
    people[alias] = created.userId;
    if (alias !== "requester") {
      await admin.call("set-worker-availability", {
        workerId: created.userId,
        available: true,
        capacityUnits: 20,
      });
    }
  }
  const courierName = `${prefix} courier`;

  const now = new Date();
  const iso = (minutes: number) =>
    new Date(now.getTime() + minutes * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const created = await admin.call("create-collection-request", {
    requesterId: people.requester,
    origin: PICKUP,
    originType: "home",
    originNodeId: pickup,
    requestedAt: iso(-5),
    deliveryDeadline: iso(600),
    earliestCollectionAt: iso(-5),
    latestCollectionAt: iso(240),
    sampleTypeId: sampleType,
    containerType: "sealed-tube",
    temperatureMinC: 2,
    temperatureMaxC: 8,
    incompatibleSampleTypes: [],
    idempotencyKey: `${prefix}-request`,
  });
  const plan = await admin.call("read-journey-plan", { journeyId: created.journeyId });
  const transport = plan.legs.find((leg: any) => leg.workerId === people.courier);
  expect(
    transport,
    `the transport leg was offered to ${JSON.stringify(plan.legs.map((leg: any) => leg.workerId))} rather than the seeded courier`,
  ).toBeTruthy();

  const courier = await new Api(request).signIn(`${prefix}-courier`, seedPassword);
  await courier.call("record-worker-location", {
    assignmentId: transport.assignmentId,
    coordinate: COURIER_AT,
    sequence: 1,
    recordedAt: iso(0),
  });

  const operations = await admin.call("read-operations-state", {});
  return { prefix, laboratory, laboratoryName, courierName, operations, journeyId: created.journeyId };
}

test("live map renders seeded routes, locations and couriers", async ({ page, request, baseURL }) => {
  test.skip(!username || !password, "ADMIN_USERNAME and ADMIN_PASSWORD required");
  test.setTimeout(90_000);

  const seed = await seedLiveNetwork(request);

  // The map data really is non-empty before the browser is involved at all.
  const route = (seed.operations.routes as any[]).find(
    (candidate) => candidate.journeyId === seed.journeyId,
  );
  expect(route, "read_operations_state returned no route for the seeded journey").toBeTruthy();
  const routePoints = (route.coordinates as any[]).map((point) => ({
    latitude: Number(point.latitude),
    longitude: Number(point.longitude),
  }));
  expect(routePoints.length, "the seeded route has fewer than two stops").toBeGreaterThanOrEqual(2);
  const courier = (seed.operations.workers as any[]).find(
    (worker) => worker.fullName === seed.courierName,
  );
  expect(courier, "read_operations_state returned no seeded courier").toBeTruthy();
  expect(courier.latestLocation, "the seeded courier has no recorded location").toBeTruthy();
  expect(courier.locationStale, "the seeded courier location is already stale").toBeFalsy();

  // ------------------------------------------------------------- the browser
  const applicationOrigin = new URL(baseURL ?? "http://127.0.0.1:18082").origin;
  const externalMapRequests: string[] = [];
  page.on("request", (browserRequest) => {
    const url = new URL(browserRequest.url());
    if (url.origin !== applicationOrigin && /(tile|map|sprite|glyph|pmtiles)/i.test(url.pathname)) {
      externalMapRequests.push(url.href);
    }
  });

  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto("/");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByTestId("authenticated-user")).toBeVisible();
  await page.getByRole("link", { name: "Live operations" }).click();

  const canvas = page.locator(".maplibregl-canvas");
  await expect(canvas).toBeVisible();
  await expect(page.locator(".map-error")).toHaveCount(0);

  // The courier's live coordinate reached the client.
  await expect
    .poll(
      () => page.getByTestId("visible-coordinate").textContent().then((value) => value ?? ""),
      { timeout: 20_000 },
    )
    .toContain(String(courier.latestLocation.latitude));

  const box = await canvas.boundingBox();
  expect(box).not.toBeNull();
  const width = box!.width;
  const height = box!.height;

  const laboratoryAt = project(LABORATORY, width, height);
  const courierAt = project(COURIER_AT, width, height);
  const start = project(routePoints[0], width, height);
  const end = project(routePoints[routePoints.length - 1], width, height);
  const midpoint = { x: (start.x + end.x) / 2, y: (start.y + end.y) / 2 };
  const length = Math.hypot(end.x - start.x, end.y - start.y);
  const control = {
    x: midpoint.x + (30 * -(end.y - start.y)) / length,
    y: midpoint.y + (30 * (end.x - start.x)) / length,
  };
  for (const [name, point] of Object.entries({ laboratoryAt, courierAt, midpoint, control })) {
    expect(
      point.x > 12 && point.x < width - 12 && point.y > 12 && point.y < height - 12,
      `${name} projects to ${JSON.stringify(point)}, outside the ${width}x${height} canvas`,
    ).toBe(true);
  }
  expect(length, "the projected route is too short to distinguish from a marker").toBeGreaterThan(80);

  // ------------------------- locations and couriers render (MapLibre hit test)
  // MapLibre only fires a layer click for a feature it has actually RENDERED at
  // that pixel, so the popup is proof the marker is on the map. The click is
  // retried because the first paint of the basemap and layers is asynchronous.
  const clickForPopup = async (point: { x: number; y: number }) => {
    await page.evaluate(() =>
      document.querySelectorAll(".maplibregl-popup").forEach((node) => node.remove()),
    );
    await canvas.click({ position: { x: Math.round(point.x), y: Math.round(point.y) } });
    return page.evaluate(() =>
      Array.from(document.querySelectorAll(".maplibregl-popup"))
        .map((node) => (node as HTMLElement).innerText)
        .join(" | "),
    );
  };

  await expect
    .poll(() => clickForPopup(laboratoryAt), { timeout: 30_000 })
    .toContain(seed.laboratoryName);
  await expect.poll(() => clickForPopup(laboratoryAt), { timeout: 30_000 }).toContain("Laboratory");
  await expect.poll(() => clickForPopup(courierAt), { timeout: 30_000 }).toContain(seed.courierName);

  // ----------------------------------------------- courier and route pixels
  await page.evaluate(() =>
    document.querySelectorAll(".maplibregl-popup").forEach((node) => node.remove()),
  );
  const bitmap = decodePng(await canvas.screenshot());
  const scale = bitmap.width / width;
  const scaled = (point: { x: number; y: number }) => ({ x: point.x * scale, y: point.y * scale });

  const courierPixels = countColour(bitmap, scaled(courierAt), 8 * scale, WORKER_COLOUR, 24);
  const awayFromCourier = countColour(
    bitmap,
    scaled({ x: courierAt.x + 60, y: courierAt.y }),
    8 * scale,
    WORKER_COLOUR,
    24,
  );
  console.log(
    `AC-64 rendered pixels: courier marker=${courierPixels}, 60px away=${awayFromCourier}`,
  );
  expect(
    awayFromCourier,
    "courier-coloured pixels also appear 60px away, so the colour test is not specific",
  ).toBeLessThan(10);
  expect(
    courierPixels,
    `no courier-coloured pixels at the courier's projected position ${JSON.stringify(courierAt)}`,
  ).toBeGreaterThan(40);

  const onRoute = countColour(bitmap, scaled(midpoint), 4 * scale, ROUTE_COLOUR, 40);
  const besideRoute = countColour(bitmap, scaled(control), 4 * scale, ROUTE_COLOUR, 40);
  console.log(
    `AC-64 rendered pixels: route line at midpoint=${onRoute}, 30px beside the line=${besideRoute} ` +
      `(canvas ${bitmap.width}x${bitmap.height}, route length ${Math.round(length)}px)`,
  );
  expect(
    onRoute,
    `no route-coloured pixels on the route line at ${JSON.stringify(midpoint)}`,
  ).toBeGreaterThan(10);
  expect(
    besideRoute,
    `the control point 30px beside the line is also route-coloured (${besideRoute} px), so the ` +
      "match does not prove the line rendered",
  ).toBeLessThan(3);

  expect(externalMapRequests, "the map requested an external host").toEqual([]);
});
