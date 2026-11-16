// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * node-compaction-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the node-wide MVCC compaction editor on the Cluster page
 * (admin.nodeCompaction / admin.setNodeCompaction) and the shard statistics
 * lookup on the Rebalance page (rebalance.getShardStats) against one real
 * qrdbd, and checks every value the UI shows against the daemon's own REST
 * response.
 */

import { test, expect, request as playwrightRequest, type APIRequestContext, type Page } from "@playwright/test";
import { startCluster, stopCluster, UI_BASE_URL, type DomCluster } from "./fixtures/dom-cluster.ts";

const LOAD_TIMEOUT = 20_000;

// The Cluster and Rebalance admin views are shown only when the UI detects
// raft mode from /admin/topology (several distinct nodes); a standalone
// daemon hides them and also has no node keyspace to compact, so this spec
// boots the real 3-node cluster fixture and drives node 1.
let daemon: DomCluster | null = null;
let api: APIRequestContext | null = null;

// Booting three raft nodes takes longer than the default 30 s test timeout.
test.describe.configure({ timeout: 180_000 });

test.beforeEach(async () => {
  daemon = await startCluster(3, { sm: "mvcc" });
  api = await playwrightRequest.newContext({ ignoreHTTPSErrors: true });
});

test.afterEach(async () => {
  await api?.dispose();
  api = null;
  await stopCluster(daemon);
  daemon = null;
});

async function expectToast(page: Page, text: string): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

async function getJSON(path: string): Promise<Record<string, unknown>> {
  const resp = await api!.get(`${daemon!.adminUrl}${path}`);
  expect(resp.status(), `${path}: ${await resp.text()}`).toBe(200);
  return (await resp.json()) as Record<string, unknown>;
}

async function openAdminView(page: Page, name: string): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText(name, { exact: true }).click();
}

test("Cluster page reads and saves the node compaction policy on the server", async ({ page }) => {
  // The daemon boots with no node-wide policy: the editor shows "off".
  const before = await getJSON("/admin/node-compaction");
  expect(before.compaction ?? null).toBeNull();

  await openAdminView(page, "Cluster");
  const card = page.getByTestId("node-compaction-card");
  const mode = card.getByLabel("Node compaction mode");
  await expect(mode).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(mode).toHaveValue("off");

  // Change to a revision policy and save.
  await mode.selectOption("revision");
  await card.getByLabel("Node retain revisions").fill("7");
  await card.getByRole("button", { name: "Save Compaction" }).click();
  await expectToast(page, "Node compaction saved");

  // The server holds the new policy.
  const afterRevision = await getJSON("/admin/node-compaction");
  expect(afterRevision.compaction).toMatchObject({ mode: "revision" });
  const revisionPolicy = afterRevision.compaction as Record<string, unknown>;
  expect(revisionPolicy.retain_revisions ?? revisionPolicy.retainRevisions).toBe(7);

  // A fresh load of the page reads it back from the server.
  await page.reload();
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Cluster", { exact: true }).click();
  await expect(card.getByLabel("Node compaction mode")).toHaveValue("revision", { timeout: LOAD_TIMEOUT });
  await expect(card.getByLabel("Node retain revisions")).toHaveValue("7");

  // Switch to a period policy, then clear it again.
  await card.getByLabel("Node compaction mode").selectOption("period");
  await card.getByLabel("Node retain period").fill("24h");
  await card.getByRole("button", { name: "Save Compaction" }).click();
  await expectToast(page, "Node compaction saved");
  const afterPeriod = await getJSON("/admin/node-compaction");
  expect(afterPeriod.compaction).toMatchObject({ mode: "period" });
  const periodPolicy = afterPeriod.compaction as Record<string, unknown>;
  expect(periodPolicy.retain_period ?? periodPolicy.retainPeriod).toBe("24h");

  await card.getByLabel("Node compaction mode").selectOption("off");
  await card.getByRole("button", { name: "Save Compaction" }).click();
  await expect
    .poll(async () => (await getJSON("/admin/node-compaction")).compaction ?? null, { timeout: LOAD_TIMEOUT })
    .toBeNull();
});

test("Rebalance page shows the server's statistics for the system shard", async ({ page }) => {
  const stats = await getJSON("/v1/rebalance/shards/1");

  await openAdminView(page, "Rebalance");
  const card = page.getByTestId("shard-stats-card");
  await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });
  await card.getByLabel("Shard ID").fill("1");
  await card.getByRole("button", { name: "Load Stats" }).click();

  const panel = page.getByTestId("shard-stats-panel");
  const table = panel.locator("table");
  await expect(table).toBeVisible({ timeout: LOAD_TIMEOUT });
  const cell = (label: string) => table.locator("tr", { has: page.locator("th", { hasText: label }) }).locator("td");
  const field = (snake: string, camel: string) => stats[snake] ?? stats[camel];

  await expect(cell("Shard ID")).toHaveText(String(field("shard_id", "shardId")));
  await expect(cell("Keys")).toHaveText(String(field("key_count", "keyCount")));
  await expect(cell("Bytes")).toContainText(`(${String(field("byte_count", "byteCount"))})`);
  await expect(cell("Leader Replica")).toHaveText(String(field("leader_id", "leaderId")));
  await expect(cell("Replicas")).toHaveText(String(field("replica_count", "replicaCount")));
});
