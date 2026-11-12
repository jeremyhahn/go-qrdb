// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * cluster-topology.spec.ts (real-backend DOM tier — multi-node)
 *
 * Closes the last real-backend coverage gap: the Raft cluster-only UI surfaces
 * (Cluster / Topology nav, the database-wizard Replication Factor picker, the
 * dedicated-WAL toggle) were previously exercised only against mocked bindings,
 * because the single-node DOM harness boots a standalone daemon.
 *
 * This spec boots a real 3-node qrdbd Raft cluster (dom-cluster.ts), points the
 * browser at node 1, and verifies end-to-end that:
 *
 *   1. The web UI detects the multi-node topology from the live
 *      /admin/topology endpoint (connection-manager.detectWebServerMode) and
 *      surfaces the Cluster + Topology admin nav entries.
 *   2. The Topology view renders the real per-shard placement: the system Raft
 *      group (shard 1) lists all three member nodes and elects a leader.
 *   3. The database-creation wizard exposes the Raft Configuration step with
 *      the Replication Factor input and the Dedicated WAL toggle — the
 *      cluster-only controls — proving they render against a real cluster, not
 *      just mocked raft state.
 *   4. The UI's view matches the daemon's own /admin/topology response (no
 *      UI↔backend drift).
 *   5. The Cluster admin page (Cluster.svelte) round-trips a REAL membership
 *      operation through the live daemon: requesting a snapshot for the
 *      system shard. Snapshot is the safe choice for this harness -- it is
 *      idempotent and needs no 4th, joinable node (unlike add-member, which
 *      the fixed 3-node fixture cannot supply a spare address/cert for).
 *   6. The Rebalance page (RebalancePanel.svelte) loads the live imbalance
 *      report against this freshly-booted, identically-loaded 3-node cluster
 *      -- the zero-hotspots case that is the COMMON path on every real
 *      cluster. This was previously blocked by a real product bug: Go's
 *      `json:"hotspots,omitempty"` drops a len-0 (non-nil) hotspots slice
 *      from the wire entirely, and RebalancePanel.svelte evaluated
 *      `report.hotspots.length` unconditionally, throwing "Cannot read
 *      properties of undefined (reading 'length')" and aborting the render.
 *      Fixed in pkg/api/rest/handlers_rebalance.go (Hotspots now serializes
 *      unconditionally, defended by a nil-slice guard) and hardened
 *      defensively in RebalancePanel.svelte (`report.hotspots ?? []`). This
 *      test is the regression guard for that fix at the real-backend tier;
 *      see e2e/rebalance-panel-web.spec.ts for the fast mocked-tier
 *      regression that reproduces the omitted-key wire shape directly.
 */

import { test, expect } from "@playwright/test";
import { startCluster, stopCluster, UI_BASE_URL, type DomCluster } from "./fixtures/dom-cluster.ts";

const LOAD_TIMEOUT = 20_000;

let cluster: DomCluster | null = null;

test.beforeEach(async () => {
  // Booting three nodes (certs, raft election) can take most of the 30 s
  // default on its own; give each test room for the boot plus its steps.
  test.setTimeout(120_000);
  cluster = await startCluster(3);
});

test.afterEach(async () => {
  // stopCluster signals the nodes but does not wait for them to exit; a node
  // still holding its fixed raft port fails the next test's boot with
  // "address already in use". Wait for every node to exit first.
  await Promise.all(
    (cluster?.procs ?? []).map(
      (p) =>
        new Promise<void>((resolve) => {
          if (p.exitCode !== null || p.signalCode !== null) return resolve();
          const kill = setTimeout(() => p.kill("SIGKILL"), 15_000);
          p.once("exit", () => {
            clearTimeout(kill);
            resolve();
          });
          p.kill("SIGTERM");
        }),
    ),
  );
  await stopCluster(cluster);
  cluster = null;
});

test("multi-node cluster: UI detects raft mode and shows Cluster + Topology nav", async ({ page }) => {
  await page.goto(UI_BASE_URL);
  // App shell loads (seal disabled → no barrier gate).
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  const aside = page.locator("aside");
  // Detected from the live /admin/topology probe (3 distinct nodes ⇒ raft).
  await expect(aside.getByText("Topology", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(aside.getByText("Cluster", { exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
});

test("multi-node cluster: Topology view renders all 3 member nodes for the system shard", async ({
  page,
  request,
}) => {
  expect(cluster).not.toBeNull();

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("aside").getByText("Topology", { exact: true }).click();

  const main = page.locator("main");
  await expect(main.getByTestId("topology-node-summary")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(main.getByTestId("topology-shards-table")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // The system Raft group (shard 1) must list all three member addresses.
  const shardRow = main.getByTestId("topology-shard-row").first();
  await expect(shardRow).toContainText("127.0.0.1:63011");
  await expect(shardRow).toContainText("127.0.0.1:63021");
  await expect(shardRow).toContainText("127.0.0.1:63031");

  // A leader must be elected (one row shows the Leader/Follower role badge).
  await expect(shardRow.locator(".badge")).toBeVisible();

  // No UI↔backend drift: the daemon's own topology reports the same 3 nodes.
  const resp = await request.get(`${cluster!.adminUrl}/admin/topology`, { ignoreHTTPSErrors: true });
  expect(resp.ok()).toBeTruthy();
  const topo = await resp.json();
  const ids = new Set<string>();
  for (const shard of topo.shards ?? []) {
    for (const id of Object.keys(shard.nodes ?? {})) ids.add(id);
  }
  expect(ids.size).toBe(3);
});

test("multi-node cluster: DB wizard exposes the Replication Factor picker and Dedicated WAL toggle", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Open the new-database wizard from the sidebar.
  await page.locator("aside").getByText("New Database").click();
  await expect(page.getByRole("heading", { name: "Database Name & Engine" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // Step 1 — Name.
  await page.locator("#db-name").fill("rf-probe");
  await page.getByRole("button", { name: "Next" }).click();

  // Step 2 — Storage Backend (default).
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Next" }).click();

  // Step 3 — Raft Configuration: the cluster-only step. This step only appears
  // when the UI detected raft mode from the live cluster topology.
  await expect(page.getByRole("heading", { name: "Raft Configuration" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  const main = page.locator("main");
  await expect(main.locator("#db-replication-factor")).toBeVisible();
  await expect(main.getByTestId("db-dedicated-wal-toggle")).toBeVisible();

  // The Replication Factor accepts a multi-node value (it is bounded by the
  // cluster node count, not pinned to 1 as it would be on a standalone server).
  await main.locator("#db-replication-factor").fill("3");
  await expect(main.locator("#db-replication-factor")).toHaveValue("3");
});

test("multi-node cluster: Cluster page requests a real snapshot for the system shard", async ({
  page,
}) => {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Navigate to the Cluster admin page (membership + snapshot management),
  // distinct from the read-only Topology view exercised above.
  await page.locator("aside").getByText("Cluster", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Cluster Peers" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  const main = page.locator("main");

  // The peers table must already reflect the live 3-node membership fetched
  // from GET /peers on mount, proving this page -- not just Topology -- talks
  // to the real cluster.
  await expect(main.locator("table tbody tr").first()).toBeVisible({ timeout: LOAD_TIMEOUT });
  const peerRowCount = await main.locator("table tbody tr").count();
  expect(peerRowCount).toBeGreaterThanOrEqual(3);

  // Drive the Snapshot card: shard 1 is the system Raft group every node
  // joined during cluster boot.
  const snapshotCard = main.locator(".card", { has: page.getByRole("heading", { name: "Request Snapshot" }) });
  await snapshotCard.getByPlaceholder("Shard ID").fill("1");
  await snapshotCard.getByRole("button", { name: "Request Snapshot" }).click();

  // A real POST /snapshot round-trip to the live leader must succeed --
  // confirmed by the success toast (not a mocked binding).
  await expect(page.getByRole("alert").getByText("Snapshot requested for shard 1")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // The form resets after a successful request.
  await expect(snapshotCard.getByPlaceholder("Shard ID")).toHaveValue("");
});

test("multi-node cluster: Rebalance page loads a real, zero-hotspots imbalance report without crashing", async ({
  page,
}) => {
  // Regression guard for the omitted-"hotspots"-key crash (see file header,
  // item 6): every node in this fixture is freshly booted and identically
  // loaded, so the live report is the zero-hotspots case that used to crash
  // the whole panel.
  const pageErrors: Error[] = [];
  page.on("pageerror", (err) => pageErrors.push(err));

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.locator("aside").getByText("Rebalance", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Rebalance Plan" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  const main = page.locator("main");

  // The three stat cards must render: Imbalance Score, Nodes, Hotspots. If
  // report.hotspots were still evaluated unconditionally, the whole section
  // (including the other two stat cards, which render first) would never
  // reach the DOM because the exception aborts the entire template render.
  await expect(main.getByText("Imbalance Score")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(main.getByText("Nodes", { exact: true })).toBeVisible();
  const hotspotsCard = main.locator(".surface-panel", { hasText: "Hotspots" });
  await expect(hotspotsCard).toBeVisible();
  await expect(hotspotsCard.getByText("0", { exact: true })).toBeVisible();

  // The Node Load table must also render (proves the render did not abort
  // partway through, before reaching the section after the stat cards).
  await expect(main.getByText("Node Load")).toBeVisible();

  // No unhandled JS exception (the "Cannot read properties of undefined
  // (reading 'length')" symptom) fired anywhere during the load.
  expect(pageErrors).toEqual([]);
});
