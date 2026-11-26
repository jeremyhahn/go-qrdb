// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-shamir.spec.ts (real-backend DOM tier)
 *
 * Drives the real UI against a live Shamir-sealed qrdbd to guard:
 *   - The init wizard collects every share holder (count equal to
 *     total_shares) locally before its Next is enabled. The admin enters
 *     each holder from a username, a role and a CSR generated offline; the
 *     one ceremony.initialize call (with the SO PIN and user PIN) enrols
 *     them all and returns each issued certificate, which the admin hands
 *     to the holder out of band.
 *   - After initialization, the completion step shows each holder's
 *     certificate and the claim instructions — no share is ever displayed
 *     in the UI: each holder claims its sealed envelope with its own keys
 *     from the qrdb CLI or the desktop app.
 *   - The "Open Database" button is immediately available.
 *   - Seal Manager shows the Share Holders panel and points a seal to the
 *     holder quorum, with the real M-of-N counts.
 *
 * CSR generation: each test generates a fresh EC P-256 CSR using openssl
 * via a child_process exec, so the spec exercises the real server-side CSR
 * signing path. The private keys are discarded after the test.
 */

import { test, expect, type Page } from "@playwright/test";
import { execSync } from "child_process";
import { mkdtempSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

// ---------------------------------------------------------------------------
// CSR generation helper
// ---------------------------------------------------------------------------

interface GeneratedCSR {
  csrPem: string;
  keyDir: string;
}

/**
 * Generates an EC P-256 private key and a matching CSR using openssl.
 * Returns the CSR PEM and the temp directory (caller must clean up via
 * rmSync(keyDir, { recursive: true, force: true })).
 */
function generateCSR(cn: string): GeneratedCSR {
  const keyDir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  const keyPath = join(keyDir, "key.pem");
  const csrPath = join(keyDir, "csr.pem");
  execSync(`openssl ecparam -name prime256v1 -genkey -noout -out "${keyPath}" 2>/dev/null`);
  execSync(
    `openssl req -new -key "${keyPath}" -out "${csrPath}" -subj "/CN=${cn}" 2>/dev/null`,
  );
  const csrPem = execSync(`cat "${csrPath}"`).toString().trim();
  return { csrPem, keyDir };
}

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

// Wizard defaults: 3-of-5 scheme (request params win over daemon config).
const THRESHOLD = 3;
const TOTAL = 5;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "shamir", shamirThreshold: 2, shamirTotal: 3 });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const SO_PIN = "dom-so-pin-1";
const USER_PIN = "dom-user-pin-1";

/** Opens the wizard and advances past the barrier step in Shamir mode. */
async function toOfficersStep(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: 15_000 });

  // Choose Shamir (wizard defaults to 3-of-5).
  await page.getByText("Shamir (M-of-N)").click();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "Enrol Share Holders" })).toBeVisible({ timeout: 10_000 });
}

/**
 * Fills holder `i`'s form with a fresh CSR. Holders are collected locally:
 * the one ceremony.initialize call enrols them all.
 */
async function fillHolder(page: Page, i: number, csrDirs: string[]): Promise<void> {
  const { csrPem, keyDir } = generateCSR(`officer-${i + 1}`);
  csrDirs.push(keyDir);
  await page.getByTestId(`officer-username-${i}`).fill(`officer-${i + 1}`);
  await page.getByTestId(`officer-csr-${i}`).fill(csrPem);
}

/**
 * Fills every holder, then completes the admin step (generated credential,
 * SO PIN, user PIN) and initializes.
 */
async function initializeShamir(page: Page): Promise<void> {
  await toOfficersStep(page);
  const csrDirs: string[] = [];
  try {
    for (let i = 0; i < TOTAL; i++) {
      await fillHolder(page, i, csrDirs);
    }
  } finally {
    for (const dir of csrDirs) {
      rmSync(dir, { recursive: true, force: true });
    }
  }
  await page.getByTestId("officers-next-btn").click();

  await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: 10_000 });
  await page.getByTestId("admin-mode-generate").check();
  await page.getByTestId("so-pin-input").fill(SO_PIN);
  await page.getByTestId("so-pin-confirm-input").fill(SO_PIN);
  await page.getByTestId("user-pin-input").fill(USER_PIN);
  await page.getByTestId("user-pin-confirm-input").fill(USER_PIN);
  await page.getByTestId("admin-next-btn").click();

  await expect(page.getByRole("heading", { name: "Server Initialized" })).toBeVisible({ timeout: 30_000 });
}

test("shamir: wizard holder step gates Next until every holder is filled in", async ({ page }) => {
  await toOfficersStep(page);

  // Next is disabled: no holder filled in yet; the count reads 0/5.
  const next = page.getByTestId("officers-next-btn");
  await expect(next).toBeDisabled();
  await expect(next).toContainText(`(0/${TOTAL})`);

  const csrDirs: string[] = [];
  try {
    for (let i = 0; i < TOTAL; i++) {
      await fillHolder(page, i, csrDirs);
      if (i < TOTAL - 1) {
        await expect(next).toContainText(`(${i + 1}/${TOTAL})`);
        await expect(next).toBeDisabled();
      }
    }
  } finally {
    for (const dir of csrDirs) {
      rmSync(dir, { recursive: true, force: true });
    }
  }

  // Every holder filled in: Next is enabled.
  await expect(next).toBeEnabled({ timeout: 5_000 });
});

test("shamir: initializing shows each holder's certificate and the claim instructions", async ({ page }) => {
  await initializeShamir(page);

  // NO individual share elements anywhere in the page.
  await expect(page.getByTestId("shamir-share")).toHaveCount(0);

  // Each holder's CA-issued certificate is shown.
  for (let i = 0; i < TOTAL; i++) {
    await expect(page.getByTestId(`holder-cert-officer-${i + 1}`)).toHaveValue(/BEGIN CERTIFICATE/);
  }

  // The alert tells holders to claim with the CLI or the desktop app.
  await expect(page.getByRole("alert").getByText(/qrdb barrier officer claim/)).toBeVisible();

  // "Open Database" is immediately enabled.
  const openBtn = page.getByTestId("open-database-btn");
  await expect(openBtn).toBeEnabled();

  await waitForHealthy(daemon!);
  await openBtn.click();

  // App shell loaded.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
});

test("shamir: after init, Seal Manager shows the Share Holders panel and the holder quorum", async ({ page }) => {
  await initializeShamir(page);

  const openBtn = page.getByTestId("open-database-btn");
  await expect(openBtn).toBeEnabled();
  await waitForHealthy(daemon!);
  await openBtn.click();

  // Navigate to Seal Manager.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // The Share Holders panel lists the five enrolled holders.
  await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible();
  await expect(page.getByTestId("seal-manager-officers-table").locator("tbody tr")).toHaveCount(TOTAL);

  // A single admin cannot seal a Shamir barrier, and the page takes no share:
  // it points to the holder quorum (the quorum seal itself is covered by the
  // real-backend tier, where holders submit with the CLI).
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toHaveCount(0);
  const note = page.getByTestId("seal-quorum-note");
  await expect(note).toContainText(`${THRESHOLD}`);
  await expect(note).toContainText(`${TOTAL} must submit`);
  await expect(note).toContainText("qrdb barrier quorum submit --op seal");
  await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
});
