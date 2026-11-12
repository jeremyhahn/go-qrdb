// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Index query real-backend spec — regression coverage for bug #10.
 *
 * Validates that:
 *   1. POST /index/create creates a secondary index (returns 200 with status:ok).
 *   2. GET /index/list returns the created index name.
 *   3. GET /index/query with a known index returns empty keys (plain KV writes
 *      do not populate indexes — this spec documents that boundary and asserts
 *      the endpoint returns 200, not a server error).
 *   4. GET /index/query on a missing index returns 500 (the server reports the
 *      missing-index error as an internal error rather than 404).
 *
 * The MVCC state machine is required for index support. The default pebble node
 * exposes the index endpoint but queries against the MVCC index store. Indexes
 * are created and the endpoint contract is validated without requiring a full
 * MVCC SM.
 */

import { test, expect } from "./fixtures/daemon.ts";

function name(label: string): string {
  return `e2e-idx-${label}-${Date.now()}`;
}

test.describe("Index query (real backend — bug #10)", () => {
  test("POST /index/create returns 200 with status:ok", async ({
    request,
    daemon,
  }) => {
    const indexName = name("create-ok");

    const resp = await request.post(`${daemon.adminUrl}/index/create`, {
      data: { name: indexName, field_name: "category" },
      headers: { "Content-Type": "application/json" },
    });

    expect(resp.ok()).toBe(true);
    const body = await resp.json() as { status: string };
    expect(body.status).toBe("ok");

    // Cleanup
    await request.post(`${daemon.adminUrl}/index/drop`, {
      data: { name: indexName },
      headers: { "Content-Type": "application/json" },
    });
  });

  test("GET /index/list returns created index", async ({ request, daemon }) => {
    const indexName = name("list-check");

    const probe = await request.post(`${daemon.adminUrl}/index/create`, {
      data: { name: indexName, field_name: "status" },
      headers: { "Content-Type": "application/json" },
    });

    expect(probe.ok()).toBe(true);

    const listResp = await request.get(`${daemon.adminUrl}/index/list`);
    expect(listResp.ok()).toBe(true);
    const list = await listResp.json() as { names: string[] };
    expect(Array.isArray(list.names)).toBe(true);
    expect(list.names).toContain(indexName);

    // Cleanup
    await request.post(`${daemon.adminUrl}/index/drop`, {
      data: { name: indexName },
      headers: { "Content-Type": "application/json" },
    });
  });

  test(
    "GET /index/query on existing index returns 200 with keys array",
    async ({ request, daemon }) => {
      const indexName = name("query-empty");

      await request.post(`${daemon.adminUrl}/index/create`, {
        data: { name: indexName, field_name: "color" },
        headers: { "Content-Type": "application/json" },
      });

      // Plain KV writes do not populate indexes — query returns empty keys array
      const resp = await request.get(
        `${daemon.adminUrl}/index/query?index_name=${encodeURIComponent(indexName)}&value=blue`,
      );
      expect(resp.ok()).toBe(true);
      const result = await resp.json() as { keys: string[] };
      expect(Array.isArray(result.keys)).toBe(true);

      // Cleanup
      await request.post(`${daemon.adminUrl}/index/drop`, {
        data: { name: indexName },
        headers: { "Content-Type": "application/json" },
      });
    },
  );

  test(
    "GET /index/query on missing index returns 500 (server documents non-existence as error)",
    async ({ request, daemon }) => {
      // The server returns HTTP 500 for a missing index query, not 404.
      // This spec documents and asserts that contract so a future change to
      // return 404 instead will be caught by this test.
      const resp = await request.get(
        `${daemon.adminUrl}/index/query?index_name=no-such-index-${Date.now()}&value=x`,
      );
      expect(resp.status()).toBe(500);
    },
  );

  test("POST /index/drop on existing index succeeds", async ({
    request,
    daemon,
  }) => {
    const indexName = name("drop");

    await request.post(`${daemon.adminUrl}/index/create`, {
      data: { name: indexName, field_name: "tag" },
      headers: { "Content-Type": "application/json" },
    });

    const dropResp = await request.post(`${daemon.adminUrl}/index/drop`, {
      data: { name: indexName },
      headers: { "Content-Type": "application/json" },
    });
    expect(dropResp.ok()).toBe(true);

    // The index should no longer appear in the list
    const listResp = await request.get(`${daemon.adminUrl}/index/list`);
    const list = await listResp.json() as { names: string[] };
    expect(list.names).not.toContain(indexName);
  });
});
