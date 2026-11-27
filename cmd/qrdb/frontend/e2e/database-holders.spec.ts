// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// E2E: a Shamir database's share holders in the database detail view
// (DatabaseHoldersPanel): the holder list and barrier status, enrolment,
// unenrolment and initialization of a database whose custody is pending,
// and the rekey and revocation rounds of an initialized one -- each over the
// database scope of the Wails OfficerBinding mock.

import { test, expect, secondLaunchEmbeddedState, injectError, clearError } from "./fixtures/wails-mock.ts";
import type { MockOfficerStatus, MockState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----";
const ZERO_TIME = "0001-01-01T00:00:00Z";

function holder(username: string, role: "so" | "custodian", shareIndex: number, claimed: boolean): MockOfficerStatus {
  return {
    username,
    role,
    cert_thumbprint: `mock${username}`,
    share_index: shareIndex,
    designated: "mtls",
    factors: ["mtls"],
    encryption_kem: "dhkem-p256",
    enrolled_at: "2026-01-01T00:00:00Z",
    claimed_at: claimed ? "2026-01-02T00:00:00Z" : ZERO_TIME,
    share_pending: !claimed && shareIndex > 0,
  };
}

function shamirDatabase(name: string, sharesRequired: number, sharesTotal: number): MockState["databases"][number] {
  return {
    name,
    backend: "file",
    status: "open",
    data_dir: `/var/lib/qrdb/${name}`,
    barrier: { enabled: true, mode: "shamir", sharesRequired, sharesTotal },
  };
}

/** "vault": a 2-of-2 Shamir database whose custody is initialized. */
const initializedState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [shamirDatabase("vault", 2, 2)],
  databaseBarrierInitializedAt: { vault: "2026-01-01T00:00:00Z" },
  databaseOfficers: {
    vault: [holder("alice", "so", 1, true), holder("bob", "custodian", 2, false)],
  },
};

/** "ledger": a 2-of-3 Shamir database with two of its three holders enrolled. */
const pendingState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [shamirDatabase("ledger", 2, 3)],
  databaseOfficers: {
    ledger: [holder("alice", "so", 0, false), holder("bob", "custodian", 0, false)],
  },
};

/** "ledger" with all three holders enrolled and no split yet. */
const readyState: MockState = {
  ...pendingState,
  databaseOfficers: {
    ledger: [holder("alice", "so", 0, false), holder("bob", "custodian", 0, false), holder("carol", "so", 0, false)],
  },
};

async function openHolders(page: Page, name: string): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator('aside button[title="Databases"]').click();
  const main = page.locator("main");
  await expect(main.getByText(name, { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await main.getByText(name, { exact: true }).click();
  await expect(page.getByTestId("db-holders-panel")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Admin - Database share holders, initialized custody", () => {
  test.use({ mockState: initializedState });

  test("lists the holders with role and claim state, and offers rekey instead of enrolment", async ({ page }) => {
    await openHolders(page, "vault");
    const table = page.getByTestId("db-holders-table");
    await expect(table.locator("tr").filter({ hasText: "alice" })).toContainText("Claimed", { timeout: LOAD_TIMEOUT });
    await expect(table.locator("tr").filter({ hasText: "bob" })).toContainText("custodian");
    await expect(table.locator("tr").filter({ hasText: "bob" })).toContainText("Pending");
    // The barrier status reports an initialization time: no enrolment, no
    // unenrolment, and the rekey panel names the database's quorum command.
    await expect(page.getByTestId("db-holders-resume")).toHaveCount(0);
    await expect(page.getByTestId("db-holder-unenrol-alice")).toHaveCount(0);
    await expect(page.getByTestId("rekey-panel")).toContainText(
      "qrdb database barrier-quorum submit --database vault --op rekey",
    );
  });

  test("starting a rekey shows the round it opened", async ({ page }) => {
    await openHolders(page, "vault");
    await page.getByTestId("rekey-threshold").fill("2");
    await page.getByTestId("start-rekey-btn").click();
    await expect(page.getByTestId("open-round")).toContainText("rekey round on the unseal domain", {
      timeout: LOAD_TIMEOUT,
    });
  });

  test("revoking a holder opens a round that removes it", async ({ page }) => {
    await openHolders(page, "vault");
    await page.getByTestId("revoke-username").fill("bob");
    await page.getByTestId("revoke-holder-btn").click();
    await expect(page.getByTestId("open-round")).toContainText("Removes bob", { timeout: LOAD_TIMEOUT });
  });

  test("a failing rekey surfaces an error and opens no round", async ({ page }) => {
    await openHolders(page, "vault");
    await injectError(page, "OfficerBinding.StartRekey", "rekey would leave fewer holders than the threshold");
    await page.getByTestId("start-rekey-btn").click();
    await expect(
      page.locator('[role="alert"]').filter({ hasText: "fewer holders than the threshold" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("open-round")).toHaveCount(0);
    await clearError(page, "OfficerBinding.StartRekey");
  });
});

test.describe("Admin - Database share holders, pending custody", () => {
  test.use({ mockState: pendingState });

  test("shows the enrolled holders and that custody is not initialized", async ({ page }) => {
    await openHolders(page, "ledger");
    const panel = page.getByTestId("db-holders-panel");
    await expect(panel.getByTestId("db-holders-table")).toContainText("alice");
    await expect(panel.getByTestId("db-holders-table")).toContainText("bob");
    await expect(panel.getByTestId("db-holders-resume")).toContainText("Custody is not initialized");
    await expect(panel.getByTestId("db-holders-panel-initialize")).toBeDisabled();
    await expect(panel.getByTestId("db-holders-panel-initialize")).toContainText("(2/3)");
  });

  test("enrols the last holder, then initializes custody", async ({ page }) => {
    await openHolders(page, "ledger");
    const panel = page.getByTestId("db-holders-panel");
    await panel.getByTestId("officer-username-0").fill("carol");
    await panel.getByTestId("officer-csr-0").fill(CSR);
    await panel.getByTestId("register-officer-btn-0").click();
    await expect(panel.getByTestId("officer-cert-0")).toBeVisible({ timeout: LOAD_TIMEOUT });
    const initialize = panel.getByTestId("db-holders-panel-initialize");
    await expect(initialize).toBeEnabled();
    await initialize.click();
    await expect(panel.getByTestId("db-holders-initialized")).toContainText("2-of-3, 3", { timeout: LOAD_TIMEOUT });
    await expect(panel.getByTestId("db-holders-table")).toContainText("carol");
    await expect(panel.getByTestId("db-holders-resume")).toHaveCount(0);
  });

  test("a refused enrolment is shown and enrols nobody", async ({ page }) => {
    await openHolders(page, "ledger");
    await injectError(page, "OfficerBinding.RegisterHolder", "ceremony: CSR signature does not verify");
    const panel = page.getByTestId("db-holders-panel");
    await panel.getByTestId("officer-username-0").fill("carol");
    await panel.getByTestId("officer-csr-0").fill(CSR);
    await panel.getByTestId("register-officer-btn-0").click();
    await expect(
      panel.locator('[role="alert"]').filter({ hasText: "CSR signature does not verify" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(panel.getByTestId("officer-cert-0")).toHaveCount(0);
    await expect(panel.getByTestId("db-holders-panel-initialize")).toBeDisabled();
    await clearError(page, "OfficerBinding.RegisterHolder");
  });

  test("removes a holder enrolled before initialization", async ({ page }) => {
    await openHolders(page, "ledger");
    const panel = page.getByTestId("db-holders-panel");
    await panel.getByTestId("db-holder-unenrol-bob").click();
    await expect(panel.getByTestId("db-holders-table")).not.toContainText("bob", { timeout: LOAD_TIMEOUT });
    await expect(panel.getByTestId("db-holders-table")).toContainText("alice");
    await expect(panel.getByTestId("db-holders-panel-initialize")).toContainText("(1/3)");
  });
});

test.describe("Admin - Database share holders, all enrolled", () => {
  test.use({ mockState: readyState });

  test("initializes custody once exactly N are enrolled", async ({ page }) => {
    await openHolders(page, "ledger");
    const panel = page.getByTestId("db-holders-panel");
    const initialize = panel.getByTestId("db-holders-panel-initialize");
    await expect(initialize).toBeEnabled({ timeout: LOAD_TIMEOUT });
    await initialize.click();
    await expect(panel.getByTestId("db-holders-initialized")).toContainText("2-of-3, 3", { timeout: LOAD_TIMEOUT });
    await expect(panel.getByTestId("db-holders-resume")).toHaveCount(0);
    await expect(page.getByTestId("rekey-panel")).toBeVisible();
  });
});
