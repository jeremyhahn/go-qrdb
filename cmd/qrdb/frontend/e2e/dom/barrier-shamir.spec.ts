// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-shamir.spec.ts (real-backend DOM tier)
 *
 * Drives the real UI against a live Shamir-sealed qrdbd to guard:
 *   - The init wizard requires officers to be registered (count equal to
 *     total_shares) BEFORE the Initialize button is enabled. The admin
 *     registers each officer by providing a username and CSR PEM generated
 *     offline; the server returns a signed certificate that the admin
 *     distributes out of band.
 *   - The Initialize button is disabled until all officers are registered,
 *     then enabled.
 *   - After initialization, the completion step shows the Shamir Barrier
 *     Initialized heading with the officer enrollment status panel — shares
 *     are NEVER displayed in the UI (regression guard against the old flow
 *     that returned share bytes; new design routes them through per-officer
 *     claim).
 *   - The "Open Database" button is immediately available.
 *   - Seal Manager shows Shamir unseal copy with the real M-of-N counts.
 *
 * CSR generation: each test generates a fresh EC P-256 CSR using openssl
 * via a child_process exec, so the spec exercises the real server-side CSR
 * signing path. The private keys are discarded after the test.
 */

import { test, expect } from "@playwright/test";
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

test("shamir: wizard officer-registration step gates Initialize button until all officers registered", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: 15_000 });

  // Choose Shamir (wizard defaults to 3-of-5).
  await page.getByText("Shamir (M-of-N)").click();
  await page.getByRole("button", { name: "Next" }).click();

  // Should be on the Officers step now.
  await expect(page.getByRole("heading", { name: "Register Security Officers" })).toBeVisible({ timeout: 10_000 });

  // Initialize button must be disabled — no officers registered yet.
  await expect(page.getByTestId("initialize-btn")).toBeDisabled();

  // Registration count badge shows 0/5.
  await expect(page.getByTestId("officer-registration-count")).toContainText("0 / 5");

  // Generate and register 5 officers.
  const csrDirs: string[] = [];
  try {
    for (let i = 0; i < TOTAL; i++) {
      const { csrPem, keyDir } = generateCSR(`officer-${i + 1}`);
      csrDirs.push(keyDir);

      await page.getByTestId(`officer-username-${i}`).fill(`officer-${i + 1}`);
      await page.getByTestId(`officer-csr-${i}`).fill(csrPem);
      await page.getByTestId(`register-officer-btn-${i}`).click();

      // Wait for the certificate to appear.
      await expect(page.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: 15_000 });

      // Verify count badge increments.
      await expect(page.getByTestId("officer-registration-count")).toContainText(`${i + 1} / ${TOTAL}`);
    }

    // Initialize button must now be enabled.
    await expect(page.getByTestId("initialize-btn")).toBeEnabled({ timeout: 5_000 });
  } finally {
    for (const dir of csrDirs) {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

test("shamir: completing officer registration and initializing shows enrollment panel", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: 15_000 });

  // Choose Shamir (wizard defaults to 3-of-5).
  await page.getByText("Shamir (M-of-N)").click();
  await page.getByRole("button", { name: "Next" }).click();

  // Officers step.
  await expect(page.getByRole("heading", { name: "Register Security Officers" })).toBeVisible({ timeout: 10_000 });

  const csrDirs: string[] = [];
  try {
    for (let i = 0; i < TOTAL; i++) {
      const { csrPem, keyDir } = generateCSR(`officer-${i + 1}`);
      csrDirs.push(keyDir);
      await page.getByTestId(`officer-username-${i}`).fill(`officer-${i + 1}`);
      await page.getByTestId(`officer-csr-${i}`).fill(csrPem);
      await page.getByTestId(`register-officer-btn-${i}`).click();
      await expect(page.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: 15_000 });
    }
  } finally {
    for (const dir of csrDirs) {
      rmSync(dir, { recursive: true, force: true });
    }
  }

  // Click Initialize.
  await page.getByTestId("initialize-btn").click();

  // Completion step: "Shamir Barrier Initialized" heading.
  await expect(
    page.getByRole("heading", { name: "Shamir Barrier Initialized" }),
  ).toBeVisible({ timeout: 20_000 });

  // NO root token at init time (INV-1): root-token generation requires a
  // quorum of Shamir shares, which officers only obtain post-claim. The
  // wizard renders the token block only when a token exists.
  await expect(page.getByTestId("root-token")).toHaveCount(0);

  // NO individual share elements anywhere in the page.
  const shareElements = page.getByTestId("shamir-share");
  await expect(shareElements).toHaveCount(0);

  // Officer claim status table is rendered.
  await expect(page.getByTestId("officer-claim-status-table")).toBeVisible();

  // Alert instructs SOs to navigate to /officer/claim. The claim path also
  // appears per-officer in the status table, so scope to the alert.
  await expect(page.getByRole("alert").getByText(/officer\/claim/)).toBeVisible();

  // "Open Database" is immediately enabled.
  const openBtn = page.getByTestId("open-database-btn");
  await expect(openBtn).toBeEnabled();

  await waitForHealthy(daemon!);
  await openBtn.click();

  // App shell loaded.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
});

test("shamir: after init, Seal Manager shows Security Officers panel and Shamir M-of-N counts", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: 15_000 });

  await page.getByText("Shamir (M-of-N)").click();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "Register Security Officers" })).toBeVisible({ timeout: 10_000 });

  const csrDirs: string[] = [];
  try {
    for (let i = 0; i < TOTAL; i++) {
      const { csrPem, keyDir } = generateCSR(`officer-${i + 1}`);
      csrDirs.push(keyDir);
      await page.getByTestId(`officer-username-${i}`).fill(`officer-${i + 1}`);
      await page.getByTestId(`officer-csr-${i}`).fill(csrPem);
      await page.getByTestId(`register-officer-btn-${i}`).click();
      await expect(page.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: 15_000 });
    }
  } finally {
    for (const dir of csrDirs) {
      rmSync(dir, { recursive: true, force: true });
    }
  }

  await page.getByTestId("initialize-btn").click();
  await expect(
    page.getByRole("heading", { name: "Shamir Barrier Initialized" }),
  ).toBeVisible({ timeout: 20_000 });

  const openBtn = page.getByTestId("open-database-btn");
  await expect(openBtn).toBeEnabled();
  await waitForHealthy(daemon!);
  await openBtn.click();

  // Navigate to Seal Manager.
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // Security Officers panel must be visible for shamir barriers.
  await expect(page.getByRole("heading", { name: "Security Officers" })).toBeVisible();

  // Shamir barriers cannot be sealed by a single admin: the server rejects
  // BarrierSeal with ErrShamirBarrierRequiresQuorum. The Seal Manager must
  // therefore present the quorum seal-vote panel instead of the single-admin
  // Seal button.
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toHaveCount(0);
  // String matchers normalize whitespace (the copy wraps across source lines).
  await expect(page.getByText("quorum of Security Officers")).toBeVisible();
  await expect(
    page.getByText(`${THRESHOLD} of ${TOTAL} officers must each submit`),
  ).toBeVisible();
  await expect(page.getByTestId("seal-vote-input")).toBeVisible();

  // The vote button stays disabled until a share is entered (shares are only
  // obtainable by officers through the mTLS claim ceremony, so the actual
  // quorum-seal e2e lives in the real-backend tier).
  await expect(page.getByTestId("submit-seal-vote-btn")).toBeDisabled();
});
