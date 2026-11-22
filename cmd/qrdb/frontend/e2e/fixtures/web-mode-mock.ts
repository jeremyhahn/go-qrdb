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
 * Web-mode test fixture.
 *
 * Unlike the standard Wails mock fixture, this fixture does NOT inject
 * window.go, so isDesktop() returns false and the application takes the
 * browser/web code path. REST API calls are intercepted via page.route()
 * so tests do not need a live qrdbd server.
 */

import { expect, type Page } from "@playwright/test";
import { test as base } from "./api-guard.ts";

// ---------------------------------------------------------------------------
// Configurable REST mock state
// ---------------------------------------------------------------------------

/** Minimal database info for mock responses. */
export interface WebMockDatabaseInfo {
  name: string;
  backend: string;
  status: string;
}

/** A single shard's placement, as returned by GET /admin/topology. */
export interface WebMockShard {
  shard_id: number;
  replica_id: number;
  is_leader: boolean;
  leader_id: number;
  /** Replica ID → Raft address. The distinct count drives standalone vs raft. */
  nodes: Record<string, string>;
}

/** Cluster topology returned by GET /admin/topology. */
export interface WebMockTopology {
  node_id: string;
  raft_address: string;
  shard_count: number;
  shards: WebMockShard[];
}

/** Controls what the REST mock returns. */
export interface WebMockState {
  /** Seal status returned by GET /v1/barrier/status. */
  sealStatus: {
    sealed: boolean;
    /** The daemon's initialized flag, which the SDK reads as is. */
    initialized: boolean;
    strategy: string;
    hardware_backed: boolean;
    initialized_at?: string;
  };
  /** Databases returned by GET /v1/databases/list. */
  databases: WebMockDatabaseInfo[];
  /**
   * Cluster topology returned by GET /admin/topology. The web UI derives its
   * server mode from this (a readable topology with any node ⇒ raft; the
   * database wizard's Raft steps need more than one). Defaults to a
   * single-node topology: Cluster nav shown, single-node wizard steps.
   */
  topology?: WebMockTopology;
  /**
   * A cluster seal holds the node: its barrier is unsealed, but GET
   * /v1/barrier/status is refused with 503 {"error":"barrier sealed",
   * "code":"SEALED"}, as the daemon's gate answers. "always" holds it from
   * the first request; "after-unseal" holds it once POST /v1/barrier/unseal
   * opened the node barrier (sealStatus is served until then). POST
   * /v1/barrier/cluster-unseal releases the hold, as does a POST to the
   * test-only /__mock/cluster-release (a release by another node or the CLI,
   * outside this page).
   */
  clusterHeld?: "always" | "after-unseal";
  /**
   * How many further status reads still answer 503 SEALED after POST
   * /v1/barrier/cluster-unseal cleared the record: the release reaches this
   * node a little later. Unset or 0 releases the hold at once.
   */
  clusterHeldReadsAfterClear?: number;
}

/** A single-node (standalone) topology. */
export const singleNodeTopology: WebMockTopology = {
  node_id: "web-mock-node",
  raft_address: "127.0.0.1:63001",
  shard_count: 1,
  shards: [
    { shard_id: 1, replica_id: 1, is_leader: true, leader_id: 1, nodes: { "1": "127.0.0.1:63001" } },
  ],
};

/** A 3-node (raft cluster) topology. */
export const threeNodeTopology: WebMockTopology = {
  node_id: "web-mock-node-1",
  raft_address: "127.0.0.1:63001",
  shard_count: 1,
  shards: [
    {
      shard_id: 1,
      replica_id: 1,
      is_leader: true,
      leader_id: 1,
      nodes: { "1": "127.0.0.1:63001", "2": "127.0.0.1:63002", "3": "127.0.0.1:63003" },
    },
  ],
};

/** Default state: server is initialized and unsealed, no databases. */
export const defaultWebMockState: WebMockState = {
  sealStatus: {
    sealed: false,
    initialized: true,
    strategy: "none",
    hardware_backed: false,
    initialized_at: "2026-01-01T00:00:00Z",
  },
  databases: [],
  topology: singleNodeTopology,
};

// ---------------------------------------------------------------------------
// Route injection
// ---------------------------------------------------------------------------

/**
 * Registers page.route() handlers that intercept all REST API calls made by
 * the RestClient. The handlers respond with JSON fixtures controlled by the
 * provided mock state.
 *
 * Note: window.go is deliberately NOT injected here. isDesktop() will return
 * false, routing the application through the web/browser code path.
 */
export async function injectWebRestMock(
  page: Page,
  state: WebMockState,
): Promise<void> {
  // Intercept health check (used by RestClient.connect).
  await page.route("**/health", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ status: "ok" }),
    });
  });

  // Intercept status endpoint (used for version info).
  await page.route("**/status", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ nodeId: "web-mock-node", status: "ok" }),
    });
  });

  // Intercept the Prometheus exposition the dashboard polls; an empty one
  // keeps the request off the network.
  await page.route("**/metrics", (route) => {
    void route.fulfill({ status: 200, contentType: "text/plain", body: "" });
  });
  // Per-page barrier state: the fixture's state object is shared, so the
  // unseal and cluster-unseal transitions live here.
  let sealed = state.sealStatus.sealed;
  let held = state.clusterHeld === "always";
  let clusterEpoch = 0;
  // Held status reads still owed after a clear, or null when none is pending.
  let heldReadsAfterClear: number | null = null;

  // Intercept barrier status endpoint (used to probe seal state).
  await page.route("**/v1/barrier/status", (route) => {
    if (held) {
      if (heldReadsAfterClear !== null) {
        heldReadsAfterClear -= 1;
        if (heldReadsAfterClear <= 0) {
          held = false;
          heldReadsAfterClear = null;
        }
      }
      void route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "barrier sealed", code: "SEALED" }),
      });
      return;
    }
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ...state.sealStatus, sealed }),
    });
  });

  if (state.clusterHeld !== undefined) {
    // Passphrase unseal of the node barrier: 204, as the daemon answers.
    await page.route("**/v1/barrier/unseal", (route) => {
      sealed = false;
      if (state.clusterHeld === "after-unseal") held = true;
      void route.fulfill({ status: 204, body: "" });
    });

    // Authenticated cluster unseal: clears the record and releases the hold.
    await page.route("**/v1/barrier/cluster-unseal", (route) => {
      const delay = state.clusterHeldReadsAfterClear ?? 0;
      if (held && delay > 0) {
        heldReadsAfterClear = delay;
      } else {
        held = false;
      }
      clusterEpoch += 1;
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ active: true, sealed: false, epoch: clusterEpoch, local: "serving" }),
      });
    });

    // Test-only hook: the hold is released outside this page (another node
    // or the CLI cleared the record). The page learns of it only by polling.
    await page.route("**/__mock/cluster-release", (route) => {
      held = false;
      heldReadsAfterClear = null;
      void route.fulfill({ status: 204, body: "" });
    });
  }

  // Intercept database list endpoint.
  await page.route("**/v1/databases/list", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ databases: state.databases }),
    });
  });

  // Intercept the cluster topology endpoint (drives web-mode raft detection).
  await page.route("**/admin/topology", (route) => {
    const topology = state.topology ?? singleNodeTopology;
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(topology),
    });
  });

  // The Dashboard Metrics card reads the Prometheus exposition at GET /metrics.
  await page.route(/\/metrics(\?.*)?$/, (route) => {
    void route.fulfill({
      status: 200,
      contentType: "text/plain; version=0.0.4",
      body: "# HELP go_goroutines Number of goroutines that currently exist.\n# TYPE go_goroutines gauge\ngo_goroutines 8\n",
    });
  });

  // Intercept capabilities endpoint (used by Dashboard on every load).
  await page.route("**/capabilities", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        kv: true,
        mvcc: true,
        transaction: true,
        batch: true,
        watch: true,
        lease: true,
        timeSeries: false,
        vector: false,
      }),
    });
  });

  // Intercept the tenant list (loaded by the admin views' tenant selectors).
  // A spec that asserts on tenants routes its own list, which takes precedence.
  await page.route("**/v1/tenants/list**", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ tenants: [] }),
    });
  });

  // Intercept the role list (loaded by the user forms' role pickers).
  await page.route("**/roles/list**", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ roles: [], total: 0, hasMore: false }),
    });
  });

  // The connect-time probes of the admin views' read routes (each view shows
  // only when its route is not refused). Empty answers keep every view on
  // offer; a spec that asserts on one of these routes its own, which takes
  // precedence.
  const probeAnswers: ReadonlyArray<readonly [string, unknown]> = [
    ["**/peers", { peers: [] }],
    ["**/v1/rebalance/report", {}],
    ["**/users/list**", { users: [], total: 0, hasMore: false }],
    ["**/v1/volume-pools/list**", { pools: [] }],
    ["**/certs/list**", { certs: [] }],
    ["**/v1/credentials", { names: [] }],
    ["**/v1/backups/list**", { backups: [] }],
    ["**/v1/audit/events**", { events: [] }],
  ];
  for (const [pattern, body] of probeAnswers) {
    await page.route(pattern, (route) => {
      void route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    });
  }
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

/**
 * Extended test fixture for pure web mode.
 *
 * Provides a page with REST API routes mocked and no Wails bindings present,
 * so the application runs in web/browser mode throughout the test.
 */
export const webTest = base.extend<{
  webMockState: WebMockState;
}>({
  webMockState: [defaultWebMockState, { option: true }],
  page: async ({ page, webMockState }, use) => {
    await injectWebRestMock(page, webMockState);
    await use(page);
  },
});

export { expect };
