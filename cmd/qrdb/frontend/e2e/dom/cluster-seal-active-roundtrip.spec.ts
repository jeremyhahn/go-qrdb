// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * cluster-seal-active-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Seal Manager's active cluster seal card (coverage-matrix group
 * 10: seal.status().clusterSeal and seal.clusterUnseal) against a live
 * three-node, passphrase-sealed qrdbd cluster. The raft transport carries a
 * node identity, so the feature gate activates cluster-seal and a seal made
 * through node 1 is recorded in the system shard and reaches every node.
 *
 * The UI talks to node 1 (the Vite proxy target). A seal from the UI writes
 * the cluster record and every node seals itself; a sealed node withholds the
 * record. Each node is then unsealed with its own credential (node 1 in the
 * UI, nodes 2 and 3 through the REST API, which the UI cannot reach) and stays
 * held: it answers status with 503 SEALED and admits only the cluster unseal,
 * so the card shows the held state and "Clear Cluster Seal" with no record.
 * clusterUnseal from the UI clears the record; every node serves again, and
 * the card's record (epoch, principal, node, at, local) matches GET
 * /v1/barrier/status on node 1 and node 2.
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  startCluster,
  stopCluster,
  CLUSTER_PASSPHRASE,
  CLUSTER_SO_PIN,
  UI_BASE_URL,
  type DomCluster,
} from "./fixtures/dom-cluster.ts";

test.setTimeout(180_000);

interface ClusterSealReport {
  active: boolean;
  sealed: boolean;
  epoch: number;
  principal?: string;
  principal_tenant?: string;
  node?: string;
  at?: string;
  local?: string;
}

interface SealStatus {
  sealed: boolean;
  strategy?: string;
  cluster_seal: ClusterSealReport;
}

let cluster: DomCluster | null = null;

test.beforeEach(async () => {
  cluster = await startCluster(3, { seal: "passphrase" });
});

test.afterEach(async () => {
  await stopCluster(cluster);
  cluster = null;
});

async function sealStatus(request: APIRequestContext, url: string): Promise<SealStatus> {
  const res = await request.get(`${url}/v1/barrier/status`, { ignoreHTTPSErrors: true });
  expect(res.status(), `seal status ${url}: ${await res.text()}`).toBe(200);
  return (await res.json()) as SealStatus;
}

async function expectToast(page: Page, text: string): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text })).toBeVisible({ timeout: 15_000 });
}

test("three nodes: the active cluster seal card follows a cluster-wide seal and its clusterUnseal", async ({ page, request }) => {
  const [node1, node2, node3] = cluster!.adminUrls as [string, string, string];

  // Every node runs the cluster-seal monitor, and the record starts clear.
  for (const url of [node1, node2, node3]) {
    const s = await sealStatus(request, url);
    expect(s.sealed).toBe(false);
    expect(s.cluster_seal.active).toBe(true);
    expect(s.cluster_seal.sealed).toBe(false);
  }

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();

  // The active card with a clear record: no cluster unseal action.
  const card = page.getByTestId("cluster-seal-status");
  const clear = await sealStatus(request, node1);
  await expect(card).toContainText(`Cluster seal: record clear (epoch ${clear.cluster_seal.epoch})`);
  await expect(card).toContainText(`this node is ${clear.cluster_seal.local}`);
  await expect(page.getByTestId("cluster-unseal-btn")).toHaveCount(0);

  // Seal through the UI: with cluster-seal active this writes the record.
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  const soPin = page.getByTestId("seal-so-pin");
  if (await soPin.isVisible()) await soPin.fill(CLUSTER_SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(page.getByText("Enter the barrier passphrase to unseal the encryption barrier.")).toBeVisible({ timeout: 15_000 });

  // The seal reaches every node, not only the one it went through.
  for (const url of [node1, node2, node3]) {
    await expect
      .poll(
        async () => {
          // A node can answer 503 for a moment: it has read the record and
          // closed its gate but not yet sealed its barrier.
          const r = await request.get(`${url}/v1/barrier/status`, { ignoreHTTPSErrors: true });
          return r.status() === 200 ? ((await r.json()) as SealStatus).sealed : false;
        },
        { timeout: 30_000, message: `${url} sealed` },
      )
      .toBe(true);
  }

  // A sealed node withholds the record (and its strategy) until it is
  // unsealed.
  const sealed1 = await sealStatus(request, node1);
  expect(sealed1.cluster_seal.active).toBe(false);

  // Unseal each node with its own credential: node 1 in the UI, the others
  // through the API, which the UI cannot reach.
  await page.getByPlaceholder("Enter passphrase").fill(CLUSTER_PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();
  for (const url of [node2, node3]) {
    const res = await request.post(`${url}/v1/barrier/unseal`, {
      data: { secret: Buffer.from(CLUSTER_PASSPHRASE).toString("base64") },
      ignoreHTTPSErrors: true,
    });
    expect(res.ok(), `unseal ${url}: ${res.status()} ${await res.text()}`).toBeTruthy();
  }

  // Unsealed, every node is still held by the record: it answers no status
  // question, and the card offers the cluster unseal with no record.
  for (const url of [node1, node2, node3]) {
    await expect
      .poll(
        async () => {
          const r = await request.get(`${url}/v1/barrier/status`, { ignoreHTTPSErrors: true });
          return `${r.status()} ${await r.text()}`;
        },
        { timeout: 30_000, message: `${url} held` },
      )
      .toContain('503 {"error":"barrier sealed","code":"SEALED"}');
  }
  await expect(page.getByTestId("cluster-seal-held")).toBeVisible({ timeout: 20_000 });
  await expect(card).toContainText("The node barrier is sealed cluster-wide.");
  await expect(page.getByTestId("cluster-seal-record")).toHaveCount(0);
  await expect(page.getByText("Enter the barrier passphrase to unseal the encryption barrier.")).toHaveCount(0);

  // Clear the cluster seal through the UI (seal.clusterUnseal).
  await page.getByTestId("cluster-unseal-btn").click();
  await expectToast(page, "Cluster seal cleared");
  await expect(page.getByTestId("cluster-seal-held")).toHaveCount(0);
  await expect(page.getByTestId("cluster-unseal-btn")).toHaveCount(0);

  // Every node serves again: unsealed, record clear.
  for (const url of [node1, node2, node3]) {
    await expect
      .poll(
        async () => {
          const r = await request.get(`${url}/v1/barrier/status`, { ignoreHTTPSErrors: true });
          if (r.status() !== 200) return { status: r.status() };
          const s = (await r.json()) as SealStatus;
          return { status: 200, sealed: s.sealed, record: s.cluster_seal.sealed, local: s.cluster_seal.local };
        },
        { timeout: 30_000, message: `${url} unsealed` },
      )
      .toEqual({ status: 200, sealed: false, record: false, local: "serving" });
  }

  // The card shows the clearing record field for field, as two nodes report
  // it: the record is replicated, so node 2 reads the one node 1 wrote.
  const s1 = await sealStatus(request, node1);
  const s2 = await sealStatus(request, node2);
  const rec = s1.cluster_seal;
  expect(rec.active).toBe(true);
  expect(rec.epoch).toBeGreaterThan(clear.cluster_seal.epoch);
  expect(rec.node).toBeTruthy();
  expect(rec.at).toBeTruthy();
  expect(s2.cluster_seal).toEqual(rec);

  await expect(card).toContainText(`Cluster seal: record clear (epoch ${rec.epoch}); this node is ${rec.local}.`);
  await expect(page.getByTestId("cluster-seal-node")).toHaveText(rec.node!);
  await expect(page.getByTestId("cluster-seal-at")).toHaveText(rec.at!);
  if (rec.principal) {
    const want = rec.principal_tenant ? `${rec.principal} (tenant ${rec.principal_tenant})` : rec.principal;
    await expect(page.getByTestId("cluster-seal-principal")).toHaveText(want);
  } else {
    await expect(page.getByTestId("cluster-seal-principal")).toHaveCount(0);
  }
});
