// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Regression test: a bulk delete running when the user switches servers
 * sends no further deletes (#1478, Codex round 1 on #1606).
 *
 * In browser mode every workspace client derives from the shared RestClient,
 * which a profile connect retargets in place. A bulk delete awaiting its
 * first DELETE used to carry on after the switch, so the remaining keys
 * listed on This Server (A) were deleted on the Second server (B).
 *
 * Driven through the bundled UI only: the user selects both of A's keys and
 * deletes them; A holds the first DELETE; the user clicks Second, then A
 * answers. Exactly one DELETE reaches A and none reaches B.
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

/** Answers every /v1 and unprefixed startup call to origin as a healthy server listing databases. */
async function mockServer(page: Page, origin: string, databases: unknown[]): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown = { sealed: false, initialized: true };
      if (path.includes("database")) body = { databases };
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

test("a bulk delete held on A sends nothing to B after a profile switch", async ({ page, baseURL }) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const secondRequests: string[] = [];
  const aDeleteRequests: string[] = [];

  await mockServer(page, pageOrigin, [{ name: "system", backend: "pebble", status: "open" }]);
  await mockServer(page, SECOND, []);
  // A serves two keys and holds its first DELETE until the gate opens.
  const held = gate();
  await page.route(
    (url) => url.origin === pageOrigin && (url.pathname === "/kv" || url.pathname.startsWith("/kv/")),
    async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (request.method() === "DELETE") {
        aDeleteRequests.push(path);
        if (aDeleteRequests.length === 1) await held.wait;
        await route.fulfill({ status: 204, headers: CORS });
        return;
      }
      if (path === "/kv/scan") {
        await json(route, { pairs: { "bulk/a": "MQ==", "bulk/b": "Mg==" } });
        return;
      }
      if (path === "/kv/revision") {
        await json(route, { revision: 7 });
        return;
      }
      await route.fallback();
    },
  );
  // Records every request B receives, then lets the handlers above answer it.
  await page.route(`${SECOND}/**`, async (route) => {
    const request = route.request();
    secondRequests.push(`${request.method()} ${new URL(request.url()).pathname}`);
    await route.fallback();
  });
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

  // A: open the system database's KV explorer and delete both keys.
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const aside = page.locator("aside");
  await aside.getByText("system", { exact: true }).first().click();
  await expect(page.getByText("2 entries")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByLabel("Select row 1", { exact: true }).check();
  await page.getByLabel("Select row 2", { exact: true }).check();
  await page.getByRole("button", { name: "Delete 2" }).click();
  await page.locator(".modal").getByRole("button", { name: "Delete" }).click();
  await expect.poll(() => aDeleteRequests.length, { timeout: LOAD_TIMEOUT }).toBe(1);

  // Switch to B while A's first DELETE is held, then let A answer it.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible({ timeout: LOAD_TIMEOUT });
  held.open();

  // The loop stops instead of sending the second key anywhere.
  await expect(page.getByText(/Bulk delete stopped after the connection changed/)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  expect(aDeleteRequests).toHaveLength(1);
  expect(secondRequests.filter((r) => r.startsWith("DELETE "))).toEqual([]);
});
