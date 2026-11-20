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
  await page.getByRole("button", { name: /add (external|remote) server|connect to server/i }).first().click();
  await page.getByLabel(/name/i).first().fill("Second");
  await page.getByLabel(/address/i).first().fill(SECOND);

  const before = firstHits.length;
  await page.getByRole("button", { name: /^(add|connect|save)/i }).last().click();

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
  const secondBefore = secondHits.length;
  await page.locator("aside").getByText("This Server", { exact: true }).first().click();
  await expect.poll(() => reads(firstHits).length, { timeout: 10_000 }).toBeGreaterThan(0);
  expect(reads(secondHits.slice(secondBefore))).toEqual([]);

  // A -> B: activating the external profile moves them back.
  const firstBefore = firstHits.length;
  const secondAgain = secondHits.length;
  await page.locator("aside").getByText("Second", { exact: true }).first().click();
  await expect.poll(() => reads(secondHits.slice(secondAgain)).length, { timeout: 10_000 }).toBeGreaterThan(0);
  expect(reads(firstHits.slice(firstBefore))).toEqual([]);
});
