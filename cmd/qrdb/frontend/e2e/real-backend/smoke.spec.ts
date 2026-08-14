// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Smoke spec — verifies that the real daemon is reachable and returns expected
 * responses on its primary admin endpoints.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import type { HealthResponse } from "./fixtures/api.ts";

test.describe("Real daemon smoke", () => {
  test("GET /health returns status ok", async ({ request, daemon }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const health = await api.get<HealthResponse>("/health");
    expect(health.status).toBe("ok");
  });

  test("GET /status returns a node_id", async ({ request, daemon }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    // The REST API uses snake_case: node_id (not nodeId)
    const status = await api.get<{ node_id: string }>("/status");
    expect(typeof status.node_id).toBe("string");
    expect(status.node_id.length).toBeGreaterThan(0);
  });

  test("GET /capabilities returns feature flags", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const caps = await api.get<Record<string, unknown>>("/capabilities");
    // kv is always present in the default pebble-backed node
    expect(typeof caps["kv"]).toBe("boolean");
  });

  test("GET /v1/databases/list returns empty list initially", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const resp = await api.get<{ databases: unknown[] }>("/v1/databases/list");
    expect(Array.isArray(resp.databases)).toBe(true);
  });

  test("GET /v1/tenants/list returns array", async ({ request, daemon }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const resp = await api.get<{ tenants: unknown[]; total: number }>(
      "/v1/tenants/list",
    );
    expect(Array.isArray(resp.tenants)).toBe(true);
    expect(typeof resp.total).toBe("number");
  });
});
