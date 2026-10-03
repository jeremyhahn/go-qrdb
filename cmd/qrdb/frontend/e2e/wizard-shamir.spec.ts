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

/** Opens a database's admin view from the sidebar's Databases list. */
async function openDatabaseView(page: Page, name: string): Promise<void> {
  await page.locator('aside button[title="Databases"]').click();
  const main = page.locator("main");
  await expect(main.getByText(name, { exact: true })).toBeVisible({ timeout: INIT_TIMEOUT });
  await main.getByText(name, { exact: true }).click();
  await expect(page.getByTestId("db-holders-panel")).toBeVisible({ timeout: INIT_TIMEOUT });
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

  test("Shamir is offered, and its M-of-N is held to whole numbers with 2 <= M <= N <= 255 before Next", async ({ page }) => {
    await toBarrierStep(page, "shamir-validate");
    const shamir = page.getByTestId("db-seal-mode-shamir");
    await expect(shamir).toBeEnabled();
    await expect(shamir).not.toHaveAttribute("title", /.+/);
    const next = page.getByRole("button", { name: "Next" });
    for (const [m, n, message] of [
      [1, 3, "At least 2 shares must be required."],
      [4, 3, "Shares required cannot exceed total shares."],
      [2.5, 3, "Shares must be whole numbers."],
      [2, 3.5, "Shares must be whole numbers."],
      // The server's seal splits a key to at most 255 shares.
      [2, 256, "Total shares cannot exceed 255."],
    ] as const) {
      await chooseShamir(page, m, n);
      await expect(next).toBeDisabled();
      await expect(page.locator("main")).toContainText(message);
    }
    await chooseShamir(page, 2, 255);
    await expect(next).toBeEnabled();
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

test.describe("Database view — resuming a Shamir database's enrolment", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("a database left sealed by Finish later resumes enrolment in the database view and initializes once exactly N are enrolled", async ({ page }) => {
    await recordKeyBindingCalls(page);
    await createShamir(page, "vault-resume", 2, 3);
    await expect(page.getByRole("heading", { name: "Share Holders" })).toBeVisible({ timeout: INIT_TIMEOUT });
    await enrolHolders(page, 1);
    await page.getByTestId("db-holders-leave").click();
    await page.getByTestId("db-holders-leave-confirm").click();
    await expect(page.locator("aside").getByText("vault-resume")).toBeVisible({ timeout: INIT_TIMEOUT });

    await openDatabaseView(page, "vault-resume");
    const panel = page.getByTestId("db-holders-panel");
    const resume = panel.getByTestId("db-holders-resume");
    await expect(resume).toContainText("Custody is not initialized");
    await expect(panel.getByTestId("db-holders-table")).toContainText("holder-0");
    // The rekey and revocation rounds need an initialized custody.
    await expect(panel.getByTestId("rekey-panel")).toHaveCount(0);

    // One holder is enrolled: two forms remain, and Initialize waits for them.
    const initialize = panel.getByTestId("db-holders-panel-initialize");
    await expect(resume.getByTestId("officer-registration-count")).toHaveText(/0 \/ 2/);
    await expect(initialize).toBeDisabled();
    await expect(initialize).toContainText("(1/3)");
    for (const [form, name] of [[0, "holder-1"], [1, "holder-2"]] as const) {
      await expect(initialize).toBeDisabled();
      await resume.getByTestId(`officer-username-${form}`).fill(name);
      await resume.getByTestId(`officer-csr-${form}`).fill(CSR);
      await resume.getByTestId(`register-officer-btn-${form}`).click();
      await expect(resume.getByTestId(`officer-cert-${form}`)).toBeVisible({ timeout: INIT_TIMEOUT });
      if (form === 0) await expect(initialize).toContainText("(2/3)");
    }
    await expect(initialize).toBeEnabled();
    await initialize.click();

    await expect(panel.getByTestId("db-holders-initialized")).toContainText("2-of-3, 3");
    await expect(panel.getByTestId("db-holders-resume")).toHaveCount(0);
    await expect(panel.getByTestId("rekey-panel")).toBeVisible();
    const state = await mockDatabases(page);
    expect(state.holders["vault-resume"]).toEqual([["holder-0", 1], ["holder-1", 2], ["holder-2", 3]]);
    // Holder admin only: no claim or submission was made from the page.
    expect(await page.evaluate(() => (window as unknown as { __keyBindingCalls: string[] }).__keyBindingCalls)).toEqual([]);
    await expect(page.locator("body")).not.toContainText("PRIVATE KEY");
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
  databaseBarrierInitializedAt: { vault: "2026-09-30T00:00:00Z" },
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

/**
 * A sealed 2-of-2 database with three holders enrolled and custody not
 * initialized: possible before the server refused enrolment past N.
 */
const overLimitState: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [
    {
      name: "vault-over",
      engine: "kv",
      backend: "file",
      status: "sealed",
      data_dir: "/var/lib/qrdb/vault-over",
      barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2 },
    },
  ],
  databaseOfficers: {
    "vault-over": ["alice", "bob", "carol"].map((username) => ({
      username,
      role: "so" as const,
      cert_thumbprint: `mock${username}`,
      share_index: 0,
      designated: "mtls",
      factors: ["mtls"],
      encryption_kem: "dhkem-p256",
      enrolled_at: "2026-09-30T00:00:00Z",
      claimed_at: "0001-01-01T00:00:00Z",
      share_pending: false,
    })),
  },
};

test.describe("Database view — more holders than N", () => {
  test.use({ mockState: overLimitState });

  test("says why Initialize cannot run, and offers no more enrolment", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: INIT_TIMEOUT });
    await openDatabaseView(page, "vault-over");
    const panel = page.getByTestId("db-holders-panel");
    const over = panel.getByTestId("db-holders-over-limit");
    await expect(over).toContainText("3 holders are enrolled");
    await expect(over).toContainText("split to exactly 2");
    await expect(over).toContainText("remove 1 holder before initializing");
    await expect(panel.getByTestId("db-holders-panel-initialize")).toBeDisabled();
    await expect(panel.getByTestId("officer-username-0")).toHaveCount(0);
  });
});

test.describe("Wails mock — a Shamir database, as the server answers", () => {
  test.use({ mockState: { ...secondLaunchEmbeddedState, databaseShamirCustody: false } });

  type Bindings = Record<string, Record<string, (...args: unknown[]) => Promise<unknown>>>;

  async function call(page: Page, binding: string, method: string, ...args: unknown[]): Promise<string> {
    return page.evaluate(
      async ({ binding, method, args }) => {
        const main = (window as unknown as { go: { main: Bindings } }).go.main;
        try {
          await main[binding]![method]!(...args);
          return "ok";
        } catch (err) {
          return err instanceof Error ? err.message : String(err);
        }
      },
      { binding, method, args },
    );
  }

  test("refuses a secret before custody, and wraps an M-of-N the seal cannot split", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: INIT_TIMEOUT });
    const create = (barrier: Record<string, unknown>) =>
      call(page, "DatabaseBinding", "Create", { name: "v", engine: "kv", backend: "file", barrier });
    // The secret is refused first, even on a node with no custody.
    expect(await create({ enabled: true, mode: "shamir", secret: "s", sharesRequired: 2, sharesTotal: 2 })).toBe(
      'database: the shamir barrier of database "v" takes no secret: its key is split to its share holders',
    );
    expect(await create({ enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2 })).toBe(
      'database: no share-holder custody is configured for the shamir barrier of database "v"',
    );
    await page.evaluate(() => {
      (window as unknown as { __QRDB_MOCK_STATE__: { databaseShamirCustody?: boolean } }).__QRDB_MOCK_STATE__.databaseShamirCustody = true;
    });
    expect(await create({ enabled: true, mode: "shamir", sharesRequired: 3, sharesTotal: 2 })).toBe(
      'database: failed to create barrier for database "v": quicraft/seal: shamir threshold must be >= 2 and <= total shares',
    );
  });

  test("refuses a holder past N and a second initialization", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: INIT_TIMEOUT });
    await page.evaluate(() => {
      (window as unknown as { __QRDB_MOCK_STATE__: { databaseShamirCustody?: boolean } }).__QRDB_MOCK_STATE__.databaseShamirCustody = true;
    });
    expect(await call(page, "DatabaseBinding", "Create", {
      name: "v", engine: "kv", backend: "file", barrier: { enabled: true, mode: "shamir", sharesRequired: 2, sharesTotal: 2 },
    })).toBe("ok");
    for (const username of ["alice", "bob"]) {
      expect(await call(page, "OfficerBinding", "RegisterHolder", { database: "v" }, { username, csr_pem: "CSR" })).toBe("ok");
    }
    expect(await call(page, "OfficerBinding", "RegisterHolder", { database: "v" }, { username: "carol", csr_pem: "CSR" })).toBe(
      "ceremony: all 2 share holders the barrier is split to are enrolled; no more can be enrolled before it is initialized",
    );
    expect(await call(page, "OfficerBinding", "InitializeDatabaseShamir", "v", "")).toBe("ok");
    expect(await call(page, "OfficerBinding", "InitializeDatabaseShamir", "v", "")).toBe("qrdb/seal: barrier already initialized");
    // Once initialized, a new holder enrols and joins at the next rekey.
    expect(await call(page, "OfficerBinding", "RegisterHolder", { database: "v" }, { username: "carol", csr_pem: "CSR" })).toBe("ok");
  });
});
