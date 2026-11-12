/**
 * SDK RestClient wire round-trip against a live daemon for leases, locks,
 * transactions, batch execute and backups (#1478).
 *
 * Every call sets each optional request field the SDK exposes, then reads
 * the result back and asserts that each field arrives typed and populated:
 * a field the SDK declares under a name the server does not send arrives
 * undefined here, and a request field the server ignores fails the
 * read-back.
 */
import { test, expect } from "./fixtures/daemon.ts";
import { RestClient } from "../../../../../sdk/typescript/src/rest.js";

// The SDK RestClient uses Node's global fetch, which rejects the daemon's
// self-signed TLS certificate; accept it for this worker, as
// sdk-wire-format.spec.ts does.
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = "0";

async function connect(adminUrl: string): Promise<RestClient> {
  const client = new RestClient(adminUrl);
  await client.connection.connect(adminUrl);
  return client;
}

function unique(label: string): string {
  return `wire-${label}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

test.describe("SDK wire round-trip: leases (real daemon)", () => {
  test("grant, timeToLive, keepAlive, list and revoke carry every LeaseInfo field", async ({ mvccDaemon }) => {
    const client = await connect(mvccDaemon.adminUrl);

    const granted = await client.lease.grant(120);
    expect(typeof granted.id).toBe("number");
    expect(granted.id).toBeGreaterThan(0);
    expect(granted.ttl).toBeGreaterThan(0);
    expect(granted.ttl).toBeLessThanOrEqual(120);

    // Attach a key through a batch put with leaseId, so keys round-trips.
    const key = unique("lease-key");
    await client.batch.execute([{ type: 0, key, value: "v", leaseId: granted.id }]);

    const ttl = await client.lease.timeToLive(granted.id);
    expect(ttl.id).toBe(granted.id);
    expect(ttl.grantedTtl).toBe(120);
    expect(ttl.remainingTtl).toBeGreaterThan(0);
    expect(ttl.remainingTtl).toBeLessThanOrEqual(120);
    expect(ttl.keys).toContain(key);

    const kept = await client.lease.keepAlive(granted.id);
    expect(kept.id).toBe(granted.id);
    expect(kept.ttl).toBeGreaterThan(0);

    const all = await client.lease.list();
    expect(all.map((l) => l.id)).toContain(granted.id);

    await client.lease.revoke(granted.id);
    const after = await client.lease.list();
    expect(after.map((l) => l.id)).not.toContain(granted.id);
    // Revoking the lease deletes the key attached to it.
    const resp = await client.batch.execute([{ type: 1, key }]);
    expect(resp.responses[0].kvs ?? []).toHaveLength(0);
  });
});

test.describe("SDK wire round-trip: locks (real daemon)", () => {
  test("acquire, re-acquire, status, list, extend and release carry every LockInfo field", async ({ mvccDaemon }) => {
    const client = await connect(mvccDaemon.adminUrl);
    const key = unique("lock");
    const holder = unique("holder");

    const info = await client.lock.acquire({
      key,
      holder_id: holder,
      ttl: 60,
      reentrant: true,
    });
    expect(info.key).toBe(key);
    expect(info.holderId).toBe(holder);
    expect(info.reentrant).toBe(true);
    expect(info.ttl).toBe(60);
    expect(info.acquireCount).toBe(1);
    expect(info.fencingToken).toBeGreaterThan(0);
    expect(info.leaseId).toBeGreaterThan(0);
    expect(info.acquiredAt).toBeGreaterThan(0);

    // Reentrant re-acquisition by the same holder bumps acquireCount.
    const again = await client.lock.acquire({
      key,
      holder_id: holder,
      ttl: 60,
      reentrant: true,
    });
    expect(again.acquireCount).toBe(2);
    expect(again.fencingToken).toBe(info.fencingToken);

    const status = await client.lock.status(key);
    expect(status).not.toBeNull();
    expect(status!.holderId).toBe(holder);
    expect(status!.reentrant).toBe(true);
    expect(status!.leaseId).toBe(info.leaseId);

    const listed = await client.lock.list(key);
    const mine = listed.locks.find((l) => l.key === key);
    expect(mine).toBeDefined();
    expect(mine!.holderId).toBe(holder);
    expect(mine!.fencingToken).toBe(info.fencingToken);

    const extended = await client.lock.extend({
      key,
      fencing_token: info.fencingToken,
    });
    expect(extended.key).toBe(key);
    expect(extended.fencingToken).toBe(info.fencingToken);
    expect(extended.holderId).toBe(holder);

    // A reentrant lock acquired twice is released twice.
    await client.lock.release({ key, fencing_token: info.fencingToken });
    await client.lock.release({ key, fencing_token: info.fencingToken });
    expect(await client.lock.status(key)).toBeNull();
  });
});

test.describe("SDK wire round-trip: transactions (real daemon)", () => {
  test("execute applies successOps when every compare target holds", async ({ mvccDaemon }) => {
    const client = await connect(mvccDaemon.adminUrl);
    const key = unique("txn");
    const lease = await client.lease.grant(120);

    const seed = await client.transaction.execute({
      compares: [],
      successOps: [{ type: 0, key, value: "one", leaseId: lease.id }],
      failureOps: [],
    });
    expect(seed.succeeded).toBe(true);
    expect(seed.revision).toBeGreaterThan(0);

    const read = await client.batch.execute([{ type: 1, key }]);
    const kv = read.responses[0].kvs![0];

    const resp = await client.transaction.execute({
      compares: [
        { key, target: 3, result: 0, value: "one" },
        { key, target: 0, result: 0, version: kv.version },
        { key, target: 1, result: 0, createRevision: kv.createRevision },
        { key, target: 2, result: 0, modRevision: kv.modRevision },
      ],
      successOps: [
        { type: 0, key, value: "two", leaseId: lease.id },
        { type: 1, key },
        { type: 3, key, rangeEnd: `${key}\u0000` },
      ],
      failureOps: [{ type: 0, key, value: "failed" }],
    });
    expect(resp.succeeded).toBe(true);
    expect(resp.revision).toBeGreaterThan(seed.revision);
    expect(resp.responses).toHaveLength(3);
    expect(resp.responses[1].kvs![0].key).toBe(key);
    expect(resp.responses[1].kvs![0].value).toBe("two");
    expect(resp.responses[1].kvs![0].leaseId).toBe(lease.id);
    expect(resp.responses[2].count).toBe(1);

    const ttl = await client.lease.timeToLive(lease.id);
    expect(ttl.keys).toContain(key);
    await client.lease.revoke(lease.id);
  });

  test("execute applies failureOps when a compare fails", async ({ mvccDaemon }) => {
    const client = await connect(mvccDaemon.adminUrl);
    const key = unique("txn-fail");

    const resp = await client.transaction.execute({
      compares: [{ key, target: 3, result: 3, value: "" }],
      successOps: [{ type: 0, key, value: "success" }],
      failureOps: [
        { type: 0, key, value: "failure" },
        { type: 1, key },
      ],
    });
    expect(resp.succeeded).toBe(false);
    expect(resp.responses[1].kvs![0].value).toBe("failure");
  });
});

test.describe("SDK wire round-trip: batch execute (real daemon)", () => {
  test("execute runs put, get, range and delete with every Op field", async ({ mvccDaemon }) => {
    const client = await connect(mvccDaemon.adminUrl);
    const prefix = unique("batch");
    const lease = await client.lease.grant(120);

    const writes = await client.batch.execute([
      { type: 0, key: `${prefix}/a`, value: "A", leaseId: lease.id },
      { type: 0, key: `${prefix}/b`, value: "B" },
    ]);
    expect(writes.responses).toHaveLength(2);
    expect(typeof writes.revision).toBe("number");
    expect(writes.revision).toBeGreaterThan(0);
    for (const r of writes.responses) expect(r.succeeded).toBe(true);

    const resp = await client.batch.execute([
      { type: 1, key: `${prefix}/a` },
      { type: 3, key: `${prefix}/`, rangeEnd: `${prefix}0` },
      { type: 2, key: `${prefix}/b` },
    ]);
    expect(resp.responses).toHaveLength(3);
    for (const r of resp.responses) expect(r.succeeded).toBe(true);
    const got = resp.responses[0].kvs![0];
    expect(got.key).toBe(`${prefix}/a`);
    expect(got.value).toBe("A");
    expect(got.leaseId).toBe(lease.id);
    expect(got.version).toBeGreaterThan(0);
    expect(got.createRevision).toBeGreaterThan(0);
    expect(got.modRevision).toBeGreaterThan(0);
    expect(resp.responses[1].count).toBe(2);

    const after = await client.batch.execute([{ type: 1, key: `${prefix}/b` }]);
    expect(after.responses[0].kvs ?? []).toHaveLength(0);
    await client.lease.revoke(lease.id);
  });
});

test.describe("SDK wire round-trip: backups (real daemon)", () => {
  test("create, get, list, restore and delete carry every BackupInfo field", async ({ mvccDaemon }) => {
    // One daemon per worker: the bootstrap listener's port is fixed, so this
    // file uses the mvcc daemon throughout.
    const client = await connect(mvccDaemon.adminUrl);
    const peers = await client.admin.peers();
    const shardId = peers.shards[0].shardId;
    const destination = `/tmp/${unique("backup")}`;

    // keyProviderId/keyRef need a server key directory, which the test
    // daemon does not configure: this backup is explicitly plaintext.
    const created = await client.backups.create({
      destination,
      compression: "zstd",
      encryption: "none",
      metadata: { purpose: "wire" },
      shardIds: [shardId],
    });
    expect(created.id.length).toBeGreaterThan(0);
    expect(created.destination).toBe(destination);
    expect(created.compression).toBe("zstd");
    expect(created.encryption).toBe("none");
    expect(created.metadata).toEqual(expect.objectContaining({ purpose: "wire" }));
    expect(created.shardCount).toBe(1);
    expect(created.size).toBeGreaterThan(0);
    expect(created.checksum.length).toBeGreaterThan(0);
    expect(created.timestamp.length).toBeGreaterThan(0);
    expect(created.status.length).toBeGreaterThan(0);
    expect(typeof created.durationMs).toBe("number");
    expect(created.durationMs).toBeGreaterThanOrEqual(0);

    const got = await client.backups.get(created.id);
    expect(got.id).toBe(created.id);
    expect(got.destination).toBe(destination);
    expect(got.compression).toBe("zstd");
    expect(got.encryption).toBe("none");
    expect(got.metadata).toEqual(expect.objectContaining({ purpose: "wire" }));
    expect(got.shardCount).toBe(1);
    expect(got.checksum).toBe(created.checksum);
    expect(typeof got.durationMs).toBe("number");

    const listed = await client.backups.list();
    expect(listed.backups.map((b) => b.id)).toContain(created.id);

    const restored = await client.backups.restore({
      backupId: created.id,
      targetShards: [shardId],
      validateFirst: true,
      allowPlaintext: true,
    });
    expect(restored.backupId).toBe(created.id);
    expect(typeof restored.shardsRestored).toBe("number");
    expect(typeof restored.durationMs).toBe("number");
    expect(restored.durationMs).toBeGreaterThanOrEqual(0);

    await client.backups.delete(created.id);
    const gone = await client.backups.list();
    expect(gone.backups.map((b) => b.id)).not.toContain(created.id);
  });
});
