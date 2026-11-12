// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SDK wire check for the users, roles, permissions and tenants groups.
 *
 * Drives the TypeScript SDK's RestClient against a live daemon. Every
 * method is called with each optional field set and the result is read
 * back through a second route, so a request or response field name that
 * does not match the handler's json tags fails here instead of silently
 * sending or reading nothing in the UI.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { RestClient } from "../../../../../sdk/typescript/src/rest.js";

// The SDK's RestClient uses Node's fetch, which rejects the daemon's
// self-signed TLS certificate (Playwright's ignoreHTTPSErrors only applies to
// its own request context), so turn verification off for this worker.
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = "0";

function uniq(label: string): string {
  return `e2e-wire-${label}-${Date.now()}`;
}

async function sdk(adminUrl: string): Promise<RestClient> {
  const client = new RestClient(adminUrl);
  await client.connection.connect(adminUrl);
  return client;
}

/** Two permission strings the server itself advertises. */
async function somePermissions(client: RestClient): Promise<string[]> {
  const resp = await client.roles.listPermissions();
  const all = resp.categories.flatMap((c) => [...c.permissions]);
  expect(all.length).toBeGreaterThan(1);
  return [all[0], all[all.length - 1]];
}

test.describe("SDK IAM wire (real daemon)", () => {
  test("permissions.list reads categories with name, group and permissions", async ({
    daemon,
  }) => {
    const client = await sdk(daemon.adminUrl);
    const resp = await client.roles.listPermissions();
    expect(resp.categories.length).toBeGreaterThan(0);
    for (const c of resp.categories) {
      expect(typeof c.name).toBe("string");
      expect(c.name).not.toBe("");
      expect(typeof c.group).toBe("string");
      expect(c.group).not.toBe("");
      expect(c.permissions.length).toBeGreaterThan(0);
    }
  });

  test("roles create/get/update/list round-trip every field", async ({ daemon }) => {
    const client = await sdk(daemon.adminUrl);
    const [p1, p2] = await somePermissions(client);
    const name = uniq("role");

    const created = await client.roles.create({
      name,
      tenantId: "system",
      permissions: [p1],
    });
    expect(created.id).not.toBe("");
    expect(created.name).toBe(name);
    expect(created.tenantId).toBe("system");
    expect([...created.permissions]).toEqual([p1]);
    expect(created.system).toBe(false);
    expect(created.createdAt).not.toBe("");
    expect(created.updatedAt).not.toBe("");

    const got = await client.roles.get(created.id);
    expect(got.name).toBe(name);
    expect(got.tenantId).toBe("system");
    expect([...got.permissions]).toEqual([p1]);

    const renamed = `${name}-v2`;
    const updated = await client.roles.update({
      id: created.id,
      name: renamed,
      permissions: [p1, p2],
    });
    expect(updated.name).toBe(renamed);

    const reread = await client.roles.get(created.id);
    expect(reread.name).toBe(renamed);
    expect([...reread.permissions].sort()).toEqual([p1, p2].sort());

    const listed = await client.roles.list({ tenantId: "system", page: 1, pageSize: 500 });
    expect(listed.total).toBeGreaterThan(0);
    expect(typeof listed.hasMore).toBe("boolean");
    const row = listed.roles.find((r) => r.id === created.id);
    expect(row?.name).toBe(renamed);
    expect(row?.tenantId).toBe("system");

    // pageSize must reach the server: a page of one holds one role.
    const one = await client.roles.list({ tenantId: "system", page: 1, pageSize: 1 });
    expect(one.roles.length).toBe(1);

    await client.roles.delete(created.id);
    const after = await client.roles.list({ tenantId: "system", page: 1, pageSize: 500 });
    expect(after.roles.find((r) => r.id === created.id)).toBeUndefined();
  });

  test("users create/get/update/list/enable/disable and role assign round-trip", async ({
    daemon,
  }) => {
    const client = await sdk(daemon.adminUrl);
    const [p1] = await somePermissions(client);
    const roleA = await client.roles.create({ name: uniq("ra"), tenantId: "system", permissions: [p1] });
    const roleB = await client.roles.create({ name: uniq("rb"), tenantId: "system", permissions: [p1] });
    const username = uniq("user");

    const created = await client.users.create({
      username,
      email: `${username}@example.test`,
      tenantId: "system",
      roleIds: [roleA.id],
    });
    expect(created.id).not.toBe("");
    expect(created.username).toBe(username);
    expect(created.tenantId).toBe("system");
    expect([...created.roleIds]).toEqual([roleA.id]);
    expect(created.lifecycle).toBe("active");
    expect(created.system).toBe(false);
    expect(created.createdAt).not.toBe("");
    expect(created.updatedAt).not.toBe("");

    const got = await client.users.get(created.id);
    expect(got.username).toBe(username);
    expect(got.tenantId).toBe("system");
    expect([...got.roleIds]).toEqual([roleA.id]);

    const renamed = `${username}-v2`;
    await client.users.update({ id: created.id, username: renamed, roleIds: [roleA.id, roleB.id] });
    const reread = await client.users.get(created.id);
    expect(reread.username).toBe(renamed);
    expect([...reread.roleIds].sort()).toEqual([roleA.id, roleB.id].sort());

    const listed = await client.users.list({ tenantId: "system", page: 1, pageSize: 500 });
    expect(listed.total).toBeGreaterThan(0);
    expect(typeof listed.hasMore).toBe("boolean");
    expect(listed.users.find((u) => u.id === created.id)?.username).toBe(renamed);
    const one = await client.users.list({ tenantId: "system", page: 1, pageSize: 1 });
    expect(one.users.length).toBe(1);

    await client.users.disable(created.id);
    expect((await client.users.get(created.id)).lifecycle).toBe("disabled");
    await client.users.enable(created.id);
    expect((await client.users.get(created.id)).lifecycle).toBe("active");

    await client.roles.unassign(created.id, roleB.id);
    expect([...(await client.users.get(created.id)).roleIds]).toEqual([roleA.id]);
    await client.roles.assign(created.id, roleB.id);
    expect([...(await client.users.get(created.id)).roleIds].sort()).toEqual(
      [roleA.id, roleB.id].sort(),
    );

    await client.users.delete(created.id);
    let status: number | undefined;
    try {
      await client.users.get(created.id);
    } catch (err) {
      status = (err as { statusCode?: number }).statusCode;
    }
    expect(status).toBe(404);
    await client.roles.delete(roleA.id);
    await client.roles.delete(roleB.id);
  });

  test("users create withoutEmailLoginKey is accepted without an email", async ({ daemon }) => {
    const client = await sdk(daemon.adminUrl);
    const username = uniq("noemail");
    const created = await client.users.create({
      username,
      withoutEmailLoginKey: true,
      tenantId: "system",
      roleIds: [],
    });
    expect((await client.users.get(created.id)).username).toBe(username);
    await client.users.delete(created.id);
  });

  test("users cert bindings round-trip through getUserByCertThumbprint", async ({ daemon }) => {
    const client = await sdk(daemon.adminUrl);
    const username = uniq("certuser");
    const user = await client.users.create({
      username,
      email: `${username}@example.test`,
      tenantId: "system",
    });
    // A base64url SHA-256 SPKI thumbprint, the form the handler validates.
    const thumbprint = Buffer.alloc(32, 0xab).toString("base64url");
    const notAfterUnix = Math.floor(Date.now() / 1000) + 3600;
    await client.users.addCertBinding(user.id, thumbprint, notAfterUnix);
    const bound = await client.users.getUserByCertThumbprint(thumbprint);
    expect(bound.id).toBe(user.id);

    await client.users.removeCertBinding(user.id, thumbprint);
    let status: number | undefined;
    try {
      await client.users.getUserByCertThumbprint(thumbprint);
    } catch (err) {
      status = (err as { statusCode?: number }).statusCode;
    }
    expect(status).toBe(404);
    await client.users.delete(user.id);
  });

  // POST /v1/tenants answers 501 on this daemon: it wires no tenant
  // onboarding service (pkg/api/rest/handlers_tenants.go), so create and
  // delete cannot run here. The read and update routes are checked on the
  // built-in "system" tenant, and every change is put back afterwards.
  test("tenants get/update/list round-trip name and isolationType", async ({ daemon }) => {
    const client = await sdk(daemon.adminUrl);
    const before = (await client.tenants.get("system")).tenant;
    expect(before.id).toBe("system");
    expect(before.isolationType).toBe("prefix");
    expect(before.createdAt).not.toBe("");
    expect(before.updatedAt).not.toBe("");

    const renamed = uniq("system-name");
    try {
      const updated = await client.tenants.update("system", {
        name: renamed,
        isolationType: "prefix",
      });
      expect(updated.tenant.name).toBe(renamed);
      expect(updated.tenant.isolationType).toBe("prefix");
      expect((await client.tenants.get("system")).tenant.name).toBe(renamed);
      const listed = await client.tenants.list();
      expect(listed.tenants.find((t) => t.id === "system")?.name).toBe(renamed);
    } finally {
      await client.tenants.update("system", { name: before.name });
    }
    expect((await client.tenants.get("system")).tenant.name).toBe(before.name);
  });

  test("tenants quotas and entitlements round-trip every field", async ({ daemon }) => {
    const client = await sdk(daemon.adminUrl);
    const id = "system";
    const before = (await client.tenants.get(id)).tenant;

    // Generous values, so no other spec sharing this daemon hits them.
    const quotas = {
      maxDatabases: 1011,
      maxRaftGroups: 1012,
      maxReplicasPerGroup: 7,
      maxTotalReplicas: 1014,
      maxDedicatedWalGroups: 1015,
      maxCollections: 1016,
      maxKeys: 1_000_017,
      maxStorageBytes: 1_000_000_018,
      maxDatabaseSizeBytes: 1_000_000_019,
      maxUsers: 1020,
      maxRoles: 1021,
      maxCertificates: 1022,
      maxBackups: 1023,
      maxVectorDims: 1024,
      maxTsMetrics: 1025,
      systemBarrierQuota: 1026,
    };
    const entitlements = {
      allowedIsolationModes: ["prefix", "barrier"],
      allowDedicatedRaft: true,
      allowVolumeStorage: true,
      allowSystemManagedBarrier: true,
      allowDedicatedWal: true,
      allowedBarrierModes: ["passphrase"],
      allowedVolumePools: ["wire-pool"],
    };
    try {
      const q = await client.tenants.updateQuotas({ tenantId: id, quotas });
      expect(q.tenant.quotas).toEqual(quotas);
      expect((await client.tenants.get(id)).tenant.quotas).toEqual(quotas);

      const e = await client.tenants.updateEntitlements({ tenantId: id, entitlements });
      expect(e.tenant.entitlements).toMatchObject(entitlements);
      expect((await client.tenants.get(id)).tenant.entitlements).toMatchObject(entitlements);
    } finally {
      if (before.quotas) await client.tenants.updateQuotas({ tenantId: id, quotas: before.quotas });
      await client.tenants.updateEntitlements({ tenantId: id, entitlements: before.entitlements ?? {} });
    }
  });
});
