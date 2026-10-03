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

/**
 * Shamir custody of a database barrier in the database wizard and the
 * database view (docs/desktop-gui/engineering.md, docs/databases).
 *
 * The wizard offers Shamir: the create carries `barrier: {enabled: true,
 * mode: "shamir", sharesRequired, sharesTotal}` and no secret, the database is
 * created sealed, and a holders step enrols exactly N holders through the
 * database scope and initializes. The wizard administers holders only: it
 * never claims or submits. It refuses Shamir only on the server's answer: the
 * Wails mock mirrors a node with no share-holder custody, which refuses the
 * create and leaves no database.
 */

import type { Page } from "@playwright/test";
import {
  test,
  expect,
  secondLaunchEmbeddedState,
  type MockState,
} from "./fixtures/wails-mock.ts";
import {
  captureDatabaseCreates,
  capturedDatabaseCreates,
} from "./fixtures/database-create-capture.ts";

const INIT_TIMEOUT = 10_000;

const CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----";

/** Walks the wizard to the barrier step for a File database on a data directory. */
async function toBarrierStep(page: Page, name: string): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: INIT_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible();
  await page.locator("#db-name").fill(name);
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible();
  await page.locator("label").filter({ hasText: "File" }).click();
  await page.locator("#data-dir-input").fill(`/var/lib/qrdb/${name}`);
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "SQL Wire Protocols" })).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "Database Encryption Barrier" })).toBeVisible();
}

/** Chooses Shamir at m-of-n on the barrier step. */
async function chooseShamir(page: Page, m: number, n: number): Promise<void> {
  await page.getByTestId("db-seal-mode-shamir").click();
  await page.locator("#db-shares-required").fill(String(m));
  await page.locator("#db-shares-total").fill(String(n));
}

/** Chooses Shamir at m-of-n and sends the create from the summary. */
async function createShamir(page: Page, name: string, m: number, n: number): Promise<void> {
  await toBarrierStep(page, name);
  await chooseShamir(page, m, n);
  await page.getByRole("button", { name: "Next" }).click();
  await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible();
  await page.locator("main").getByRole("button", { name: "Create Database" }).click();
}

/** Enrols holders 0..n-1 through the shared enrolment forms. */
async function enrolHolders(page: Page, n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await page.getByTestId(`officer-username-${i}`).fill(`holder-${i}`);
    await page.getByTestId(`officer-csr-${i}`).fill(CSR);
    await page.getByTestId(`register-officer-btn-${i}`).click();
    await expect(page.getByTestId(`officer-cert-${i}`)).toBeVisible({ timeout: INIT_TIMEOUT });
  }
}

/**
 * Records every call the page makes to the holder bindings that use a
 * holder's own key, so a spec can assert the wizard makes none.
 */
async function recordKeyBindingCalls(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;
    w.__keyBindingCalls = [];
    const officer = (w.go as Record<string, Record<string, Record<string, unknown>>>)?.main?.OfficerBinding;
    for (const name of ["ClaimEnvelope", "SubmitShare"]) {
      const orig = officer?.[name] as ((...args: unknown[]) => Promise<unknown>) | undefined;
      if (officer && orig) {
        officer[name] = async (...args: unknown[]) => {
          (w.__keyBindingCalls as string[]).push(name);
          return orig(...args);
        };
      }
    }
  });
}

/** The mock's databases and their holders. */
async function mockDatabases(page: Page): Promise<{ names: string[]; holders: Record<string, [string, number][]> }> {
  return page.evaluate(() => {
    const s = (window as unknown as {
      __QRDB_MOCK_STATE__: {
        databases: { name: string }[];
        databaseOfficers?: Record<string, { username: string; share_index: number }[]>;
      };
    }).__QRDB_MOCK_STATE__;
    const holders: Record<string, [string, number][]> = {};
    for (const [db, list] of Object.entries(s.databaseOfficers ?? {})) {
      holders[db] = list.map((h) => [h.username, h.share_index]);
    }
    return { names: s.databases.map((d) => d.name), holders };
  });
}

test.describe("Database wizard — Shamir custody", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("Shamir is offered, and its M-of-N is held to 2 <= M <= N before Next", async ({ page }) => {
    await toBarrierStep(page, "shamir-validate");
    const shamir = page.getByTestId("db-seal-mode-shamir");
    await expect(shamir).toBeEnabled();
    await expect(shamir).not.toHaveAttribute("title", /.+/);
    const next = page.getByRole("button", { name: "Next" });
    for (const [m, n] of [[1, 3], [4, 3]] as const) {
      await chooseShamir(page, m, n);
      await expect(next).toBeDisabled();
    }
    await chooseShamir(page, 2, 3);
    await expect(next).toBeEnabled();
  });

  test("the create carries the M-of-N and no secret; N holders are enrolled through the database scope and custody is initialized", async ({ page }) => {
    await captureDatabaseCreates(page);
    await recordKeyBindingCalls(page);
    await createShamir(page, "vault", 2, 3);
    await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible({ timeout: INIT_TIMEOUT });

    const creates = await capturedDatabaseCreates(page);
    expect(creates).toHaveLength(1);
    expect(creates[0]?.["dataDir"]).toBe("/var/lib/qrdb/vault");
    expect(creates[0]?.["barrier"]).toEqual({ enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 3 });

    const initialize = page.getByTestId("db-holders-initialize");
    await expect(initialize).toBeDisabled();
    await enrolHolders(page, 3);
    await expect(page.getByTestId("officer-registration-count")).toHaveText(/3 \/ 3/);
    await expect(initialize).toBeEnabled();
    await initialize.click();

    await expect(page.getByTestId("db-holders-initialized")).toContainText("2-of-3, 3");
    await expect(page.getByTestId("db-holders-next")).toContainText(
      "qrdb database barrier-officer claim --database vault",
    );
    // The page administers holders only: no claim and no submission is made
    // from it, and it holds no share and no holder key.
    expect(await page.evaluate(() => (window as unknown as { __keyBindingCalls: string[] }).__keyBindingCalls)).toEqual([]);
    await expect(page.locator("body")).not.toContainText("PRIVATE KEY");

    await page.getByTestId("db-holders-done").click();
    await expect(page.locator("aside").getByText("vault")).toBeVisible({ timeout: INIT_TIMEOUT });
    const state = await mockDatabases(page);
    expect(state.holders["vault"]).toEqual([["holder-0", 1], ["holder-1", 2], ["holder-2", 3]]);
  });

  test("an initialization the server refuses is shown, and nothing is reported done", async ({ page }) => {
    await createShamir(page, "vault-refused", 2, 2);
    await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible({ timeout: INIT_TIMEOUT });
    await enrolHolders(page, 2);
    await page.evaluate(() => {
      const s = (window as unknown as { __QRDB_MOCK_STATE__: { errors: Record<string, string> } }).__QRDB_MOCK_STATE__;
      s.errors["OfficerBinding.InitializeDatabaseShamir"] = "seal: ceremony distributor not configured";
    });
    await page.getByTestId("db-holders-initialize").click();
    await expect(page.getByTestId("db-holders-error")).toContainText("ceremony distributor not configured");
    await expect(page.getByTestId("db-holders-initialized")).toHaveCount(0);
  });

  test("finishing later warns that the database stays sealed, then leaves it created and sealed", async ({ page }) => {
    await createShamir(page, "vault-later", 2, 3);
    await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible({ timeout: INIT_TIMEOUT });
    await enrolHolders(page, 1);

    await page.getByTestId("db-holders-leave").click();
    const warning = page.getByTestId("db-holders-leave-warning");
    await expect(warning).toContainText("stays sealed");
    await expect(warning).toContainText("qrdb database barrier-init-shamir --database vault-later");
    await page.getByTestId("db-holders-leave-confirm").click();

    await expect(page.locator("aside").getByText("vault-later")).toBeVisible({ timeout: INIT_TIMEOUT });
    const state = await mockDatabases(page);
    expect(state.names).toContain("vault-later");
    expect(state.holders["vault-later"]).toEqual([["holder-0", 0]]);
  });
});

test.describe("Database wizard — a server that cannot serve Shamir", () => {
  // A node whose database barrier manager has no share-holder custody wired.
  test.use({ mockState: { ...secondLaunchEmbeddedState, databaseShamirCustody: false } });

  test("refuses the Shamir create; the refusal is shown and no database is left", async ({ page }) => {
    await captureDatabaseCreates(page);
    await createShamir(page, "vault-nocustody", 2, 2);

    await expect(
      page.getByText(/no share-holder custody is configured for the shamir barrier of database "vault-nocustody"/),
    ).toBeVisible({ timeout: INIT_TIMEOUT });
    await expect(page.getByRole("heading", { name: "Share Holders" })).toHaveCount(0);
    expect(await capturedDatabaseCreates(page)).toHaveLength(1);
    expect((await mockDatabases(page)).names).not.toContain("vault-nocustody");
  });
});

/** A Shamir database whose custody is initialized, with two holders. */
const shamirDbState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    {
      name: "vault",
      engine: "kv",
      backend: "file",
      status: "open",
      data_dir: "/var/lib/qrdb/vault",
      barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2 },
    },
  ],
  databaseOfficers: {
    vault: ["alice", "bob"].map((username, i) => ({
      username,
      role: "so" as const,
      cert_thumbprint: `mock${username}`,
      share_index: i + 1,
      designated: "mtls",
      factors: ["mtls"],
      encryption_kem: "dhkem-p256",
      enrolled_at: "2026-09-30T00:00:00Z",
      claimed_at: "2026-09-30T00:05:00Z",
      share_pending: false,
    })),
  },
};

test.describe("Database view — Shamir holders", () => {
  test.use({ mockState: shamirDbState });

  async function openVault(page: Page): Promise<void> {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: INIT_TIMEOUT });
    await page.locator('aside button[title="Databases"]').click();
    const main = page.locator("main");
    await expect(main.getByText("vault")).toBeVisible({ timeout: INIT_TIMEOUT });
    await main.getByText("vault").click();
    await expect(page.getByTestId("db-holders-panel")).toBeVisible({ timeout: INIT_TIMEOUT });
  }

  test("lists the holders and opens a rekey round on the database", async ({ page }) => {
    await openVault(page);
    const table = page.getByTestId("db-holders-table");
    await expect(table).toContainText("alice");
    await expect(table).toContainText("bob");
    await expect(table).toContainText("Claimed");

    await expect(page.getByText("qrdb database barrier-quorum submit --database vault --op rekey")).toBeVisible();
    await page.getByTestId("rekey-panel").getByRole("button", { name: /Start Rekey/i }).click();
    await expect(page.getByText(/Rekey round opened/)).toBeVisible({ timeout: INIT_TIMEOUT });
  });
});
