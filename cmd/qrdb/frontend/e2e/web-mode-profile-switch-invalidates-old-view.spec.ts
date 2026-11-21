// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Regression test: switching servers unmounts the old server's workspace
 * before the shared REST client retargets (#1478, Codex on #1607).
 *
 * RestClient.connection.connect marks the client connected once the new
 * server's /health answers and then awaits its /status. A's credential
 * detail, with its Delete button, used to stay mounted in that window while
 * every request already went to B: deleting A's displayed credential could
 * delete B's credential of the same name.
 *
 * Driven through the bundled UI only: the user opens a credential on This
 * Server (A), then clicks the Second server (B) in the sidebar while B's
 * /status response is held. While it is held, A's Delete control is gone
 * and no DELETE reaches B; once B answers, B's own workspace opens.
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

/** Answers every /v1 and unprefixed startup call to origin as a healthy server. */
async function mockServer(page: Page, origin: string): Promise<void> {
  await page.route(
    (url) => url.origin === origin && (url.pathname.startsWith("/v1/") || UNPREFIXED.test(url.pathname)),
    async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown = { sealed: false, initialized: true };
      if (path.includes("database")) body = { databases: [] };
      if (path.includes("health")) body = { status: "ok" };
      await json(route, body);
    },
  );
}

/** Serves one credential, "shared", on origin. */
async function mockCredentials(page: Page, origin: string): Promise<void> {
  await page.route(
    (url) => url.origin === origin && url.pathname.startsWith("/v1/credentials"),
    async (route) => {
      switch (new URL(route.request().url()).pathname) {
        case "/v1/credentials":
          await json(route, { names: ["shared"] });
          return;
        case "/v1/credentials/strategy":
          await json(route, { strategy: "manual" });
          return;
        case "/v1/credentials/shared/versions":
          await json(route, {
            name: "shared",
            versions: [{ version: 1, is_current: true, created_at_unix: 1_700_000_000 }],
          });
          return;
        default:
          await json(route, {});
      }
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

test("A's credential Delete control is gone while B's /status is held, and no DELETE reaches B", async ({
  page,
  baseURL,
}) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const secondRequests: string[] = [];

  // B holds its /status (RestClient.connect's second request) until released.
  const status = gate();
  let statusRequested = false;
  await page.route(
    (url) => url.origin === SECOND && url.pathname === "/status",
    async (route) => {
      if (route.request().method() === "OPTIONS") {
        await route.fallback();
        return;
      }
      statusRequested = true;
      await status.wait;
      await json(route, { nodeId: "second-node", status: "ok" });
    },
  );
  await mockServer(page, pageOrigin);
  await mockServer(page, SECOND);
  await mockCredentials(page, pageOrigin);
  await mockCredentials(page, SECOND);
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

  // A: open the credential's detail panel with its Delete control.
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const aside = page.locator("aside");
  await aside.getByText("Credentials", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Credentials")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("table tbody tr").first().click();
  const deleteButton = page.getByRole("button", { name: "Delete", exact: true });
  await expect(deleteButton).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Switch to B; the client already targets B once B's /health answers.
  await aside.getByText("Second", { exact: true }).first().click();
  await expect.poll(() => statusRequested, { timeout: LOAD_TIMEOUT }).toBe(true);
  expect(secondRequests.some((r) => r.endsWith(" /health"))).toBe(true);

  // While B's /status is held, A's workspace is unmounted: no Delete control,
  // no credential table, and nothing destructive reaches B.
  await expect(page.getByTestId("workspace-not-connected")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(deleteButton).toHaveCount(0);
  await expect(page.locator("table tbody tr")).toHaveCount(0);
  expect(secondRequests.filter((r) => r.startsWith("DELETE "))).toEqual([]);

  // B answers: its own workspace opens, still without any DELETE sent to it.
  status.open();
  await expect(page.getByTestId("workspace-not-connected")).toHaveCount(0, { timeout: LOAD_TIMEOUT });
  await expect(page.locator(".breadcrumbs").getByText("Second")).toBeVisible({ timeout: LOAD_TIMEOUT });
  expect(secondRequests.filter((r) => r.startsWith("DELETE "))).toEqual([]);
});
