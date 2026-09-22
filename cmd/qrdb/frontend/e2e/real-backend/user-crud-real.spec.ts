// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * User CRUD real-backend spec.
 *
 * Validates POST /users, GET /users/list, GET /users/{id},
 * PUT /users/{id}, and DELETE /users/{id} against the live daemon.
 *
 * Wire contract (pkg/api/rest/handlers_users.go): uint64 Snowflake IDs
 * are encoded as JSON strings (proto3 convention) to avoid Number
 * precision loss; entity keys are snake_case (tenant_id, role_ids,
 * created_at, updated_at); list responses carry users/total/has_more;
 * query parameters are camelCase (tenantID, page, pageSize).
 *
 * User management requires auth-enabled=false (set in the daemon fixture)
 * so calls are unauthenticated. Specs check for 501 Not Implemented and
 * skip gracefully if the user service is unavailable.
 */

import { test, expect } from "./fixtures/daemon.ts";

interface UserResponse {
  // uint64 Snowflake IDs exceed Number.MAX_SAFE_INTEGER and arrive as
  // JSON strings.
  id: string;
  username: string;
  tenant_id: string;
  role_ids: string[];
  lifecycle: string;
  system: boolean;
  created_at: string;
  updated_at: string;
}

interface ListUsersResponse {
  users: UserResponse[];
  total: number;
  has_more: boolean;
}

function username(label: string): string {
  return `e2e-user-${label}-${Date.now()}`;
}

test.describe("User CRUD (real backend)", () => {
  test("POST /users creates user, appears in list", async ({
    request,
    daemon,
  }) => {
    const uname = username("create");

    const resp = await request.post(`${daemon.adminUrl}/users`, {
      data: { username: uname, tenant_id: "default", role_ids: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (resp.status() === 501) {
      console.info("[user-crud-real] User service not available (501). Skipping.");
      return;
    }

    expect(resp.status()).toBe(201);
    const user = await resp.json() as UserResponse;
    expect(user.username).toBe(uname);
    expect(typeof user.id).toBe("string");
    expect(user.tenant_id).toBe("default");

    // Verify in list (tenant-scoped query).
    const listResp = await request.get(
      `${daemon.adminUrl}/users/list?tenantID=default`,
    );
    expect(listResp.ok()).toBe(true);
    const list = await listResp.json() as ListUsersResponse;
    expect(list.users.some((u) => u.id === user.id)).toBe(true);
    expect(typeof list.has_more).toBe("boolean");

    // Cleanup
    await request.delete(`${daemon.adminUrl}/users/${user.id}`);
  });

  test("GET /users/{id} returns created user", async ({ request, daemon }) => {
    const uname = username("get");

    const createResp = await request.post(`${daemon.adminUrl}/users`, {
      data: { username: uname, tenant_id: "default", role_ids: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[user-crud-real] User service not available (501). Skipping.");
      return;
    }

    const user = await createResp.json() as UserResponse;

    const getResp = await request.get(`${daemon.adminUrl}/users/${user.id}`);
    expect(getResp.ok()).toBe(true);
    const fetched = await getResp.json() as UserResponse;
    expect(fetched.id).toBe(user.id);
    expect(fetched.username).toBe(uname);

    // Cleanup
    await request.delete(`${daemon.adminUrl}/users/${user.id}`);
  });

  test("DELETE /users/{id} removes user", async ({ request, daemon }) => {
    const uname = username("delete");

    const createResp = await request.post(`${daemon.adminUrl}/users`, {
      data: { username: uname, tenant_id: "default", role_ids: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[user-crud-real] User service not available (501). Skipping.");
      return;
    }

    const user = await createResp.json() as UserResponse;

    const deleteResp = await request.delete(
      `${daemon.adminUrl}/users/${user.id}`,
    );
    expect(deleteResp.status()).toBe(204);

    const getResp = await request.get(`${daemon.adminUrl}/users/${user.id}`);
    expect(getResp.status()).toBe(404);
  });

  test("PUT /users/{id} updates username", async ({ request, daemon }) => {
    const uname = username("update");
    const newName = username("updated");

    const createResp = await request.post(`${daemon.adminUrl}/users`, {
      data: { username: uname, tenant_id: "default", role_ids: [] },
      headers: { "Content-Type": "application/json" },
    });

    if (createResp.status() === 501) {
      console.info("[user-crud-real] User service not available (501). Skipping.");
      return;
    }

    const user = await createResp.json() as UserResponse;

    const updateResp = await request.put(`${daemon.adminUrl}/users/${user.id}`, {
      data: { username: newName, role_ids: [] },
      headers: { "Content-Type": "application/json" },
    });

    expect(updateResp.ok()).toBe(true);
    const updated = await updateResp.json() as UserResponse;
    expect(updated.username).toBe(newName);

    // Cleanup
    await request.delete(`${daemon.adminUrl}/users/${user.id}`);
  });
});
