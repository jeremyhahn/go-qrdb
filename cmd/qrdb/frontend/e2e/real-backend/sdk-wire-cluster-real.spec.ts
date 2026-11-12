// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SDK wire check for the cluster and barrier database fields (#1478).
 *
 * Boots the 3-node DOM cluster fixture (auth off) and drives the
 * TypeScript SDK's RestClient directly -- no browser, no mock. Every
 * database method and create field the single-node wire spec could not
 * reach is set, then read back through database.get / barrierStatus.
 *
 * The barrier needs a dedicated store (an explicit dataDir) and a node
 * barrier manager, which only a sealed cluster configures, so the barrier
 * checks run on the passphrase-sealed cluster.
 */

import { test, expect } from "@playwright/test";
import {
  startCluster,
  stopCluster,
  type ClusterSealMode,
  type DomCluster,
} from "../dom/fixtures/dom-cluster.ts";
import { RestClient } from "../../../../../sdk/typescript/src/rest.js";

// The fixture serves a self-signed certificate; the SDK uses Node's fetch.
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = "0";

test.describe.configure({ mode: "serial" });

/** Status code of the SDK error a rejected call throws. */
async function statusOf(p: Promise<unknown>): Promise<number | undefined> {
  try {
    await p;
    return undefined;
  } catch (err) {
    return (err as { statusCode?: number }).statusCode;
  }
}

function useCluster(seal: ClusterSealMode): {
  cluster: () => DomCluster;
  client: () => RestClient;
} {
  let cluster: DomCluster | null = null;
  let client: RestClient | null = null;
  test.beforeAll(async () => {
    test.setTimeout(180_000);
    cluster = await startCluster(3, { seal });
    client = new RestClient(cluster.adminUrl);
    await client.connection.connect(cluster.adminUrl);
  });
  test.afterAll(async () => {
    await stopCluster(cluster);
  });
  return { cluster: () => cluster!, client: () => client! };
}

test.describe("SDK database cluster fields (3-node cluster)", () => {
  const ctx = useCluster("none");

  test("create raft, replicas and dedicatedWal round-trip", async () => {
    const db = ctx.client().database;
    const created = await db.create({
      name: "wire_dist",
      backend: "raft",
      replicas: 3,
      dedicatedWal: true,
      raft: { clusterMode: "distributed" },
    });
    expect(created.replicas).toBe(3);
    const got = await db.get("wire_dist");
    expect(got.backend).toBe("raft");
    expect(got.replicas).toBe(3);
    expect(got.dedicatedWal).toBe(true);
    expect(got.raft?.clusterMode).toBe("distributed");
    expect(typeof got.shardId).toBe("number");
  });

  test("create raft local with localNodeCount round-trips", async () => {
    const db = ctx.client().database;
    await db.create({
      name: "wire_local",
      backend: "raft",
      raft: { clusterMode: "local", localNodeCount: 3 },
    });
    const got = await db.get("wire_local");
    expect(got.raft?.clusterMode).toBe("local");
    expect(got.raft?.localNodeCount).toBe(3);
  });

  test("updateRaft sets the cluster mode both ways", async () => {
    const db = ctx.client().database;
    await db.create({ name: "wire_r1", backend: "raft" });
    expect((await db.get("wire_r1")).raft).toBeUndefined();

    const set = await db.updateRaft("wire_r1", {
      clusterMode: "local",
      localNodeCount: 3,
    });
    expect(set.raft?.clusterMode).toBe("local");
    let got = await db.get("wire_r1");
    expect(got.raft?.clusterMode).toBe("local");
    expect(got.raft?.localNodeCount).toBe(3);

    await db.updateRaft("wire_r1", { clusterMode: "none" });
    got = await db.get("wire_r1");
    expect(got.raft?.clusterMode).toBe("none");
    expect(got.raft?.localNodeCount).toBeUndefined();
  });

  test("create dataDir round-trips", async () => {
    const db = ctx.client().database;
    const dir = `${ctx.cluster().dataDirs[0]}/wire-file-db`;
    await db.create({ name: "wire_file", backend: "file", dataDir: dir });
    expect((await db.get("wire_file")).dataDir).toBe(dir);
  });

  test("updateProtocol round-trips", async () => {
    const db = ctx.client().database;
    await db.create({ name: "wire_proto", backend: "memory" });
    await db.updateProtocol("wire_proto", { mysql: true, postgresql: true });
    const got = await db.get("wire_proto");
    expect(got.protocol?.mysql).toBe(true);
    expect(got.protocol?.postgresql).toBe(true);
  });

  test("updateConnection round-trips a peer node as the remote", async () => {
    const db = ctx.client().database;
    const address = ctx.cluster().adminUrls[1]!;
    await db.create({ name: "wire_conn", backend: "memory" });
    await db.updateConnection("wire_conn", { transport: "rest", address });
    const got = await db.get("wire_conn");
    expect(got.connection?.transport).toBe("rest");
    expect(got.connection?.address).toBe(address);
  });

  test("create connection and mode are not sent (server refuses them)", async () => {
    // The SDK strips mode and connection from create: the server's strict
    // decoder refuses both, so a remote database is configured afterwards
    // with updateConnection. Server gap, recorded in #1478.
    const db = ctx.client().database;
    const created = await db.create({
      name: "wire_remote",
      backend: "memory",
      mode: "remote",
      connection: { transport: "rest", address: ctx.cluster().adminUrls[1]! },
    });
    expect(created.mode).toBe("embedded");
    expect(created.connection).toBeUndefined();
  });

  test("scale round-trips targetReplicas", async () => {
    // Server gap (#1478): with auth off the request carries no tenant and
    // ScaleForTenant answers 500 "tenant ID required". Remove test.fail
    // once the server scales the caller's (system) tenant.
    test.fail();
    const db = ctx.client().database;
    await db.create({ name: "wire_scale", backend: "raft", replicas: 3 });
    const scaled = await db.scale("wire_scale", 1);
    expect(scaled.replicas).toBe(1);
  });
});

test.describe("SDK database barrier fields (passphrase-sealed cluster)", () => {
  const ctx = useCluster("passphrase");

  test("create barrier round-trips into get and barrierStatus", async () => {
    const db = ctx.client().database;
    await db.create({
      name: "wire_bar",
      backend: "pebble",
      dataDir: `${ctx.cluster().dataDirs[0]}/wire-bar`,
      barrier: { enabled: true, mode: "passphrase", secret: "wire-bar-secret-01" },
    });
    const got = await db.get("wire_bar");
    expect(got.barrier?.enabled).toBe(true);
    expect(got.barrier?.mode).toBe("passphrase");

    const status = await db.barrierStatus("wire_bar");
    expect(status.sealed).toBe(false);
    expect(status.strategy).toBe("passphrase");
    expect(status.hardwareBacked).toBe(false);
    expect(typeof status.initializedAt).toBe("string");
    expect(typeof status.clusterSeal?.active).toBe("boolean");
  });

  test("updateBarrier accepts the same config and refuses a toggle", async () => {
    const db = ctx.client().database;
    const same = await db.updateBarrier("wire_bar", {
      enabled: true,
      mode: "passphrase",
    });
    expect(same.barrier?.enabled).toBe(true);
    expect(same.barrier?.mode).toBe("passphrase");
    // No re-encryption migration exists: changing the barrier after
    // create is a 409, in both directions.
    expect(await statusOf(db.updateBarrier("wire_bar", { enabled: false }))).toBe(409);
    await db.create({
      name: "wire_plain",
      backend: "pebble",
      dataDir: `${ctx.cluster().dataDirs[0]}/wire-plain`,
    });
    expect(
      await statusOf(
        db.updateBarrier("wire_plain", { enabled: true, mode: "passphrase" }),
      ),
    ).toBe(409);
    expect((await db.get("wire_bar")).barrier?.enabled).toBe(true);
  });
});
