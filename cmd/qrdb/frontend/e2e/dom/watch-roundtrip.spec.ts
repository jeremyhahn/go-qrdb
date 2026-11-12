// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * watch-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Opens a prefix watch through the Watch view on a live daemon, writes a key
 * under that prefix straight through the REST API (not through the UI), and
 * expects the event to stream into the view's Events table. A key outside the
 * prefix must not produce an event.
 */

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const PREFIX = "watched/";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Opens the default (kv-engine) database workspace on its KV Store tab. */
async function openWorkspace(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("a prefix watch streams an event for a key written through the API", async ({
  page,
  request,
}) => {
  await openWorkspace(page);
  await page.getByRole("tab", { name: "Watch", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Watch", exact: true }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText("Waiting for events...")).toBeVisible();

  // --- Open a prefix watch. ----------------------------------------------
  const prefixToggle = page.getByRole("checkbox");
  if (!(await prefixToggle.isChecked())) {
    await prefixToggle.check();
  }
  await page.locator("input[placeholder='Key prefix...']").fill(PREFIX);
  await page.getByRole("button", { name: "Start Watch" }).click();
  await expect(
    page.getByText("Watch started successfully.").last(),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText("1 active watch", { exact: true })).toBeVisible();

  // --- Write through the REST API, outside the UI. ------------------------
  const put = async (key: string, value: string): Promise<void> => {
    const resp = await request.put(
      `${daemon!.adminUrl}/kv/${encodeURIComponent(key)}`,
      {
        data: value,
        ignoreHTTPSErrors: true,
      },
    );
    expect(
      resp.ok(),
      `PUT ${key}: ${resp.status()} ${await resp.text()}`,
    ).toBeTruthy();
  };
  await put("unwatched/key", "ignored");
  await put(`${PREFIX}k1`, "hello-watch");

  // --- The event streams into the Events table. ---------------------------
  await expect(page.getByText(`${PREFIX}k1`).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText("Waiting for events...")).toHaveCount(0);
  await expect(page.getByText("unwatched/key")).toHaveCount(0);
});
