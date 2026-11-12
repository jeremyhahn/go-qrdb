// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * database-settings-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the per-database settings forms of the admin database detail
 * (DatabaseSettingsForms: database.updateSQL, updateCompaction, updateRaft,
 * updateBarrier, updateConnection) against a live seal:"none" daemon. Each
 * test changes one value in a form, saves it, reloads the page, and checks
 * both the re-rendered form and the daemon's stored database entry
 * (GET /v1/databases/{name}).
 */

import { test, expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { join } from "node:path";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const DB_NAME = "settingsrt";
// A per-database barrier needs a dedicated store that it wraps: a pebble
// database with a data directory of its own (a raft-backend database is
// served from its raft group and refused).
const BARRIER_DB = "barriersettings";
// Raft settings run on a raft-backend database.
const RAFT_DB = "raftsettings";

let daemon: DomDaemon | null = null;
let api: APIRequestContext | null = null;

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ playwright }) => {
  // The server's config validation refuses seal.enabled=false while
  // auth.enabled is at its default (true) before the --auth-enabled=false
  // flag applies; the fixture passes the parent environment through, so the
  // env override reaches validation and the daemon boots unsealed.
  process.env.QRDB_AUTH_ENABLED = "false";
  // A compaction policy needs a store that keeps MVCC history.
  process.env.QRDB_RAFT_SM_TYPE = "mvcc";
  daemon = await startDaemon({ seal: "none" });
  api = await playwright.request.newContext({
    baseURL: daemon.adminUrl,
    ignoreHTTPSErrors: true,
  });
  for (const data of [
    { name: DB_NAME, backend: "pebble" },
    { name: BARRIER_DB, backend: "pebble", dataDir: join(daemon.dataDir, BARRIER_DB) },
    { name: RAFT_DB, backend: "raft" },
  ]) {
    const resp = await api.post("/v1/databases", { data });
    expect(resp.ok(), await resp.text()).toBeTruthy();
  }
});

test.afterAll(async () => {
  if (api) {
    for (const name of [DB_NAME, BARRIER_DB, RAFT_DB]) {
      const resp = await api.delete(`/v1/databases/${name}`);
      expect(resp.ok(), await resp.text()).toBeTruthy();
    }
    await api.dispose();
    api = null;
  }
  await stopDaemon(daemon);
  daemon = null;
});

/** The daemon's stored database entry. */
async function storedDb(name = DB_NAME): Promise<Record<string, unknown>> {
  const resp = await api!.get(`/v1/databases/${name}`);
  expect(resp.ok(), await resp.text()).toBeTruthy();
  return (await resp.json()) as Record<string, unknown>;
}

/** Opens the admin detail view of the test database. */
async function openDetail(page: Page, name = DB_NAME): Promise<Locator> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator('aside button[title="Databases"]').click();
  const main = page.locator("main");
  await expect(main.getByText(name, { exact: true }).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  await main.getByText(name, { exact: true }).first().click();
  await expect(main.getByTestId("db-settings-sql")).toBeVisible({ timeout: LOAD_TIMEOUT });
  return main;
}

test("updateSQL: concurrency mode round-trips through the daemon", async ({ page }) => {
  let main = await openDetail(page);
  let form = main.getByTestId("db-settings-sql");
  await form.getByRole("combobox").selectOption("pessimistic");
  await form.getByRole("button", { name: "Save SQL Settings" }).click();
  await expect(form.getByTestId("outcome-success")).toHaveText("SQL settings saved.");

  await page.reload();
  main = await openDetail(page);
  form = main.getByTestId("db-settings-sql");
  await expect(form.getByRole("combobox")).toHaveValue("pessimistic");
  expect((await storedDb()).sql).toMatchObject({ concurrency: "pessimistic" });
});

test("updateCompaction: period policy round-trips through the daemon", async ({ page }) => {
  let main = await openDetail(page);
  let form = main.getByTestId("db-settings-compaction");
  await form.getByRole("combobox").selectOption("period");
  await form.getByLabel("Retention period").fill("24h");
  await form.getByRole("button", { name: "Save Compaction Policy" }).click();
  await expect(form.getByTestId("outcome-success")).toHaveText("Compaction policy saved.");

  await page.reload();
  main = await openDetail(page);
  form = main.getByTestId("db-settings-compaction");
  await expect(form.getByRole("combobox")).toHaveValue("period");
  await expect(form.getByLabel("Retention period")).toHaveValue("24h");
  expect((await storedDb()).compaction).toMatchObject({ mode: "period", retainPeriod: "24h" });
});

test("updateConnection: remote connection round-trips through the daemon", async ({ page }) => {
  let main = await openDetail(page);
  let form = main.getByTestId("db-settings-connection");
  await form.getByLabel("Transport").selectOption("quic");
  await form.getByLabel("Address").fill("db.example.com:9443");
  await form.getByRole("button", { name: "Save Connection" }).click();
  await expect(form.getByTestId("outcome-success")).toHaveText("Connection configuration saved.");

  await page.reload();
  main = await openDetail(page);
  form = main.getByTestId("db-settings-connection");
  await expect(form.getByLabel("Transport")).toHaveValue("quic");
  await expect(form.getByLabel("Address")).toHaveValue("db.example.com:9443");
  expect((await storedDb()).connection).toMatchObject({
    transport: "quic",
    address: "db.example.com:9443",
  });
});

// The server changes no barrier after create (enable, disable and a strategy
// switch are migrations it refuses with 409; only a same-config write is
// accepted), so the roundtrip is: the form sends the change, shows the
// server's refusal, and after a reload the form and the daemon still hold
// the database's barrier as created.
test("updateBarrier: the form reports the daemon's refusal and the stored barrier is unchanged", async ({
  page,
}) => {
  let main = await openDetail(page, BARRIER_DB);
  let form = main.getByTestId("db-settings-barrier");
  await expect(form.getByLabel("Barrier enabled")).not.toBeChecked();
  await form.getByLabel("Barrier enabled").check();
  await form.getByLabel("Barrier mode").selectOption("passphrase");
  await form.getByRole("button", { name: "Save Barrier Configuration" }).click();
  await expect(form.getByTestId("outcome-error")).toContainText(
    "cannot enable the encryption barrier",
  );
  await expect(form.getByTestId("outcome-success")).toHaveCount(0);

  await page.reload();
  main = await openDetail(page, BARRIER_DB);
  form = main.getByTestId("db-settings-barrier");
  await expect(form.getByLabel("Barrier enabled")).not.toBeChecked();
  const stored = (await storedDb(BARRIER_DB)).barrier as { enabled?: boolean } | undefined;
  expect(stored?.enabled ?? false).toBe(false);
});

// The fixture runs a raft cluster (single replica), so updateRaft is a full
// roundtrip on a raft-backend database.
test("updateRaft: raft configuration round-trips through the daemon", async ({ page }) => {
  let main = await openDetail(page, RAFT_DB);
  let form = main.getByTestId("db-settings-raft");
  await form.getByLabel("Cluster mode").selectOption("distributed");
  await form.getByLabel("Replica ID").fill("1");
  await form.getByLabel("Raft address").fill("127.0.0.1:7100");
  await form.getByLabel("Discovery").selectOption("static");
  await form.getByRole("button", { name: "Save Raft Configuration" }).click();
  await expect(form.getByTestId("outcome-success")).toHaveText("Raft configuration saved.");

  await page.reload();
  main = await openDetail(page, RAFT_DB);
  form = main.getByTestId("db-settings-raft");
  await expect(form.getByLabel("Cluster mode")).toHaveValue("distributed");
  await expect(form.getByLabel("Replica ID")).toHaveValue("1");
  await expect(form.getByLabel("Raft address")).toHaveValue("127.0.0.1:7100");
  await expect(form.getByLabel("Discovery")).toHaveValue("static");
  expect((await storedDb(RAFT_DB)).raft).toMatchObject({
    clusterMode: "distributed",
    replicaId: 1,
    raftAddress: "127.0.0.1:7100",
    discovery: "static",
  });
});
