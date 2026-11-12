// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * metrics-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives admin.metrics from the Dashboard's Metrics card against a live qrdbd
 * (seal "none", auth off):
 *
 *   the card lists the real metric families the daemon's GET /metrics
 *   exposes (a go_ or qrdb_ family among them), and the filter narrows the
 *   list to the families whose name, type or help matches.
 */

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

/** A family every Go process exports; the filter must narrow the list to it. */
const FILTER = "go_goroutines";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the admin Dashboard. */
async function openDashboard(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("aside").getByText("Dashboard", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** The family names a Prometheus text exposition declares with # TYPE lines. */
function exposedFamilies(text: string): Set<string> {
  const names = new Set<string>();
  for (const line of text.split("\n")) {
    const m = /^# TYPE (\S+) \S+/.exec(line);
    if (m) names.add(m[1]!);
  }
  return names;
}

test("metrics round-trip (UI ↔ live daemon)", async ({ page, request }) => {
  expect(daemon).not.toBeNull();

  // --- What the daemon exposes. -----------------------------------------------------
  const resp = await request.get(`${daemon!.adminUrl}/metrics`, {
    ignoreHTTPSErrors: true,
  });
  const text = await resp.text();
  expect(resp.status(), `GET /metrics: ${text.slice(0, 300)}`).toBe(200);
  const served = exposedFamilies(text);
  expect(served.has(FILTER), `daemon exposes ${FILTER}`).toBeTruthy();

  // --- The card lists real families. ------------------------------------------------
  await openDashboard(page);
  const families = page.getByTestId("metrics-family");
  await expect(families.first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  const listed = await families.evaluateAll((rows) =>
    rows.map((r) => r.getAttribute("data-family") ?? ""),
  );
  expect(
    listed.some((n) => n.startsWith("go_") || n.startsWith("qrdb_")),
    `go_/qrdb_ family in ${listed.length} rows`,
  ).toBeTruthy();
  for (const name of listed)
    expect(
      served.has(name),
      `listed family ${name} is served by /metrics`,
    ).toBeTruthy();

  // --- The filter narrows the list to matching families. ----------------------------
  await page.getByTestId("metrics-filter").fill(FILTER);
  await expect(families.filter({ hasText: FILTER }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect
    .poll(async () => families.count(), { timeout: LOAD_TIMEOUT })
    .toBeLessThan(listed.length);
  const narrowed = await families.evaluateAll((rows) =>
    rows.map((r) => ({
      name: r.getAttribute("data-family") ?? "",
      text: (r.textContent ?? "").toLowerCase(),
    })),
  );
  expect(narrowed.map((f) => f.name)).toContain(FILTER);
  for (const f of narrowed)
    expect(f.text, `row ${f.name} matches "${FILTER}"`).toContain(FILTER);

  // A filter nothing matches empties the list and says so.
  await page.getByTestId("metrics-filter").fill("no-such-metric-family-e2e");
  await expect(page.getByTestId("metrics-empty")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(families).toHaveCount(0);
});
