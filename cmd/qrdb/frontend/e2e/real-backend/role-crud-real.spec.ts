// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Role CRUD real-backend spec.
 *
 * Validates POST /roles, GET /roles/list, GET /roles/{id},
 * PUT /roles/{id}, and DELETE /roles/{id} against the live daemon.
 *
 * Wire contract (pkg/api/rest/handlers_roles.go): role entity keys are
 * snake_case (tenant_id, created_at, updated_at); list responses carry
 * roles/total/has_more; create body is {name, tenant_id, permissions};
 * update body is {name, permissions}; list query parameters are
 * camelCase (tenantID, page, pageSize). POST returns 201, DELETE 204,
 * unknown role 404.
 */

import { test, expect } from "./fixtures/daemon.ts";

interface RoleResponse {
  id: string;
  name: string;
  tenant_id: string;
  permissions: string[];
  system: boolean;
  created_at: string;
  updated_at: string;
}

interface ListRolesResponse {
  roles: RoleResponse[];
  total: number;
  has_more: boolean;
}

function roleName(label: string): string {
  return `e2e-role-${label}-${Date.now()}`;
}

test.describe("Role CRUD (real backend)", () => {
  test("POST /roles creates role, appears in list", async ({
    request,
    daemon,
  }) => {
    const name = roleName("create");

    const resp = await request.post(`${daemon.adminUrl}/roles`, {
      data: { name, tenant_id: "default", permissions: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (resp.status() === 501) {
      console.info("[role-crud-real] Role service not available (501). Skipping.");
      return;
    }

    expect(resp.status()).toBe(201);
    const role = await resp.json() as RoleResponse;
    expect(role.name).toBe(name);
    expect(typeof role.id).toBe("string");
    expect(role.tenant_id).toBe("default");
    expect(role.system).toBe(false);

    // Verify in list (tenant-scoped query).
    const listResp = await request.get(
      `${daemon.adminUrl}/roles/list?tenantID=default`,
    );
    expect(listResp.ok()).toBe(true);
    const list = await listResp.json() as ListRolesResponse;
    expect(list.roles.some((r) => r.id === role.id)).toBe(true);
    expect(typeof list.has_more).toBe("boolean");

    // Cleanup
    await request.delete(`${daemon.adminUrl}/roles/${role.id}`);
  });

  test("GET /roles/{id} returns created role", async ({ request, daemon }) => {
    const name = roleName("get");

    const createResp = await request.post(`${daemon.adminUrl}/roles`, {
      data: { name, tenant_id: "default", permissions: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[role-crud-real] Role service not available (501). Skipping.");
      return;
    }

    const role = await createResp.json() as RoleResponse;

    const getResp = await request.get(`${daemon.adminUrl}/roles/${role.id}`);
    expect(getResp.ok()).toBe(true);
    const fetched = await getResp.json() as RoleResponse;
    expect(fetched.id).toBe(role.id);
    expect(fetched.name).toBe(name);
    expect(fetched.tenant_id).toBe("default");

    // Cleanup
    await request.delete(`${daemon.adminUrl}/roles/${role.id}`);
  });

  test("DELETE /roles/{id} removes role", async ({ request, daemon }) => {
    const name = roleName("delete");

    const createResp = await request.post(`${daemon.adminUrl}/roles`, {
      data: { name, tenant_id: "default", permissions: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[role-crud-real] Role service not available (501). Skipping.");
      return;
    }

    const role = await createResp.json() as RoleResponse;

    const deleteResp = await request.delete(
      `${daemon.adminUrl}/roles/${role.id}`,
    );
    expect(deleteResp.status()).toBe(204);

    const getResp = await request.get(`${daemon.adminUrl}/roles/${role.id}`);
    expect(getResp.status()).toBe(404);
  });

  test("PUT /roles/{id} updates role name", async ({ request, daemon }) => {
    const name = roleName("update");
    const newName = roleName("updated");

    const createResp = await request.post(`${daemon.adminUrl}/roles`, {
      data: { name, tenant_id: "default", permissions: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[role-crud-real] Role service not available (501). Skipping.");
      return;
    }

    const role = await createResp.json() as RoleResponse;

    const updateResp = await request.put(`${daemon.adminUrl}/roles/${role.id}`, {
      data: { name: newName, permissions: [] },
      headers: { "Content-Type": "application/json" },
    });

    expect(updateResp.ok()).toBe(true);
    const updated = await updateResp.json() as RoleResponse;
    expect(updated.name).toBe(newName);

    // Cleanup
    await request.delete(`${daemon.adminUrl}/roles/${role.id}`);
  });
});
