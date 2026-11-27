// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Web mode: connecting to a sealed remote profile leaves the previous
 * profile disconnected (the one REST client left it). The sealed profile
 * must become the active one and open its Seal Manager, else every view is
 * hidden and the server cannot be unsealed from the UI.
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

/**
 * Answers every /v1 and unprefixed startup call to origin as a healthy
 * server whose barrier is sealed or not.
 */
async function mockServer(page: Page, origin: string, sealed: boolean): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown = { sealed, initialized: true, strategy: sealed ? "shamir" : "none", hardware_backed: false };
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

test("connecting to a sealed profile activates it and opens its Seal Manager", async ({ page, baseURL }) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  await mockServer(page, pageOrigin, false);
  await mockServer(page, SECOND, true);
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

  await page.locator("aside").getByText("Second", { exact: true }).first().click();

  // B is the active profile, persisted and named in the breadcrumb.
  await expect
    .poll(() => page.evaluate((k) => localStorage.getItem(k), ACTIVE_KEY), { timeout: LOAD_TIMEOUT })
    .toBe("second");
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator(".breadcrumbs").getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  // Its Seal Manager is mounted, not the switching empty state.
  await expect(page.getByTestId("workspace-not-connected")).toHaveCount(0);
  await expect(page.locator("main").getByText("Seal Manager", { exact: true }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
});
