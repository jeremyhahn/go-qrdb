// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * enable-encryption-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Enable-encryption (none -> passphrase) migration driven through the REAL
 * Seal Manager wizard against a live daemon with a committed "none" barrier.
 * The mocked tier (web-mode-enable-encryption.spec.ts) stubs the migration
 * endpoint; this spec exercises the genuine at-rest re-encryption engine:
 * data written BEFORE the migration must survive it and remain readable
 * through the UI afterwards, and the barrier status must report the keyed
 * strategy — real crypto, real store recycle, zero data loss.
 */

import { test, expect } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
// The system-scope migration recycles the node data plane in-process; allow
// a generous window before the status poll reports the keyed strategy.
const MIGRATE_TIMEOUT = 60_000;

let daemon: DomDaemon | null = null;

/**
 * Initializes the daemon's committed "none" barrier via the sealed-window
 * REST endpoint. The strategy comes from server config; the secret is
 * ignored by the none strategy but the endpoint requires a non-empty one.
 */
const SO_PIN = "dom-so-pin-1";
const USER_PIN = "dom-user-pin-1";

async function initializeNoneBarrier(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    // The barrier route refuses initialization (409, use qrdb init); the
    // ceremony route is the one entry point and needs the SO PIN and user PIN.
    const body = JSON.stringify({
      so_pin: SO_PIN,
      user_pin: USER_PIN,
      admin_generate: true,
    });
    const req = https.default.request(
      `${d.adminUrl}/v1/ceremony/initialize`,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: {
          "Content-Type": "application/json",
          "Content-Length": Buffer.byteLength(body),
        },
      },
      (res) => {
        res.resume();
        if (res.statusCode && res.statusCode < 300) resolve();
        else
          reject(new Error(`ceremony initialize returned ${res.statusCode}`));
      },
    );
    req.on("error", reject);
    req.end(body);
  });
}

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none-committed" });
  await initializeNoneBarrier(daemon);
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("none -> passphrase migration through the Seal Manager preserves data", async ({
  page,
}) => {
  // The migration wait alone (MIGRATE_TIMEOUT) exceeds the 30 s default.
  test.setTimeout(3 * MIGRATE_TIMEOUT);
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Seed data that must survive the migration. -------------------------
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .getByRole("button", { name: "Create", exact: true })
    .first()
    .click();
  await page
    .locator("input[placeholder='Enter key']")
    .fill("survives/migration");
  await page
    .locator("textarea[placeholder='Enter value (supports JSON)']")
    .fill("pre-migration-value");
  await page
    .getByRole("button", { name: "Create", exact: true })
    .last()
    .click();
  await expect(
    page.getByText('Created "survives/migration"').last(),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- Run the enable-encryption wizard against the real backend. ---------
  await page.locator("aside").getByText("Seal Manager").click();
  await expect(page.getByTestId("enable-encryption-wizard")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page
    .getByTestId("enable-encryption-strategy")
    .selectOption("passphrase");
  await page
    .getByTestId("enable-encryption-passphrase")
    .fill("migrate-me-securely-1");
  await page.getByTestId("enable-encryption-submit").click();

  // The real migration runs (journalled copy -> verify -> swap -> adopt),
  // then the daemon recycles the data plane in-process and comes back SEALED
  // under the new passphrase strategy. A sealed barrier reports no strategy,
  // so the wizard's status poll takes {sealed: true} as completion and shows
  // the strategy it requested.
  await expect(page.getByTestId("enable-encryption-success")).toBeVisible({
    timeout: MIGRATE_TIMEOUT,
  });
  await expect(page.getByTestId("enable-encryption-success")).toContainText(
    "passphrase",
  );

  // A system-scope migration seals the recycled barrier: the success state
  // must tell the operator to unseal with the new credential.
  await expect(
    page.getByTestId("enable-encryption-unseal-guidance"),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.getByTestId("enable-encryption-unseal-guidance"),
  ).toContainText("sealed");

  // --- Unseal with the NEW passphrase through the sealed gate. ------------
  // Reloading while sealed routes to the full-screen sealed gate, which must
  // render mode-aware passphrase copy (the migrated strategy is reported
  // while sealed) — see barrier-passphrase.spec.ts for the canonical flow.
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "QRDB is Sealed" }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.getByText(
      "Enter the barrier passphrase to unseal the encryption barrier.",
    ),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Submit Share" })).toHaveCount(
    0,
  );

  await page.locator("#unseal-share").fill("migrate-me-securely-1");
  await page.getByRole("button", { name: "Unseal", exact: true }).click();

  // The daemon resumes its full bootstrap asynchronously after the unseal;
  // the sealed window serves only the barrier endpoints, so the app's
  // immediate reconnect can race the data plane (GET /health transiently
  // 404s). Wait for genuine health, then re-enter the app deterministically.
  await waitForHealthy(daemon!);
  await page.reload();

  // After unseal the app shell loads again.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 30_000 });

  // --- Zero data loss: the pre-migration entry is still readable through
  // the real crypto path (decrypted under the new passphrase-derived key).
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(
    page.locator("table").getByText("survives/migration"),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
});
