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
   * server mode from this (more than one distinct node ⇒ raft). Defaults to a
   * single-node topology so the UI resolves to standalone.
   */
  topology?: WebMockTopology;
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

  // Intercept barrier status endpoint (used to probe seal state).
  await page.route("**/v1/barrier/status", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(state.sealStatus),
    });
  });

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
