// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Rebalance panel real-backend spec.
 *
 * SINGLE-NODE LIMITATION: fixtures/daemon.ts spawns exactly one qrdbd
 * process with a fixed single-member peer set (`--peers 1=127.0.0.1:<port>
 * --replica-id 1`, see ensureDaemon). It has no facility to stand up a
 * multi-node cluster, and the real-backend tier runs single-worker /
 * non-parallel against that one shared daemon, so spawning a second
 * cluster member here is not practical without changing the shared
 * fixture used by all 28+ other real-backend specs. This spec therefore
 * exercises exactly what a real, honest single-node backend does: report
 * status (GET /v1/rebalance/report, GET /v1/rebalance/shards/{id}) and
 * generate a plan (POST /v1/rebalance/plan) — and asserts that the plan is
 * a genuine, empty no-op (there is no second node to move a shard to)
 * rather than a faked multi-node move. See
 * pkg/service/rebalance_service.go CreateRebalancePlan: it short-circuits
 * to an empty-moves plan whenever len(report.Nodes) < 2. Executing that
 * empty plan (POST /v1/rebalance/execute) is still driven against the
 * live backend end-to-end, and honestly reports zero completed/failed
 * moves rather than mocking a transfer.
 *
 * Nothing in this spec uses page.route or wails-mock — every request goes
 * to the live daemon spawned by fixtures/daemon.ts.
 */

import { test, expect } from "./fixtures/daemon.ts";

interface ShardInfoResponse {
  shard_id: number;
  replica_id: number;
  is_leader: boolean;
  leader_id: number;
  nodes: Record<string, string>;
}

interface TopologyResponse {
  node_id: string;
  raft_address: string;
  shard_count: number;
  shards: ShardInfoResponse[];
}

interface NodeLoadResponse {
  node_id: string;
  address: string;
  shard_count: number;
  total_keys: number;
  total_bytes: number;
  leader_count: number;
}

interface ImbalanceReportResponse {
  nodes: NodeLoadResponse[];
  imbalance_score: number;
  hotspots: string[];
}

interface RebalanceMoveResponse {
  shard_id: number;
  from_node: string;
  to_node: string;
  estimated_bytes: number;
}

interface RebalancePlanResponse {
  moves: RebalanceMoveResponse[];
  estimated_duration_ms: number;
  current_imbalance: number;
}

interface RebalanceExecutionResponse {
  moves_completed: number;
  moves_failed: number;
  duration_ms: number;
  final_imbalance: number;
}

interface ShardStatsResponse {
  shard_id: number;
  key_count: number;
  byte_count: number;
  leader_id: number;
  replica_count: number;
}

async function getTopology(
  request: import("@playwright/test").APIRequestContext,
  daemon: { adminUrl: string },
): Promise<TopologyResponse> {
  const resp = await request.get(`${daemon.adminUrl}/admin/topology`);
  expect(resp.ok()).toBe(true);
  return resp.json() as Promise<TopologyResponse>;
}

test.describe("Rebalance panel (real backend — single-node)", () => {
  test("GET /v1/rebalance/report reflects the real single-node topology", async ({
    request,
    daemon,
  }) => {
    const topo = await getTopology(request, daemon);
    expect(topo.shard_count).toBeGreaterThan(0);

    const resp = await request.get(`${daemon.adminUrl}/v1/rebalance/report`);
    expect(resp.ok()).toBe(true);
    const report = (await resp.json()) as ImbalanceReportResponse;

    expect(Array.isArray(report.nodes)).toBe(true);
    // A single-node fixture daemon has exactly one distinct node address.
    expect(report.nodes.length).toBe(1);
    expect(report.nodes[0]!.address).toBe(topo.raft_address);
    expect(report.nodes[0]!.shard_count).toBe(topo.shard_count);
    // A "cluster" of one node is trivially, honestly balanced.
    expect(report.imbalance_score).toBe(0);
    expect(Array.isArray(report.hotspots)).toBe(true);
    expect(report.hotspots.length).toBe(0);
  });

  test("POST /v1/rebalance/plan on a single node returns an honest empty no-op plan", async ({
    request,
    daemon,
  }) => {
    const resp = await request.post(`${daemon.adminUrl}/v1/rebalance/plan`, {
      data: {},
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.ok()).toBe(true);
    const plan = (await resp.json()) as RebalancePlanResponse;

    // service.RebalanceService.CreateRebalancePlan returns Moves == nil
    // whenever len(report.Nodes) < 2 — there is no second node to receive a
    // shard, so the real backend must not fabricate a move. This is the
    // genuine single-node contract, not a stand-in for a multi-node result.
    expect(plan.moves ?? []).toEqual([]);
    expect(plan.current_imbalance).toBe(0);
  });

  test("POST /v1/rebalance/execute on the no-op plan performs zero real shard moves", async ({
    request,
    daemon,
  }) => {
    const planResp = await request.post(
      `${daemon.adminUrl}/v1/rebalance/plan`,
      { data: {}, headers: { "Content-Type": "application/json" } },
    );
    expect(planResp.ok()).toBe(true);
    const plan = (await planResp.json()) as RebalancePlanResponse;
    expect(plan.moves ?? []).toEqual([]);

    const execResp = await request.post(
      `${daemon.adminUrl}/v1/rebalance/execute`,
      { data: plan, headers: { "Content-Type": "application/json" } },
    );
    expect(execResp.ok()).toBe(true);
    const result = (await execResp.json()) as RebalanceExecutionResponse;

    // An empty plan against the live backend completes with zero
    // completed/failed moves — an honest no-op execution, not a mocked
    // "Rebalance complete" toast standing in for real cluster action.
    expect(result.moves_completed).toBe(0);
    expect(result.moves_failed).toBe(0);
    expect(result.final_imbalance).toBe(0);
  });

  test("GET /v1/rebalance/shards/{id} returns real per-shard statistics", async ({
    request,
    daemon,
  }) => {
    const topo = await getTopology(request, daemon);
    expect(topo.shards.length).toBeGreaterThan(0);
    const shardID = topo.shards[0]!.shard_id;

    const resp = await request.get(
      `${daemon.adminUrl}/v1/rebalance/shards/${shardID}`,
    );
    expect(resp.ok()).toBe(true);
    const stats = (await resp.json()) as ShardStatsResponse;

    expect(stats.shard_id).toBe(shardID);
    // Single-node cluster: this node is the only, and therefore leading,
    // replica for every shard it hosts.
    expect(stats.replica_count).toBe(1);
    expect(stats.leader_id).toBeGreaterThan(0);
  });

  test("GET /v1/rebalance/shards/{id} returns 400 for a non-numeric shard id", async ({
    request,
    daemon,
  }) => {
    const resp = await request.get(
      `${daemon.adminUrl}/v1/rebalance/shards/not-a-number`,
    );
    expect(resp.status()).toBe(400);
  });
});
