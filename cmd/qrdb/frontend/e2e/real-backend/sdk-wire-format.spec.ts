// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SDK wire-format real-backend spec.
 *
 * Starts a real qrdbd daemon and exercises the TypeScript SDK's RestClient
 * directly (no browser, no mock) to verify that the snake_case → camelCase
 * transformer in fetchJSON() correctly populates every camelCase field in the
 * declared SDK response types.
 *
 * This test proves the regression found by the Chrome UI integration test:
 * the Dashboard showed "Follower" even on the leader node because
 * HealthResponse.isLeader was undefined (the wire sends is_leader).
 */

import { test, expect } from "./fixtures/daemon.ts";
import { RestClient } from "../../../../../sdk/typescript/src/rest.js";

// The SDK RestClient uses Node's global fetch, which rejects the daemon's
// self-signed TLS certificate (Playwright's ignoreHTTPSErrors only applies to
// its own request/page contexts, not the SDK's fetch). The real-backend daemon
// always uses a self-signed cert, so accept it for this worker. This mirrors
// what ignoreHTTPSErrors does for the other real-backend specs.
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = "0";

test.describe("SDK RestClient wire-format (real daemon)", () => {
  test("health() returns isLeader as boolean, not undefined", async ({
    daemon,
  }) => {
    const client = new RestClient(daemon.adminUrl);
    await client.connection.connect(daemon.adminUrl);

    const health = await client.admin.health();

    // status is always present — basic sanity.
    expect(health.status).toBe("ok");

    // isLeader must be a boolean (true on a single-node cluster).
    // Before the fix this was undefined because the wire sends is_leader.
    expect(typeof health.isLeader).toBe("boolean");
    expect(health.isLeader).toBe(true);
  });

  test("status() populates all camelCase fields from snake_case wire", async ({
    daemon,
  }) => {
    const client = new RestClient(daemon.adminUrl);
    await client.connection.connect(daemon.adminUrl);

    const status = await client.admin.status();

    // nodeId was previously undefined (wire: node_id).
    expect(typeof status.nodeId).toBe("string");
    expect(status.nodeId.length).toBeGreaterThan(0);

    // raftAddress was previously undefined (wire: raft_address).
    expect(typeof status.raftAddress).toBe("string");
    expect(status.raftAddress.length).toBeGreaterThan(0);

    // isLeader was previously undefined (wire: is_leader).
    expect(typeof status.isLeader).toBe("boolean");

    // leaderId was previously undefined (wire: leader_id).
    expect(typeof status.leaderId).toBe("number");

    // shardCount was previously undefined (wire: shard_count).
    expect(typeof status.shardCount).toBe("number");
    expect(status.shardCount).toBeGreaterThanOrEqual(1);

    // isDraining was previously undefined (wire: is_draining).
    expect(typeof status.isDraining).toBe("boolean");
  });

  test("peers() populates shardId, leaderId, replicaId, isLeader", async ({
    daemon,
  }) => {
    const client = new RestClient(daemon.adminUrl);
    await client.connection.connect(daemon.adminUrl);

    const peers = await client.admin.peers();

    expect(Array.isArray(peers.shards)).toBe(true);
    expect(peers.shards.length).toBeGreaterThanOrEqual(1);

    const shard = peers.shards[0];
    // shardId was previously undefined (wire: shard_id).
    expect(typeof shard.shardId).toBe("number");
    // leaderId was previously undefined (wire: leader_id).
    expect(typeof shard.leaderId).toBe("number");

    expect(Array.isArray(shard.peers)).toBe(true);
    const peer = shard.peers[0];
    // replicaId was previously undefined (wire: replica_id).
    expect(typeof peer.replicaId).toBe("number");
    expect(typeof peer.address).toBe("string");
    // isLeader was previously undefined (wire: is_leader).
    expect(typeof peer.isLeader).toBe("boolean");
  });

  test("connection.getVersion() returns nodeId as server version", async ({
    daemon,
  }) => {
    const client = new RestClient(daemon.adminUrl);
    await client.connection.connect(daemon.adminUrl);

    const ver = client.connection.getVersion();
    // Before the fix: ver.server was '' because status.nodeId was undefined.
    expect(ver["server"]).toBeTruthy();
    expect(ver["server"].length).toBeGreaterThan(0);
  });

  test("capabilities() preserves camelCase timeSeries field", async ({
    daemon,
  }) => {
    const client = new RestClient(daemon.adminUrl);
    await client.connection.connect(daemon.adminUrl);

    const caps = await client.admin.capabilities();

    // kv is always enabled on the default binary.
    expect(caps.kv).toBe(true);
    // timeSeries has a Go json tag of "timeSeries" (already camelCase).
    // The transformer must not mangle it to "timeSeries" → "timeSeries" (no-op).
    expect(typeof caps.timeSeries).toBe("boolean");
  });
});

// ---------------------------------------------------------------------------
// Outbound transform regression: SDK camelCase request fields must be
// serialized as snake_case so that Go's json:"snake_case" struct tags match.
//
// Bug: client.tenants.create({ isolationType: "prefix" }) was sending
// `{"isolationType":"prefix"}` — the Go handler's `json:"isolation_type"` tag
// silently ignored the unknown key and defaulted to "barrier".
// After the fix, jsonBody() applies deepCamelToSnake before serialization.
// ---------------------------------------------------------------------------

test.describe(
  "SDK outbound transform — tenant isolationType → isolation_type (real daemon)",
  () => {
    test(
      "create with isolationType:'prefix' persists as prefix, not barrier",
      async ({ daemon }) => {
        const client = new RestClient(daemon.adminUrl);
        await client.connection.connect(daemon.adminUrl);

        const id = `sdk-iso-prefix-${Date.now()}`;
        const resp = await client.tenants.create({
          id,
          name: "SDK Prefix Tenant",
          isolationType: "prefix",
        });

        // The SDK response is snake→camel transformed on inbound.
        // TenantInfo.isolationType should reflect what the server persisted.
        expect(resp.tenant.isolationType).toBe("prefix");

        // Fetch directly to confirm server-side persistence.
        const got = await client.tenants.get(id);
        expect(got.tenant.isolationType).toBe("prefix");

        // Cleanup.
        await client.tenants.delete(id);
      },
    );

    test(
      "create with isolationType:'barrier' is rejected on a no-seal daemon (422)",
      async ({ daemon }) => {
        const client = new RestClient(daemon.adminUrl);
        await client.connection.connect(daemon.adminUrl);

        const id = `sdk-iso-barrier-${Date.now()}`;
        // The shared real-backend daemon runs WITHOUT the seal subsystem, and
        // barrier isolation is fail-closed: the server rejects the request
        // with 422 (ErrBarrierIsolationUnavailable). This still proves the
        // outbound transform: if the SDK had sent `isolationType` (camelCase)
        // instead of `isolation_type`, the server would have silently
        // defaulted to "prefix" and the create would have SUCCEEDED.
        let statusCode: number | undefined;
        try {
          await client.tenants.create({
            id,
            name: "SDK Barrier Tenant",
            isolationType: "barrier",
          });
        } catch (err) {
          statusCode = (err as { statusCode?: number }).statusCode;
        }
        expect(statusCode).toBe(422);
      },
    );

    test(
      "create without isolationType defaults to prefix (server default)",
      async ({ daemon }) => {
        const client = new RestClient(daemon.adminUrl);
        await client.connection.connect(daemon.adminUrl);

        const id = `sdk-iso-default-${Date.now()}`;
        const resp = await client.tenants.create({
          id,
          name: "SDK Default Tenant",
        });

        // Server default is "prefix" when isolation_type is omitted —
        // barrier isolation is strictly opt-in.
        expect(resp.tenant.isolationType).toBe("prefix");

        await client.tenants.delete(id);
      },
    );
  },
);
