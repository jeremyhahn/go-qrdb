/**
 * Web mode: connecting a saved external profile points the REST client at
 * that profile's address (#1478, Codex round 1 on #1607). Before the fix
 * connectProfile probed whatever server the client already targeted (the
 * page origin), so a profile for another server showed "connected" while
 * every read still went to the page origin.
 */
import { test, expect, type Page, type Route } from "@playwright/test";

const SECOND = "https://second-server.test:9443";

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

function recordApi(page: Page, origin: string, hits: string[]): Promise<void> {
  return page.route(`${origin}/v1/**`, async (route) => {
    hits.push(new URL(route.request().url()).pathname);
    await healthy(route);
  });
}

test("a saved external profile's reads go to its own address", async ({ page, baseURL }) => {
  const pageOrigin = new URL(baseURL ?? "http://127.0.0.1:5173").origin;
  const firstHits: string[] = [];
  const secondHits: string[] = [];
  await recordApi(page, pageOrigin, firstHits);
  await recordApi(page, SECOND, secondHits);
  // CORS preflights for the second origin.
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

  await page.goto("/");
  await page.getByRole("button", { name: /add (external|remote) server|connect to server/i }).first().click();
  await page.getByLabel(/name/i).first().fill("Second");
  await page.getByLabel(/address/i).first().fill(SECOND);

  const before = firstHits.length;
  await page.getByRole("button", { name: /^(add|connect|save)/i }).last().click();

  // The probe (seal status) and the follow-up reads reach the second server.
  await expect.poll(() => secondHits.length, { timeout: 10_000 }).toBeGreaterThan(0);
  expect(secondHits.some((p) => p.includes("seal") || p.includes("barrier"))).toBe(true);
  // Nothing issued after the switch went to the page origin.
  const afterSwitch = firstHits.slice(before);
  expect(afterSwitch.filter((p) => p.includes("seal") || p.includes("database"))).toEqual([]);
});
