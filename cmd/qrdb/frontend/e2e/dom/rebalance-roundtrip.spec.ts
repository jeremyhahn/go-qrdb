// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * rebalance-roundtrip.spec.ts (real-backend DOM tier — multi-node)
 *
 * Drives views/admin/RebalancePanel.svelte against a live 3-node qrdbd Raft
 * cluster (dom-cluster.ts) after two databases were created over REST, and
 * checks each panel surface against the daemon's own REST answer:
 *
 *   1. Node Load: one row per cluster node, with the shard, leader and key
 *      counts GET /v1/rebalance/report returns for that node.
 *   2. Generate Plan: the UI's POST /v1/rebalance/plan reports the same move
 *      count the server plans for the same request. On this evenly loaded
 *      cluster that is zero moves, so the panel offers no Execute action.
 *   3. Shard statistics (rebalance.getShardStats): the panel opens them from
 *      a plan move row. An even cluster plans no move, so the spec passes the
 *      real plan through and adds one move for the real system shard (1);
 *      the statistics the panel then shows come live from
 *      GET /v1/rebalance/shards/1 and must equal the REST answer. This is the
 *      regression guard for the SDK calling a route the server never served
 *      (/v1/rebalance/shards/{id}/stats, 404).
 */

import { test, expect, type Page } from "@playwright/test";
import { startCluster, stopCluster, UI_BASE_URL, type DomCluster } from "./fixtures/dom-cluster.ts";

const LOAD_TIMEOUT = 20_000;
const OPTS = { ignoreHTTPSErrors: true };

let cluster: DomCluster | null = null;

test.beforeEach(async () => {
  cluster = await startCluster(3);
});

test.afterEach(async () => {
  await stopCluster(cluster);
  cluster = null;
});

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

async function openRebalance(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Rebalance", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Rebalance Plan" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test("multi-node cluster: rebalance panel shows the server's node load and plan", async ({ page, request }) => {
  test.setTimeout(120_000);
  const base = cluster!.adminUrl;

  for (const name of ["rb-alpha", "rb-beta"]) {
    const resp = await request.post(`${base}/v1/databases`, { ...OPTS, data: { name, backend: "raft" } });
    expect(resp.status()).toBe(201);
  }

  const reportResp = await request.get(`${base}/v1/rebalance/report`, OPTS);
  expect(reportResp.ok()).toBeTruthy();
  const report = await reportResp.json();
  expect(report.nodes).toHaveLength(3);

  await openRebalance(page);
  const main = page.locator("main");
  const nodeTable = main.locator(".card", { hasText: "Node Load" }).locator("table");
  await expect(nodeTable.locator("tbody tr")).toHaveCount(3, { timeout: LOAD_TIMEOUT });

  for (const node of report.nodes) {
    const row = nodeTable.locator("tbody tr", { hasText: node.node_id });
    await expect(row).toHaveCount(1);
    const cells = row.locator("td");
    await expect(cells.nth(1)).toHaveText(node.address);
    await expect(cells.nth(2)).toHaveText(String(node.shard_count));
    await expect(cells.nth(3)).toHaveText(String(node.leader_count));
    await expect(cells.nth(4)).toHaveText(String(node.total_keys));
  }

  // Generate a plan from the panel and compare it with the server's own plan
  // for the same request (Max Moves 10, Target Balance 0.1 are the defaults).
  const planResp = await request.post(`${base}/v1/rebalance/plan`, {
    ...OPTS,
    data: { max_moves: 10, target_balance: 0.1 },
  });
  expect(planResp.ok()).toBeTruthy();
  const plan = await planResp.json();
  const moves: unknown[] = plan.moves ?? [];

  await main.getByRole("button", { name: "Generate Plan" }).click();
  await expectToast(page, `Plan generated: ${moves.length} move(s)`);
  if (moves.length === 0) {
    await expect(main.getByRole("button", { name: "Execute Plan" })).toHaveCount(0);
  } else {
    await expect(main.getByRole("button", { name: "Execute Plan" })).toBeVisible();
  }
});

test("multi-node cluster: rebalance panel shows live shard statistics for a plan move", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  const base = cluster!.adminUrl;

  const statsResp = await request.get(`${base}/v1/rebalance/shards/1`, OPTS);
  expect(statsResp.status()).toBe(200);
  const stats = await statsResp.json();
  expect(stats.shard_id).toBe(1);

  const report = await (await request.get(`${base}/v1/rebalance/report`, OPTS)).json();
  const [from, to] = [report.nodes[0].node_id, report.nodes[1].node_id];

  // Pass the real plan through and add one move for the real system shard,
  // so the panel offers the move row that opens shard statistics.
  await page.route("**/v1/rebalance/plan", async (route) => {
    const real = await route.fetch();
    const body = await real.json();
    body.moves = [{ shard_id: 1, from_node: from, to_node: to, estimated_bytes: 0 }];
    await route.fulfill({ response: real, json: body });
  });

  await openRebalance(page);
  const main = page.locator("main");
  await main.getByRole("button", { name: "Generate Plan" }).click();
  await expectToast(page, "Plan generated: 1 move(s)");

  const moveRow = main.locator(".card", { hasText: "Rebalance Plan" }).locator("tbody tr", { hasText: from });
  await expect(moveRow).toHaveCount(1, { timeout: LOAD_TIMEOUT });
  await moveRow.click();

  await expect(main.getByRole("heading", { name: "Shard 1 Statistics" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  const statsTable = main.locator("table", { has: page.locator("th", { hasText: "Leader Replica" }) });
  await expect(statsTable.locator("tr", { hasText: "Shard ID" }).locator("td")).toHaveText("1", {
    timeout: LOAD_TIMEOUT,
  });
  await expect(statsTable.locator("tr", { hasText: "Keys" }).locator("td")).toHaveText(String(stats.key_count));
  await expect(statsTable.locator("tr", { hasText: "Bytes" }).locator("td")).toContainText(
    `(${stats.byte_count})`,
  );
  await expect(statsTable.locator("tr", { hasText: "Leader Replica" }).locator("td")).toHaveText(
    String(stats.leader_id),
  );
  await expect(statsTable.locator("tr", { hasText: "Replicas" }).locator("td")).toHaveText(
    String(stats.replica_count),
  );

  await main.getByRole("button", { name: "Close" }).click();
  await expect(main.getByRole("heading", { name: "Shard 1 Statistics" })).toHaveCount(0);
});
