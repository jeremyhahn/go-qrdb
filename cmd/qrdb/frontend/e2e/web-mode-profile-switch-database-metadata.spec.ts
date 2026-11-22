// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Web mode: one REST client serves every profile, so after a switch the
 * database store still holds the previous server's list until the new
 * server's list arrives. These specs pin that the workspace never mounts a
 * database view from the previous server's metadata for the new active
 * profile (it would show the old server's settings and submit them to the
 * new server's same-named database), and that a list the previous server
 * answers late never replaces the new server's list.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";
import type { Page, Route } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const SECOND = "https://second-server.test:9443";
const PROFILES_KEY = "qrdb:profiles:v1";
const ACTIVE_KEY = "qrdb:active-profile:v1";
const UNPREFIXED = /^\/(health|healthz|livez|readyz|ready|version)$/;
const CORS = { "access-control-allow-origin": "*" };
const LIST = "/v1/databases/list";

function json(route: Route, body: unknown): Promise<void> {
  return route.fulfill({ status: 200, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

/** A gate the test opens to release held responses. */
function gate(): { wait: Promise<void>; open: () => void } {
  let open = (): void => {};
  const wait = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { wait, open };
}

interface ServerMock {
  /** One database the server lists. */
  database: { name: string; backend: string; status: string };
  /** Resolves before each database list answer while set. */
  holdList?: Promise<void>;
}

/**
 * Answers every /v1 and unprefixed startup call to origin as a healthy,
 * unsealed server listing mock.database, and appends "<tag> <METHOD> <path>"
 * for each to the shared ordered log.
 */
async function mockServer(page: Page, origin: string, tag: string, mock: ServerMock, log: string[]): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      log.push(`${tag} ${request.method()} ${path}`);
      if (path === LIST) {
        if (mock.holdList !== undefined) await mock.holdList;
        await json(route, { databases: [mock.database] });
        return;
      }
      let body: unknown = { sealed: false, initialized: true, strategy: "none", hardware_backed: false };
      if (path.includes("database")) body = mock.database;
      if (path.includes("credentials")) body = { names: [], strategy: "manual" };
      if (path.includes("health")) body = { status: "ok" };
      await json(route, body);
    },
  );
}

/** CORS preflights for SECOND; register it last so it runs first. */
async function mockPreflight(page: Page): Promise<void> {
  await page.route(`${SECOND}/**`, async (route) => {
    if (route.request().method() === "OPTIONS") {
      await route.fulfill({
        status: 204,
        headers: {
          ...CORS,
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,PUT,PATCH,DELETE,OPTIONS",
        },
      });
      return;
    }
    await route.fallback();
  });
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

/** Log entries for tag that are not reads (GET) or preflights. */
function writesOf(log: readonly string[], tag: string): string[] {
  return log.filter((e) => e.startsWith(`${tag} `) && !e.startsWith(`${tag} GET `) && !e.startsWith(`${tag} OPTIONS `));
}

test("switching from a database's Settings never shows the old server's metadata for the new server", async ({
  page,
  baseURL,
}) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const log: string[] = [];
  const bList = gate();
  const a: ServerMock = { database: { name: "shared", backend: "alpha-backend", status: "open" } };
  const b: ServerMock = { database: { name: "shared", backend: "beta-backend", status: "open" }, holdList: bList.wait };
  await mockServer(page, pageOrigin, "A", a, log);
  await mockServer(page, SECOND, "B", b, log);
  await mockPreflight(page);
  await saveProfiles(page, pageOrigin);

  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const aside = page.locator("aside");
  const main = page.locator("main");

  // Open A's database Settings: A's metadata is on screen.
  await aside.getByText("shared", { exact: true }).first().click();
  await main.getByText("Settings", { exact: true }).first().click();
  await expect(main.getByText("alpha-backend")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Switch to B while B's database list is held.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect
    .poll(() => log.some((e) => e === `B GET ${LIST}`), { timeout: LOAD_TIMEOUT })
    .toBe(true);
  await expect
    .poll(() => page.evaluate((k) => localStorage.getItem(k), ACTIVE_KEY), { timeout: LOAD_TIMEOUT })
    .toBe("second");
  // Let every ready continuation and render run.
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
  // B is active and its list has not arrived: no database view, A's or B's.
  await expect(main.getByText("alpha-backend")).toHaveCount(0);
  await expect(main.getByText("beta-backend")).toHaveCount(0);
  await expect(page.getByTestId("workspace-not-connected")).toBeVisible();
  expect(writesOf(log, "B")).toEqual([]);

  // Release B's list: B's dashboard shows, and nothing was written to B.
  bList.open();
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("workspace-not-connected")).toHaveCount(0);
  await expect(main.getByText("alpha-backend")).toHaveCount(0);
  expect(writesOf(log, "B")).toEqual([]);
});

test("a database list the previous server answers after a switch never replaces the new server's list", async ({
  page,
  baseURL,
}) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const log: string[] = [];
  const aList = gate();
  const a: ServerMock = { database: { name: "alpha-db", backend: "alpha-backend", status: "open" }, holdList: aList.wait };
  const b: ServerMock = { database: { name: "beta-db", backend: "beta-backend", status: "open" } };
  await mockServer(page, pageOrigin, "A", a, log);
  await mockServer(page, SECOND, "B", b, log);
  await mockPreflight(page);
  await saveProfiles(page, pageOrigin);

  // A's list is held from the first load on.
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect
    .poll(() => log.some((e) => e === `A GET ${LIST}`), { timeout: LOAD_TIMEOUT })
    .toBe(true);
  const aside = page.locator("aside");

  // Switch to B: B's list answers at once.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(aside.getByText("beta-db", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Release A's late list: it is dropped, B's list stays.
  const released = log.length;
  const aAnswered = page.waitForResponse((r) => {
      const url = new URL(r.url());
      return url.origin === pageOrigin && url.pathname === LIST;
    }, { timeout: LOAD_TIMEOUT });
  aList.open();
  await aAnswered;
  // Let the response's continuations and every render run.
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
  await expect(aside.getByText("beta-db", { exact: true })).toBeVisible();
  await expect(aside.getByText("alpha-db", { exact: true })).toHaveCount(0);
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible();
  expect(await page.evaluate((k) => localStorage.getItem(k), ACTIVE_KEY)).toBe("second");
  expect(log.slice(released).filter((e) => e.startsWith("A "))).toEqual([]);
});
