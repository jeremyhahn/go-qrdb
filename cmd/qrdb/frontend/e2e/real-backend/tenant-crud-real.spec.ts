// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Tenant CRUD real-backend spec — regression coverage for bug #7.
 *
 * Validates that POST /v1/tenants, GET /v1/tenants/list,
 * GET /v1/tenants/{id}, PUT /v1/tenants/{id}, and
 * DELETE /v1/tenants/{id} all behave according to the server contract.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import type { TenantInfo, ListTenantsResponse } from "./fixtures/api.ts";

/** Returns a test-scoped unique tenant ID to avoid cross-test pollution. */
function tenantId(label: string): string {
  return `e2e-${label}-${Date.now()}`;
}

test.describe("Tenant CRUD (real backend)", () => {
  test("POST /v1/tenants creates a tenant with prefix isolation", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("create-prefix");

    const tenant = await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "Create Prefix Test",
      isolation_type: "prefix",
    });

    expect(tenant.id).toBe(id);
    expect(tenant.name).toBe("Create Prefix Test");
    expect(tenant.isolation_type).toBe("prefix");

    // Verify it appears in the list
    const list = await api.get<ListTenantsResponse>("/v1/tenants/list");
    const found = list.tenants.find((t) => t.id === id);
    expect(found).toBeDefined();
    expect(found!.isolation_type).toBe("prefix");

    // Cleanup
    await api.delete(`/v1/tenants/${id}`);
  });

  test("POST /v1/tenants with barrier isolation fails closed (422) without seal", async ({
    request,
    daemon,
  }) => {
    // The shared real-backend daemon runs WITHOUT the seal subsystem.
    // Barrier isolation is strictly opt-in and fail-closed: the server
    // rejects creation with 422 (ErrBarrierIsolationUnavailable) rather
    // than silently provisioning an unencrypted "barrier" tenant.
    const id = tenantId("create-barrier");

    const resp = await request.post(`${daemon.adminUrl}/v1/tenants`, {
      data: { id, name: "Create Barrier Test", isolation_type: "barrier" },
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.status()).toBe(422);

    // The tenant must NOT have been created.
    const api = new ApiClient(request, daemon.adminUrl);
    const status = await api.getStatus(`/v1/tenants/${id}`);
    expect(status).toBe(404);
  });

  test("GET /v1/tenants/list includes newly created tenant", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("list-check");

    await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "List Check",
      isolation_type: "prefix",
    });

    const list = await api.get<ListTenantsResponse>("/v1/tenants/list");
    expect(list.tenants.some((t) => t.id === id)).toBe(true);
    expect(list.total).toBeGreaterThanOrEqual(1);

    // Cleanup
    await api.delete(`/v1/tenants/${id}`);
  });

  test("PUT /v1/tenants/{id} updates tenant name", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("update-name");

    await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "Original Name",
      isolation_type: "prefix",
    });

    const updated = await api.put<TenantInfo>(`/v1/tenants/${id}`, {
      name: "Updated Name",
    });

    expect(updated.id).toBe(id);
    expect(updated.name).toBe("Updated Name");

    // Confirm via GET
    const fetched = await api.get<TenantInfo>(`/v1/tenants/${id}`);
    expect(fetched.name).toBe("Updated Name");

    // Cleanup
    await api.delete(`/v1/tenants/${id}`);
  });

  test("PUT /v1/tenants/{id} isolation change to barrier fails closed (422) without seal", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("change-isolation");

    await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "Isolation Change",
      isolation_type: "prefix",
    });

    // Barrier isolation requires the seal subsystem; the no-seal daemon
    // rejects the isolation change with 422 (ErrBarrierIsolationUnavailable).
    const resp = await request.put(`${daemon.adminUrl}/v1/tenants/${id}`, {
      data: { isolation_type: "barrier" },
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.status()).toBe(422);

    // The tenant must be unchanged.
    const fetched = await api.get<TenantInfo>(`/v1/tenants/${id}`);
    expect(fetched.isolation_type).toBe("prefix");

    // Cleanup
    await api.delete(`/v1/tenants/${id}`);
  });

  test("DELETE /v1/tenants/{id} removes the tenant", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("delete-check");

    await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "To Delete",
      isolation_type: "prefix",
    });

    await api.delete(`/v1/tenants/${id}`);

    // Should now 404
    const status = await api.getStatus(`/v1/tenants/${id}`);
    expect(status).toBe(404);
  });

  test("GET /v1/tenants/{id} returns 404 for unknown tenant", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const status = await api.getStatus("/v1/tenants/no-such-tenant-xyz");
    expect(status).toBe(404);
  });

  test("POST /v1/tenants returns 409 on duplicate id", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const id = tenantId("dup-check");

    await api.post<TenantInfo>("/v1/tenants", {
      id,
      name: "First",
      isolation_type: "prefix",
    });

    const resp = await request.post(`${daemon.adminUrl}/v1/tenants`, {
      data: { id, name: "Second", isolation_type: "prefix" },
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.status()).toBe(409);

    // Cleanup
    await api.delete(`/v1/tenants/${id}`);
  });
});
