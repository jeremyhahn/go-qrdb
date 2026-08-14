// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * KV CRUD real-backend spec.
 *
 * Exercises PUT /kv/{key}, GET /kv/{key}, DELETE /kv/{key}, and
 * GET /kv (list) against a live daemon. Every action is verified by
 * an independent API read so test pass/fail reflects real state.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import type { KVListResponse } from "./fixtures/api.ts";

function key(label: string): string {
  return `e2e-kv-${label}-${Date.now()}`;
}

test.describe("KV CRUD (real backend)", () => {
  test("PUT then GET round-trips value correctly", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("rountrip");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "hello-world");

    const fetched = await request.get(`${daemon.adminUrl}/kv/${encodeURIComponent(k)}`);
    expect(fetched.ok()).toBe(true);
    expect(await fetched.text()).toBe("hello-world");

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
  });

  test("GET on missing key returns 404", async ({ request, daemon }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const status = await api.getStatus(`/kv/${encodeURIComponent(key("missing"))}`);
    expect(status).toBe(404);
  });

  test("PUT updates value for existing key", async ({ request, daemon }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("update");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "v1");
    await api.putText(`/kv/${encodeURIComponent(k)}`, "v2");

    const resp = await request.get(`${daemon.adminUrl}/kv/${encodeURIComponent(k)}`);
    expect(await resp.text()).toBe("v2");

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
  });

  test("DELETE removes key — subsequent GET returns 404", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("delete");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "to-be-deleted");
    await api.delete(`/kv/${encodeURIComponent(k)}`);

    const status = await api.getStatus(`/kv/${encodeURIComponent(k)}`);
    expect(status).toBe(404);
  });

  test("GET /kv list returns keys matching prefix", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const prefix = `e2e-list-${Date.now()}/`;
    const keys = [`${prefix}a`, `${prefix}b`, `${prefix}c`];

    for (const k of keys) {
      await api.putText(`/kv/${encodeURIComponent(k)}`, "val");
    }

    const resp = await api.get<KVListResponse>("/kv", { prefix });
    for (const k of keys) {
      expect(resp.keys).toContain(k);
    }

    // Cleanup
    for (const k of keys) {
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    }
  });

  test("GET /kv list without prefix returns all keys", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("all-list");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "present");

    const resp = await api.get<KVListResponse>("/kv");
    expect(resp.keys).toContain(k);

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
  });
});
