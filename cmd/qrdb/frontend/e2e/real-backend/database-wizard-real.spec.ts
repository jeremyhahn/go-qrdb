// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Database wizard real-backend spec.
 *
 * Validates that POST /v1/databases creates a database, it appears in
 * GET /v1/databases/list, and per-database isolation holds (separate
 * KV namespaces between two databases).
 */

import { test, expect } from "./fixtures/daemon.ts";

interface DatabaseInfoResponse {
  name: string;
  engine: string;
  backend: string;
  status: string;
  protocol?: { mysql: boolean; postgresql: boolean };
}

interface ListDatabasesResponse {
  databases: DatabaseInfoResponse[];
}

function dbName(label: string): string {
  return `e2e-db-${label}-${Date.now()}`;
}

test.describe("Database management (real backend)", () => {
  test(
    "POST /v1/databases creates database, appears in list",
    async ({ request, daemon }) => {
      const name = dbName("create");

      const resp = await request.post(`${daemon.adminUrl}/v1/databases`, {
        data: {
          name,
          engine: "kv",
          backend: "pebble",
          protocol: { mysql: false, postgresql: false },
        },
        headers: { "Content-Type": "application/json" },
      });

      // The full daemon wires the database service; creation must succeed.
      // (No skip guards — the project forbids them.)
      expect(resp.status()).toBe(201);
      const db = await resp.json() as DatabaseInfoResponse;
      expect(db.name).toBe(name);
      expect(db.engine).toBe("kv");

      // Verify in list
      const listResp = await request.get(
        `${daemon.adminUrl}/v1/databases/list`,
      );
      expect(listResp.ok()).toBe(true);
      const list = await listResp.json() as ListDatabasesResponse;
      expect(list.databases.some((d) => d.name === name)).toBe(true);

      // Cleanup
      await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);
    },
  );

  test("GET /v1/databases/{name} returns created database", async ({
    request,
    daemon,
  }) => {
    const name = dbName("get");

    const createResp = await request.post(`${daemon.adminUrl}/v1/databases`, {
      data: {
        name,
        engine: "kv",
        backend: "pebble",
        protocol: { mysql: false, postgresql: false },
      },
      headers: { "Content-Type": "application/json" },
    });

    expect(createResp.status()).toBe(201);

    const getResp = await request.get(
      `${daemon.adminUrl}/v1/databases/${name}`,
    );
    expect(getResp.ok()).toBe(true);
    const db = await getResp.json() as DatabaseInfoResponse;
    expect(db.name).toBe(name);

    // Cleanup
    await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);
  });

  test("DELETE /v1/databases/{name} removes database from list", async ({
    request,
    daemon,
  }) => {
    const name = dbName("drop");

    const createResp = await request.post(`${daemon.adminUrl}/v1/databases`, {
      data: {
        name,
        engine: "kv",
        backend: "pebble",
        protocol: { mysql: false, postgresql: false },
      },
      headers: { "Content-Type": "application/json" },
    });

    expect(createResp.status()).toBe(201);

    await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);

    const getResp = await request.get(
      `${daemon.adminUrl}/v1/databases/${name}`,
    );
    expect(getResp.status()).toBe(404);
  });

  test("two databases maintain separate KV namespaces (real isolation, both directions)", async ({
    request,
    daemon,
  }) => {
    const dbA = dbName("iso-a");
    const dbB = dbName("iso-b");
    const key = "shared-key";

    const createA = await request.post(`${daemon.adminUrl}/v1/databases`, {
      data: { name: dbA, engine: "kv", backend: "pebble", protocol: { mysql: false, postgresql: false } },
      headers: { "Content-Type": "application/json" },
    });
    // No skip guard: the full daemon must support per-database routing.
    expect(createA.status()).toBe(201);

    const createB = await request.post(`${daemon.adminUrl}/v1/databases`, {
      data: { name: dbB, engine: "kv", backend: "pebble", protocol: { mysql: false, postgresql: false } },
      headers: { "Content-Type": "application/json" },
    });
    expect(createB.status()).toBe(201);

    // Write the SAME logical key in three scopes: dbA, dbB, and the system
    // database (no header → system). Each must be isolated.
    const putA = await request.put(`${daemon.adminUrl}/kv/${key}`, {
      data: "db-a-value",
      headers: { "Content-Type": "text/plain", "X-QRDB-Database": dbA },
    });
    expect(putA.ok()).toBe(true);
    const putB = await request.put(`${daemon.adminUrl}/kv/${key}`, {
      data: "db-b-value",
      headers: { "Content-Type": "text/plain", "X-QRDB-Database": dbB },
    });
    expect(putB.ok()).toBe(true);
    const putSys = await request.put(`${daemon.adminUrl}/kv/${key}`, {
      data: "system-value",
      headers: { "Content-Type": "text/plain" }, // no header → system database
    });
    expect(putSys.ok()).toBe(true);

    // Each database sees ONLY its own value for the shared key.
    const readA = await request.get(`${daemon.adminUrl}/kv/${key}`, {
      headers: { "X-QRDB-Database": dbA },
    });
    const readB = await request.get(`${daemon.adminUrl}/kv/${key}`, {
      headers: { "X-QRDB-Database": dbB },
    });
    const readSys = await request.get(`${daemon.adminUrl}/kv/${key}`);
    expect(readA.ok()).toBe(true);
    expect(readB.ok()).toBe(true);
    expect(readSys.ok()).toBe(true);
    expect(await readA.text()).toBe("db-a-value");
    expect(await readB.text()).toBe("db-b-value");
    expect(await readSys.text()).toBe("system-value");

    // Leak guard (regression for the unprefixed-system bug): a scan of the
    // system database must NOT surface dbA's or dbB's keys. Each scope's scan
    // returns exactly one entry — its own — keyed by the bare (stripped) key.
    const scanSys = await request.get(`${daemon.adminUrl}/kv/scan?prefix=`);
    expect(scanSys.ok()).toBe(true);
    const sysPairs = ((await scanSys.json()) as { pairs: Record<string, string> }).pairs;
    // The system scan must contain its own key and no namespaced sibling keys.
    expect(Object.keys(sysPairs)).toContain(key);
    expect(Object.keys(sysPairs).some((k) => k.includes(dbA) || k.includes(dbB))).toBe(false);

    const scanA = await request.get(`${daemon.adminUrl}/kv/scan?prefix=`, {
      headers: { "X-QRDB-Database": dbA },
    });
    const aPairs = ((await scanA.json()) as { pairs: Record<string, string> }).pairs;
    expect(Object.keys(aPairs)).toEqual([key]); // exactly its own key, stripped

    // Cleanup
    await request.delete(`${daemon.adminUrl}/v1/databases/${dbA}`);
    await request.delete(`${daemon.adminUrl}/v1/databases/${dbB}`);
  });
});
