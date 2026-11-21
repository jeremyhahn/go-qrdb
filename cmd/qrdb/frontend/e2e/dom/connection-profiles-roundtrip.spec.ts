// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * connection-profiles-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Connection Manager in web mode against a live qrdbd (seal
 * "none", auth off):
 *
 *   save a REST profile for the daemon (connectionManager.saveProfile),
 *   which connects and becomes the active profile (setActive), so the
 *   database list loads (connection.isConnected); disconnect it
 *   (disconnect), reconnect it, delete it (deleteProfile), and check it is
 *   gone after a full page reload.
 */

import { test, expect, type Locator, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const PROFILE_NAME = "E2E Daemon Profile";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon. */
async function openApp(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Opens the Add Server modal holding the Connection Manager. */
async function openManager(page: Page): Promise<Locator> {
  await page.locator("aside").getByTitle("Add server").first().click();
  const modal = page.locator(".modal-open .modal-box");
  await expect(modal.getByRole("heading", { name: "Servers" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  return modal;
}

/** The profile card with the given name. */
function card(modal: Locator, name: string): Locator {
  return modal.locator(".card", { has: modal.page().getByRole("heading", { name, exact: true }) });
}

test("connection profile save, activate, disconnect, reconnect, delete round-trip (UI ↔ live daemon)", async ({ page }) => {
  expect(daemon).not.toBeNull();
  const address = daemon!.adminUrl;

  // --- Save (connects and activates) ----------------------------------------------
  await openApp(page);
  let modal = await openManager(page);
  await modal.getByRole("button", { name: "Server", exact: true }).click();
  await modal.getByPlaceholder("Remote Server").fill(PROFILE_NAME);
  await modal.locator("#ext-transport").selectOption("rest");
  await modal.locator("input[placeholder]").nth(1).fill(address);
  await modal.locator(".card-actions .btn-primary").click();

  // A connected profile closes the modal and becomes the active server: the
  // database list of the daemon loads.
  await expect(page.locator(".modal-open")).toHaveCount(0, { timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("banner").getByText(PROFILE_NAME, { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator("aside").getByText("system", { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Persisted: after a reload the profile is listed, connected and active.
  await openApp(page);
  await expect(page.locator("aside").getByText("system", { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  modal = await openManager(page);
  let profile = card(modal, PROFILE_NAME);
  await expect(profile).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(profile).toContainText(address);
  await expect(profile.getByText("Connected", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Disconnect ------------------------------------------------------------------
  await profile.getByRole("button", { name: "Disconnect" }).click();
  await expect(profile.getByText("Disconnected", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(profile.getByRole("button", { name: "Disconnect" })).toHaveCount(0);
  await expect(page.getByRole("banner").getByText("Disconnected", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Reconnect -------------------------------------------------------------------
  // A successful connect re-activates the profile and closes the modal.
  await profile.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.locator(".modal-open")).toHaveCount(0, { timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("banner").getByText("Connected", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.locator("aside").getByText("system", { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  modal = await openManager(page);
  profile = card(modal, PROFILE_NAME);
  await expect(profile.getByText("Connected", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- Delete ----------------------------------------------------------------------
  await openApp(page);
  modal = await openManager(page);
  profile = card(modal, PROFILE_NAME);
  await expect(profile).toBeVisible({ timeout: LOAD_TIMEOUT });
  await profile.getByRole("button", { name: "Delete profile" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(card(modal, PROFILE_NAME)).toHaveCount(0, { timeout: LOAD_TIMEOUT });

  // Gone after a reload; the built-in profile remains.
  await openApp(page);
  modal = await openManager(page);
  await expect(card(modal, "This Server")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(card(modal, PROFILE_NAME)).toHaveCount(0);
});
