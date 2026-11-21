// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Regression test: web-mode profile switches are serialized (#1478, Codex
 * on #1607).
 *
 * RestClient.connection.connect changes the client's baseUrl before it
 * awaits /health. Two overlapping profile Connect actions used to interleave
 * on the shared REST client: when one server's health response arrived
 * after the other had connected, its continuation recorded the wrong
 * profile as the connected target, so the UI labelled one server while
 * reads and destructive operations reached the other.
 *
 * Driven through the bundled UI only: from This Server (A, the page origin)
 * the user clicks Second (B) while B's /health is held, then clicks This
 * Server again. While B's /health is held the second switch must not touch
 * the client (A sees no /health). Once B is released both switches run in
 * order, each against its own address, and the last one wins: the client,
 * the active label and the saved active profile all name This Server.
 */
import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";
import type { Page, Route } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const SECOND = "https://second-server.test:9443";
const PROFILES_KEY = "qrdb:profiles:v1";
const ACTIVE_KEY = "qrdb:active-profile:v1";
const UNPREFIXED = /^\/(health|healthz|livez|readyz|ready|version)$/;
const CORS = { "access-control-allow-origin": "*" };

function json(route: Route, body: unknown): Promise<void> {
  return route.fulfill({ status: 200, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

/** A gate the test opens to release a held response. */
function gate(): { wait: Promise<void>; open: () => void } {
  let open = (): void => {};
  const wait = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { wait, open };
}

/**
 * Answers every /v1 and unprefixed startup call to origin as a healthy
 * server, and appends "<tag> <path>" for each to the shared ordered log.
 */
async function mockServer(page: Page, origin: string, tag: string, log: string[]): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      const path = new URL(route.request().url()).pathname;
      log.push(`${tag} ${path}`);
      let body: unknown = { sealed: false, initialized: true };
      if (path.includes("database")) body = { databases: [] };
      if (path.includes("credentials")) body = { names: [], strategy: "manual" };
      if (path.includes("health")) body = { status: "ok" };
      await json(route, body);
    },
  );
}

/** Persists the built-in profile (A, the page origin) and SECOND (B), A active. */
async function saveProfiles(page: Page, pageOrigin: string): Promise<void> {
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
    ({ key, activeKey, value }) => {
      localStorage.setItem(key, value);
      localStorage.setItem(activeKey, "self");
    },
    { key: PROFILES_KEY, activeKey: ACTIVE_KEY, value: JSON.stringify(profiles) },
  );
}

test("a second switch waits for a held first switch, then each reaches its own address in order", async ({
  page,
  baseURL,
}) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const log: string[] = [];
  const healthOf = (tag: string): number => log.filter((e) => e === `${tag} /health`).length;

  // B holds its first /health (the first switch's connect) until released.
  const firstHealth = gate();
  let secondHealthCalls = 0;
  await page.route(
    (url) => url.origin === SECOND && url.pathname === "/health",
    async (route) => {
      if (route.request().method() === "OPTIONS") {
        await route.fallback();
        return;
      }
      secondHealthCalls += 1;
      if (secondHealthCalls === 1) await firstHealth.wait;
      await route.fallback();
    },
  );
  await mockServer(page, pageOrigin, "A", log);
  await mockServer(page, SECOND, "B", log);
  // CORS preflights for B; registered last so it runs first.
  await page.route(`${SECOND}/**`, async (route) => {
    if (route.request().method() === "OPTIONS") {
      await route.fulfill({
        status: 204,
        headers: {
          ...CORS,
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,PUT,DELETE,OPTIONS",
        },
      });
      return;
    }
    await route.fallback();
  });
  await saveProfiles(page, pageOrigin);

  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const aside = page.locator("aside");

  // First switch: A -> B, held at B's /health.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect.poll(() => secondHealthCalls, { timeout: LOAD_TIMEOUT }).toBe(1);

  // Second switch while the first is held: back to This Server. It must not
  // retarget the shared client until the first has settled.
  const aHealthBefore = healthOf("A");
  await aside.getByText("This Server", { exact: true }).first().click();
  // Let the click's handlers and every ready continuation run.
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
  expect(healthOf("A")).toBe(aHealthBefore);
  // B's held /health is the only connect in flight: no B read has started.
  expect(log.filter((e) => e.startsWith("B ") && e !== "B /health")).toEqual([]);

  // Release B: the first switch completes against B, then the second runs
  // against A, and nothing is sent to B after A's last /health.
  firstHealth.open();
  await expect.poll(() => healthOf("A"), { timeout: LOAD_TIMEOUT }).toBeGreaterThan(aHealthBefore);
  await expect(page.locator(".breadcrumbs").getByText("This Server")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect
    .poll(() => page.evaluate((k) => localStorage.getItem(k), ACTIVE_KEY), { timeout: LOAD_TIMEOUT })
    .toBe("self");
  // Both addresses were connected to, B first: the switches ran in order.
  const firstB = log.indexOf("B /health");
  const lastAHealth = log.lastIndexOf("A /health");
  expect(firstB).toBeGreaterThanOrEqual(0);
  expect(lastAHealth).toBeGreaterThan(firstB);
  // B's first switch probed B itself (its seal status), not A.
  expect(log.slice(firstB).some((e) => e.startsWith("B /v1/"))).toBe(true);
  // The last switch won: the final probe went to A, the header says
  // Connected, and a read the user starts now reaches A, never B.
  await expect
    .poll(() => log.slice(lastAHealth).some((e) => e.startsWith("A /v1/")), { timeout: LOAD_TIMEOUT })
    .toBe(true);
  await expect(page.getByText("Connected", { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  const settled = log.length;
  // This Server is listed first; either server's admin entry only navigates,
  // so the read reaches whatever the client targets.
  await aside.getByText("Credentials", { exact: true }).first().click();
  await expect(page.locator(".breadcrumbs").getByText("Credentials")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect
    .poll(() => log.slice(settled).some((e) => e.startsWith("A /v1/credentials")), { timeout: LOAD_TIMEOUT })
    .toBe(true);
  expect(log.slice(settled).filter((e) => e.startsWith("B "))).toEqual([]);
});
