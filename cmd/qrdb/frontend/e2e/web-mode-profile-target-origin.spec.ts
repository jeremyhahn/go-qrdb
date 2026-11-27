/**
 * Web mode: the REST client always targets the active profile's address
 * (#1478, Codex on #1607). Connecting a saved external profile, activating
 * another profile and reloading with an external profile saved as active
 * all go through one target selection, so the UI never labels one server
 * while reads and destructive operations reach another.
 *
 * Uses the guarded web fixture and mocks the unprefixed startup endpoints
 * (/health and friends, which App.initialize's connection.connect requests)
 * of both origins, so the app reaches the shell instead of Connection Error.
 */
import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";
import type { Page, Route } from "@playwright/test";

const SECOND = "https://second-server.test:9443";
const PROFILES_KEY = "qrdb:profiles:v1";
const ACTIVE_KEY = "qrdb:active-profile:v1";
const UNPREFIXED = /^\/(health|healthz|livez|readyz|ready|version)$/;

/** A healthy, unsealed, barrier-initialized answer for any REST path. */
async function healthy(route: Route): Promise<void> {
  const url = new URL(route.request().url());
  let body: unknown = { sealed: false, initialized: true };
  if (url.pathname.includes("database")) body = { databases: [] };
  if (url.pathname.includes("health")) body = { status: "ok" };
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    headers: { "access-control-allow-origin": "*" },
    body: JSON.stringify(body),
  });
}

/** Records and answers every /v1 and unprefixed startup call to origin. */
async function mockServer(page: Page, origin: string, hits: string[]): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      hits.push(new URL(route.request().url()).pathname);
      await healthy(route);
    },
  );
}

/** CORS preflights for the second origin; registered last so it runs first. */
async function allowPreflight(page: Page): Promise<void> {
  await page.route(`${SECOND}/**`, async (route) => {
    if (route.request().method() === "OPTIONS") {
      await route.fulfill({
        status: 204,
        headers: {
          "access-control-allow-origin": "*",
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,PUT,DELETE,OPTIONS",
        },
      });
      return;
    }
    await route.fallback();
  });
}

function pageOriginOf(baseURL: string | undefined): string {
  return new URL(baseURL ?? "http://127.0.0.1:5173").origin;
}

/** Reads (seal probe, database list) among the recorded hits. */
function reads(hits: string[]): string[] {
  return hits.filter((p) => p.includes("seal") || p.includes("barrier") || p.includes("database"));
}

/** Persists the built-in profile plus an external profile for SECOND. */
async function saveProfiles(page: Page, pageOrigin: string, activeId: string): Promise<void> {
  const profiles = [
    {
      id: "self",
      name: "This Server",
      type: "external",
      external: { transport: "rest", address: pageOrigin },
      builtin: true,
      createdAt: "2026-01-01T00:00:00Z",
    },
    {
      id: "second",
      name: "Second",
      type: "external",
      external: { transport: "rest", address: SECOND },
      createdAt: "2026-01-01T00:00:00Z",
    },
  ];
  await page.addInitScript(
    ({ key, activeKey, value, active }) => {
      localStorage.setItem(key, value);
      localStorage.setItem(activeKey, active);
    },
    { key: PROFILES_KEY, activeKey: ACTIVE_KEY, value: JSON.stringify(profiles), active: activeId },
  );
}

test("a saved external profile's reads go to its own address", async ({ page, baseURL }) => {
  const pageOrigin = pageOriginOf(baseURL);
  const firstHits: string[] = [];
  const secondHits: string[] = [];
  await mockServer(page, pageOrigin, firstHits);
  await mockServer(page, SECOND, secondHits);
  await allowPreflight(page);

  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: 10_000 });
  await page.locator("aside").getByTitle("Add server").first().click();
  const modal = page.locator(".modal-box");
  await expect(modal).toBeVisible({ timeout: 10_000 });
  await modal.getByRole("button", { name: "Server", exact: true }).click();
  const form = modal.locator(".card", { has: page.getByRole("heading", { name: "Connect to Server" }) });
  await expect(form).toBeVisible({ timeout: 10_000 });
  await modal.locator("#ext-name").fill("Second");
  await modal.locator("#ext-transport").selectOption("rest");
  await modal.locator("#ext-address").fill(SECOND);

  const before = firstHits.length;
  await form.getByRole("button", { name: "Connect", exact: true }).click();

  // The probe (seal status) and the follow-up reads reach the second server.
  await expect.poll(() => reads(secondHits).length, { timeout: 10_000 }).toBeGreaterThan(0);
  // Nothing issued after the switch went to the page origin.
  expect(reads(firstHits.slice(before))).toEqual([]);
});

test("reloading with an external profile saved as active targets that server", async ({ page, baseURL }) => {
  const pageOrigin = pageOriginOf(baseURL);
  const firstHits: string[] = [];
  const secondHits: string[] = [];
  await mockServer(page, pageOrigin, firstHits);
  await mockServer(page, SECOND, secondHits);
  await allowPreflight(page);
  await saveProfiles(page, pageOrigin, "second");

  await page.goto("/");

  // Startup connects to the saved active profile's server, not the origin.
  await expect.poll(() => reads(secondHits).length, { timeout: 10_000 }).toBeGreaterThan(0);
  expect(reads(firstHits)).toEqual([]);
});

test("activating another profile retargets the REST client and back", async ({ page, baseURL }) => {
  const pageOrigin = pageOriginOf(baseURL);
  const firstHits: string[] = [];
  const secondHits: string[] = [];
  await mockServer(page, pageOrigin, firstHits);
  await mockServer(page, SECOND, secondHits);
  await allowPreflight(page);
  await saveProfiles(page, pageOrigin, "second");

  await page.goto("/");
  await expect.poll(() => reads(secondHits).length, { timeout: 10_000 }).toBeGreaterThan(0);

  // B -> A: activating the built-in profile moves reads to the page origin.
  await page.locator("aside").getByText("This Server", { exact: true }).first().click();
  await expect(page.locator(".breadcrumbs").getByText("This Server")).toBeVisible({ timeout: 10_000 });
  await expect.poll(() => reads(firstHits).length, { timeout: 10_000 }).toBeGreaterThan(0);
  // Requests B received before the switch completed are not counted.
  const secondBefore = secondHits.length;
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: 10_000 });
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
  expect(reads(secondHits.slice(secondBefore))).toEqual([]);

  // A -> B: activating the external profile moves them back.
  const firstBefore = firstHits.length;
  const secondAgain = secondHits.length;
  await page.locator("aside").getByText("Second", { exact: true }).first().click();
  await expect.poll(() => reads(secondHits.slice(secondAgain)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  expect(reads(firstHits.slice(firstBefore))).toEqual([]);
});

test("an unavailable saved server still reaches the profile picker", async ({ page, baseURL }) => {
  const pageOrigin = pageOriginOf(baseURL);
  const firstHits: string[] = [];
  await mockServer(page, pageOrigin, firstHits);
  // The saved active server is down: every request to it fails.
  await page.route(`${SECOND}/**`, (route) => route.abort("connectionrefused"));
  await saveProfiles(page, pageOrigin, "second");

  await page.goto("/");

  // The app shell with the profile picker loads instead of the startup
  // error screen that could only retry the same server.
  const aside = page.locator("aside");
  await expect(aside.getByText("This Server", { exact: true }).first()).toBeVisible({ timeout: 10_000 });
  await expect(aside.getByText("Second", { exact: true }).first()).toBeVisible();

  // Select This Server: reads move to the hosting origin.
  await aside.getByText("This Server", { exact: true }).first().click();
  await expect.poll(() => reads(firstHits).length, { timeout: 10_000 }).toBeGreaterThan(0);
  await expect
    .poll(() => page.evaluate((k) => localStorage.getItem(k), ACTIVE_KEY), { timeout: 10_000 })
    .toBe("self");

  // Remove the failed profile.
  await aside.getByTitle("Add server").click();
  const modal = page.locator(".modal-box");
  await expect(modal).toBeVisible({ timeout: 10_000 });
  const card = modal.locator(".card", { has: page.getByText("Second", { exact: true }) });
  await card.getByTitle("Delete profile").click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(card).toHaveCount(0, { timeout: 10_000 });
  await expect
    .poll(() => page.evaluate((k) => localStorage.getItem(k) ?? "", PROFILES_KEY), { timeout: 10_000 })
    .not.toContain(SECOND);
});

/** A one-family Prometheus exposition. */
function exposition(name: string): string {
  return `# HELP ${name} Test family.\n# TYPE ${name} gauge\n${name} 1\n`;
}

async function answerMetrics(route: Route, name: string): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: "text/plain; version=0.0.4",
    headers: { "access-control-allow-origin": "*" },
    body: exposition(name),
  });
}

/** A gate the test opens to release a held response. */
function gate(): { wait: Promise<void>; open: () => void } {
  let open = (): void => {};
  const wait = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { wait, open };
}

test("switching servers on the Dashboard reloads metrics and drops the previous server's", async ({
  page,
  baseURL,
}) => {
  const pageOrigin = pageOriginOf(baseURL);
  const firstHits: string[] = [];
  const secondHits: string[] = [];
  const A_FAMILY = "server_a_family";
  const B_FAMILY = "server_b_family";

  // Server A answers its first metrics read at once and holds the second.
  let aCalls = 0;
  const aLate = gate();
  let aLateDone: Promise<void> | null = null;
  await page.route(
    (url) => url.origin === pageOrigin && url.pathname === "/metrics",
    async (route) => {
      aCalls += 1;
      if (aCalls === 1) {
        await answerMetrics(route, A_FAMILY);
        return;
      }
      aLateDone = aLate.wait.then(() => answerMetrics(route, A_FAMILY));
      await aLateDone;
    },
  );
  // Server B holds its first metrics read and answers the rest at once.
  let bCalls = 0;
  const bFirst = gate();
  await page.route(
    (url) => url.origin === SECOND && url.pathname === "/metrics",
    async (route) => {
      if (route.request().method() === "OPTIONS") {
        await route.fallback();
        return;
      }
      bCalls += 1;
      if (bCalls === 1) await bFirst.wait;
      await answerMetrics(route, B_FAMILY);
    },
  );
  await mockServer(page, pageOrigin, firstHits);
  await mockServer(page, SECOND, secondHits);
  await allowPreflight(page);
  await saveProfiles(page, pageOrigin, "self");

  const family = (name: string) => page.locator(`[data-testid="metrics-family"][data-family="${name}"]`);
  const aside = page.locator("aside");

  await page.goto("/");
  await expect(family(A_FAMILY)).toHaveCount(1, { timeout: 10_000 });

  // A -> B while B's metrics are in flight: A's metrics are gone at once.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect.poll(() => bCalls, { timeout: 10_000 }).toBe(1);
  await expect.poll(() => secondHits.some((p) => p.includes("health")), { timeout: 10_000 }).toBe(true);
  await expect(family(A_FAMILY)).toHaveCount(0, { timeout: 10_000 });
  bFirst.open();
  await expect(family(B_FAMILY)).toHaveCount(1, { timeout: 10_000 });

  // B -> A with A's metrics held, then A -> B: A's late answer is dropped.
  await aside.getByText("This Server", { exact: true }).first().click();
  await expect.poll(() => aCalls, { timeout: 10_000 }).toBe(2);
  await aside.getByText("Second", { exact: true }).first().click();
  await expect.poll(() => bCalls, { timeout: 10_000 }).toBe(2);
  await expect(family(B_FAMILY)).toHaveCount(1, { timeout: 10_000 });
  aLate.open();
  await expect.poll(() => aLateDone !== null, { timeout: 10_000 }).toBe(true);
  await aLateDone;
  // Let the page settle the late response before checking it was dropped.
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
  await expect(family(A_FAMILY)).toHaveCount(0);
  await expect(family(B_FAMILY)).toHaveCount(1);
});
