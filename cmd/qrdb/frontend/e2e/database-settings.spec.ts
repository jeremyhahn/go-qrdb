/*
 * Copyright 2026 Jeremy Hahn
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Mocked-tier e2e for the per-database settings forms in the admin database
// detail: database.updateSQL, updateCompaction, updateRaft, updateBarrier and
// updateConnection. The Wails mock applies each request to the database entry
// of its state, so a test reads the entry back to assert what the form sent.

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
  type MockState,
} from "./fixtures/wails-mock.ts";
import type { Locator, Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

const DB_NAME = "settingsdb";

const stateWithDb: MockState = {
  ...secondLaunchEmbeddedState,
  databases: [{ name: DB_NAME, backend: "pebble", status: "open" }],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Opens the admin detail view of the test database. */
async function openDetail(page: Page): Promise<Locator> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator('aside button[title="Databases"]').click();
  const main = page.locator("main");
  await expect(main.getByText(DB_NAME)).toBeVisible({ timeout: LOAD_TIMEOUT });
  await main.getByText(DB_NAME).click();
  await expect(main.getByTestId("db-settings-sql")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  return main;
}

/** The mock's database entry: what the last update request wrote. */
async function storedDb(page: Page): Promise<Record<string, unknown>> {
  return page.evaluate((name: string) => {
    const w = window as unknown as Record<string, unknown>;
    const s = w.__QRDB_MOCK_STATE__ as { databases: { name: string }[] };
    const db = s.databases.find((d) => d.name === name);
    return JSON.parse(JSON.stringify(db ?? {})) as Record<string, unknown>;
  }, DB_NAME);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Database settings forms", () => {
  test.use({ mockState: stateWithDb });

  test("renders all five settings forms", async ({ page }) => {
    const main = await openDetail(page);
    for (const id of ["sql", "compaction", "raft", "barrier", "connection"]) {
      await expect(main.getByTestId(`db-settings-${id}`)).toBeVisible();
    }
  });

  test("updateSQL saves the concurrency mode", async ({ page }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-sql");
    await form.getByRole("combobox").selectOption("pessimistic");
    await form.getByRole("button", { name: "Save SQL Settings" }).click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "SQL settings saved.",
    );
    expect((await storedDb(page)).sql).toEqual({ concurrency: "pessimistic" });
  });

  test("updateCompaction saves a period policy and validates it", async ({
    page,
  }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-compaction");
    await form.getByRole("combobox").selectOption("period");
    const save = form.getByRole("button", { name: "Save Compaction Policy" });

    await form.getByLabel("Retention period").fill("soon");
    await expect(form.getByTestId("validation-error")).toBeVisible();
    await expect(save).toBeDisabled();

    await form.getByLabel("Retention period").fill("24h");
    await expect(form.getByTestId("validation-error")).toHaveCount(0);
    await save.click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "Compaction policy saved.",
    );
    expect((await storedDb(page)).compaction).toEqual({
      mode: "period",
      retainPeriod: "24h",
    });
  });

  test("updateRaft saves a distributed configuration", async ({ page }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-raft");
    await form.getByLabel("Cluster mode").selectOption("distributed");
    await form.getByLabel("Replica ID").fill("2");
    await form.getByLabel("Raft address").fill("10.0.0.2:7000");
    await form.getByLabel("Discovery").selectOption("static");
    await form.getByLabel("Peers").fill("10.0.0.1:7000\n10.0.0.3:7000");
    await form.getByRole("button", { name: "Save Raft Configuration" }).click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "Raft configuration saved.",
    );
    expect((await storedDb(page)).raft).toEqual({
      clusterMode: "distributed",
      replicaId: 2,
      raftAddress: "10.0.0.2:7000",
      peers: ["10.0.0.1:7000", "10.0.0.3:7000"],
      discovery: "static",
    });
  });

  test("updateBarrier enables a passphrase barrier", async ({ page }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-barrier");
    await form.getByLabel("Barrier enabled").check();
    await form.getByLabel("Barrier mode").selectOption("passphrase");
    await form
      .getByRole("button", { name: "Save Barrier Configuration" })
      .click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "Barrier configuration saved.",
    );
    expect((await storedDb(page)).barrier).toEqual({
      enabled: true,
      mode: "passphrase",
    });
  });

  test("updateConnection saves the remote connection", async ({ page }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-connection");
    await form.getByLabel("Transport").selectOption("quic");
    await form.getByLabel("Address").fill("db.example.com:9443");
    await form.getByLabel("CA file").fill("/etc/qrdb/ca.pem");
    await form.getByRole("button", { name: "Save Connection" }).click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "Connection configuration saved.",
    );
    expect((await storedDb(page)).connection).toEqual({
      transport: "quic",
      address: "db.example.com:9443",
      caFile: "/etc/qrdb/ca.pem",
    });
  });

  test("a server error shows the error state and keeps the stored value", async ({
    page,
  }) => {
    const main = await openDetail(page);
    await injectError(page, "DatabaseBinding.UpdateSQL", "sql update refused");

    const form = main.getByTestId("db-settings-sql");
    await form.getByRole("combobox").selectOption("optimistic");
    await form.getByRole("button", { name: "Save SQL Settings" }).click();

    await expect(form.getByTestId("outcome-error")).toContainText(
      "sql update refused",
    );
    await expect(form.getByTestId("outcome-success")).toHaveCount(0);
    expect((await storedDb(page)).sql).toBeUndefined();

    await clearError(page, "DatabaseBinding.UpdateSQL");
  });
});
