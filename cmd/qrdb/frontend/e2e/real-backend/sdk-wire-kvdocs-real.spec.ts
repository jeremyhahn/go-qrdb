// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SDK wire check (real backend): kv, documents, databases, index/query.
 *
 * Drives the TypeScript SDK's REST client against a live daemon. Each
 * method is called with every optional field set, and the result is read
 * back through a second route so a request or response field name that
 * does not match the handler's struct fails here instead of making a UI
 * action silently send or read nothing.
 */

import { createServer, type AddressInfo } from "node:net";
import { RestClient } from "@qrdb/sdk";
import { test, expect } from "./fixtures/daemon.ts";

// The daemon serves a self-signed certificate; the SDK uses Node's fetch,
// which (unlike the fixture's request context) verifies it.
process.env.NODE_TLS_REJECT_UNAUTHORIZED = "0";

function uniq(label: string): string {
  return `wire-${label}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const port = (srv.address() as AddressInfo).port;
      srv.close(() => resolve(port));
    });
  });
}

async function sdk(adminUrl: string): Promise<RestClient> {
  const client = new RestClient(adminUrl);
  await client.connection.connect(adminUrl);
  return client;
}

test.describe("SDK wire: kv, documents, databases, index/query (real backend)", () => {
  test("kv: put/get/exists/list/scan/delete round-trip through the SDK", async ({
    request,
    daemon,
  }) => {
    const c = await sdk(daemon.adminUrl);
    const prefix = `${uniq("kv")}/`;
    const a = `${prefix}a`;
    const b = `${prefix}b`;

    await c.kv.put(a, "alpha");
    await c.kv.put(b, "béta");

    // Second route: the raw GET the handler serves.
    const raw = await request.get(`${daemon.adminUrl}/kv/${encodeURIComponent(a)}`);
    expect(raw.ok()).toBe(true);
    expect(await raw.text()).toBe("alpha");

    expect(await c.kv.get(b)).toBe("béta");
    expect(await c.kv.exists(a)).toBe(true);
    expect([...(await c.kv.list(prefix))].sort()).toEqual([a, b]);
    expect(await c.kv.scan(prefix)).toEqual({ [a]: "alpha", [b]: "béta" });

    await c.kv.delete(a);
    expect(await c.kv.exists(a)).toBe(false);
    expect((await request.get(`${daemon.adminUrl}/kv/${encodeURIComponent(a)}`)).status()).toBe(404);
    await c.kv.delete(b);
  });

  test("databases: create/get/list/updateSQL/drop round-trip", async ({
    daemon,
  }) => {
    const c = await sdk(daemon.adminUrl);
    const name = uniq("db").replace(/-/g, "_");
    const etcdAddr = `127.0.0.1:${await freePort()}`;

    const created = await c.database.create({
      name,
      backend: "pebble",
      protocol: {
        mysql: false,
        postgresql: false,
        etcd: { enabled: true, listenAddr: etcdAddr, advertiseClientUrls: [`http://${etcdAddr}`] },
      },
      sql: { concurrency: "pessimistic" },
    });
    expect(created.name).toBe(name);
    expect(created.backend).toBe("pebble");

    const got = await c.database.get(name);
    expect(got.name).toBe(name);
    expect(got.backend).toBe("pebble");
    expect(got.sql?.concurrency).toBe("pessimistic");
    expect(got.protocol?.mysql ?? false).toBe(false);
    expect(got.protocol?.postgresql ?? false).toBe(false);
    expect(got.protocol?.etcd?.enabled).toBe(true);
    expect(got.protocol?.etcd?.listenAddr).toBe(etcdAddr);
    expect(got.protocol?.etcd?.advertiseClientUrls).toEqual([`http://${etcdAddr}`]);

    const listed = await c.database.list();
    expect(listed.map((d) => d.name)).toContain(name);

    const sqlUpd = await c.database.updateSQL(name, { concurrency: "optimistic" });
    expect(sqlUpd.sql?.concurrency ?? "optimistic").toBe("optimistic");
    expect((await c.database.get(name)).sql?.concurrency ?? "optimistic").toBe("optimistic");

    // updateCompaction is not driven here: the fixture's daemon runs without
    // an MVCC state machine, and the server refuses a compaction policy on a
    // store that keeps no history (422).

    await c.database.drop(name);
    expect((await c.database.list()).map((d) => d.name)).not.toContain(name);
  });

  test("documents: collection create/get/list and document CRUD round-trip", async ({
    daemon,
  }) => {
    const c = await sdk(daemon.adminUrl);
    const db = uniq("docdb").replace(/-/g, "_");
    await c.database.create({ name: db, backend: "memory" });
    const coll = uniq("coll").replace(/-/g, "_");

    const created = await c.collection.create({
      database: db,
      name: coll,
      id_strategy: "field_hash",
      field_hash: "email",
      indexes: [{ field_name: "n", unique: false, sparse: true }],
    });
    expect(created.name).toBe(coll);
    expect(created.database).toBe(db);
    expect(created.id_strategy).toBe("field_hash");
    expect(created.field_hash).toBe("email");

    const got = await c.collection.get(db, coll);
    expect(got.id_strategy).toBe("field_hash");
    expect(got.field_hash).toBe("email");
    expect(typeof got.created_at).toBe("string");
    expect(got.indexes).toEqual([{ field_name: "n", sparse: true }]);

    const listed = await c.collection.list(db);
    expect(listed.map((x) => x.name)).toContain(coll);

    const doc = { email: "a@example.com", first_name: "Ada", n: 3, tags: ["x", "y"], nested: { ok: true } };
    const ins = await c.collection.insertDocument({ database: db, collection: coll, document: doc });
    expect(typeof ins.id).toBe("string");
    expect(ins.id).not.toBe("");

    const read = await c.collection.getDocument(db, coll, ins.id);
    expect(read.id).toBe(ins.id);
    expect(read.document).toMatchObject(doc);

    const upd = { ...doc, n: 4 };
    await c.collection.updateDocument({ database: db, collection: coll, id: ins.id, document: upd });
    expect((await c.collection.getDocument(db, coll, ins.id)).document).toMatchObject({ n: 4 });

    await c.collection.insertDocument({ database: db, collection: coll, document: { email: "b@example.com" } });
    const page1 = await c.collection.listDocuments({ database: db, collection: coll, page_size: 1 });
    expect(page1.documents.length).toBe(1);
    expect(page1.total_count).toBe(2);
    expect(page1.next_page_token).toBeTruthy();
    const page2 = await c.collection.listDocuments({
      database: db,
      collection: coll,
      page_size: 1,
      page_token: page1.next_page_token,
    });
    expect(page2.documents.length).toBe(1);
    expect(page2.documents[0].id).not.toBe(page1.documents[0].id);
    expect((await c.collection.get(db, coll)).document_count).toBe(2);

    await c.collection.deleteDocument(db, coll, ins.id);
    await expect(c.collection.getDocument(db, coll, ins.id)).rejects.toThrow();

    // collection.drop is not driven here: the server refuses it (501) on a
    // state machine that keeps no MVCC history, which this fixture runs.
  });

  test("index: create/list/query/scan/drop round-trip with every flag", async ({
    daemon,
  }) => {
    const c = await sdk(daemon.adminUrl);
    const idx = uniq("idx").replace(/-/g, "_");
    const field = uniq("f").replace(/-/g, "_");

    await c.index.create({
      name: idx,
      fieldName: field,
      unique: false,
      sparse: true,
      numeric: false,
      caseInsensitive: true,
    });
    expect((await c.index.list()).names).toContain(idx);

    const k1 = uniq("ik1");
    const k2 = uniq("ik2");
    await c.kv.put(k1, JSON.stringify({ [field]: "apple" }));
    await c.kv.put(k2, JSON.stringify({ [field]: "banana" }));

    const q = await c.index.query(idx, "apple");
    expect(q.keys).toContain(k1);
    expect(q.keys).not.toContain(k2);
    // caseInsensitive reached the handler: a differently-cased lookup matches.
    expect((await c.index.query(idx, "APPLE")).keys).toContain(k1);

    const s = await c.index.scan(idx, "a", "c");
    const byValue = Object.fromEntries(s.entries.map((e) => [e.value, e.keys]));
    expect(byValue["apple"]).toContain(k1);
    expect(byValue["banana"]).toContain(k2);

    await c.index.drop(idx);
    expect((await c.index.list()).names).not.toContain(idx);
    await c.kv.delete(k1);
    await c.kv.delete(k2);
  });
});
