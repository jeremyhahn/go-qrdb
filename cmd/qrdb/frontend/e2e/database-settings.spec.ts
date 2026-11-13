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
    await form.getByLabel("Peers", { exact: true }).fill("10.0.0.1:7000\n10.0.0.3:7000");
    // Static discovery is new to this database: Save waits for its peers.
    await expect(form.getByText("Static discovery needs at least one peer.")).toBeVisible();
    await expect(form.getByRole("button", { name: "Save Raft Configuration" })).toBeDisabled();
    await form.getByLabel("Static peers").fill("10.0.0.4:7000");
    await form.getByLabel("Shared secret").fill("static-secret");
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
      static: { peers: ["10.0.0.4:7000"], sharedSecret: "static-secret" },
    });
  });

  test.describe("with an existing static discovery block", () => {
    test.use({
      mockState: {
        ...stateWithDb,
        databases: [{
          name: DB_NAME,
          backend: "pebble",
          status: "open",
          raft: {
            clusterMode: "distributed",
            replicaId: 1,
            raftAddress: "10.0.0.1:7000",
            discovery: "static",
            static: { peers: ["10.0.0.2:7000", "10.0.0.3:7000"], sharedSecret: "kept-secret" },
          },
        }],
      },
    });

    test("updateRaft keeps the unedited static peers and secret", async ({ page }) => {
      const main = await openDetail(page);
      const form = main.getByTestId("db-settings-raft");
      // The method is already configured: the form asks for nothing more.
      await expect(form.getByLabel("Static peers")).toHaveCount(0);
      await form.getByLabel("Replica ID").fill("3");
      await form.getByRole("button", { name: "Save Raft Configuration" }).click();

      await expect(form.getByTestId("outcome-success")).toHaveText(
        "Raft configuration saved.",
      );
      expect((await storedDb(page)).raft).toEqual({
        clusterMode: "distributed",
        replicaId: 3,
        raftAddress: "10.0.0.1:7000",
        discovery: "static",
        static: { peers: ["10.0.0.2:7000", "10.0.0.3:7000"], sharedSecret: "kept-secret" },
      });
    });

    test("switching to DNS discovery requires its domain and keeps the static block", async ({ page }) => {
      const main = await openDetail(page);
      const form = main.getByTestId("db-settings-raft");
      await form.getByLabel("Discovery").selectOption("dns");
      await expect(form.getByText("DNS discovery needs a domain.")).toBeVisible();
      await expect(form.getByRole("button", { name: "Save Raft Configuration" })).toBeDisabled();
      await form.getByLabel("DNS domain").fill("cluster.example.com");
      await form.getByRole("button", { name: "Save Raft Configuration" }).click();

      await expect(form.getByTestId("outcome-success")).toHaveText(
        "Raft configuration saved.",
      );
      expect((await storedDb(page)).raft).toEqual({
        clusterMode: "distributed",
        replicaId: 1,
        raftAddress: "10.0.0.1:7000",
        discovery: "dns",
        static: { peers: ["10.0.0.2:7000", "10.0.0.3:7000"], sharedSecret: "kept-secret" },
        dns: { domain: "cluster.example.com" },
      });
    });
  });

  test("the barrier is shown read-only and the server refuses a change", async ({ page }) => {
    const main = await openDetail(page);
    const section = main.getByTestId("db-settings-barrier");
    await expect(section.getByTestId("barrier-enabled")).toHaveText("No");
    await expect(section.getByTestId("barrier-fixed-note")).toContainText(
      "fixed when the database is created",
    );
    await expect(section.getByTestId("barrier-fixed-note")).toContainText("Share Holders");
    // No edit controls: the backend refuses every transition they offered.
    await expect(section.getByRole("checkbox")).toHaveCount(0);
    await expect(section.getByRole("combobox")).toHaveCount(0);
    await expect(section.getByRole("button")).toHaveCount(0);

    // The mock refuses an enable as the server does, with its message.
    const before = (await storedDb(page)).barrier;
    const refusal = await page.evaluate(async (name: string) => {
      const w = window as unknown as {
        go: { main: { DatabaseBinding: { UpdateBarrier: (n: string, r: unknown) => Promise<unknown> } } };
      };
      try {
        await w.go.main.DatabaseBinding.UpdateBarrier(name, { barrier: { enabled: true, mode: "passphrase" } });
        return "";
      } catch (err: unknown) {
        return String(err);
      }
    }, DB_NAME);
    expect(refusal).toContain("cannot enable the encryption barrier");
    expect((await storedDb(page)).barrier).toEqual(before);
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

  test("a new REST connection takes an http(s) URL, not host:port", async ({ page }) => {
    const main = await openDetail(page);
    const form = main.getByTestId("db-settings-connection");
    const save = form.getByRole("button", { name: "Save Connection" });
    await form.getByLabel("Transport").selectOption("rest");

    await form.getByLabel("Address").fill("db.example.com:8080");
    await expect(form.getByText("Address must be an http:// or https:// URL.")).toBeVisible();
    await expect(save).toBeDisabled();
    await form.getByLabel("Address").fill("ftp://db.example.com");
    await expect(save).toBeDisabled();

    await form.getByLabel("Address").fill("https://db.example.com");
    await expect(form.getByTestId("validation-error")).toHaveCount(0);
    await save.click();

    await expect(form.getByTestId("outcome-success")).toHaveText(
      "Connection configuration saved.",
    );
    expect((await storedDb(page)).connection).toEqual({
      transport: "rest",
      address: "https://db.example.com",
    });
  });

  test.describe("with an existing REST connection", () => {
    test.use({
      mockState: {
        ...stateWithDb,
        databases: [{
          name: DB_NAME,
          backend: "pebble",
          status: "open",
          connection: { transport: "rest", address: "https://db.example.com" },
        }],
      },
    });

    test("the form opens valid with Save enabled", async ({ page }) => {
      const main = await openDetail(page);
      const form = main.getByTestId("db-settings-connection");
      await expect(form.getByLabel("Address")).toHaveValue("https://db.example.com");
      await expect(form.getByTestId("validation-error")).toHaveCount(0);
      await expect(form.getByRole("button", { name: "Save Connection" })).toBeEnabled();
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
