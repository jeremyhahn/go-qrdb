// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * per-tenant-barrier.spec.ts
 *
 * Per-tenant barrier initialization and status on a seal-enabled+unsealed daemon.
 *
 * Per-tenant barrier routes (requires seal.enabled=true daemon):
 *   POST /v1/tenants/{id}/barrier/init/complete {passphrase}  -> 204
 *   GET  /v1/tenants/{id}/barrier/status               -> 200 {tenant_id, sealed, initialized, strategy, hardware_backed}
 *   POST /v1/tenants/{id}/barrier/unseal {key}         -> key must be BASE64 encoded
 *
 * API asymmetry note:
 *   POST .../barrier/init/complete takes the `passphrase` as base64 bytes.
 *   POST .../barrier/unseal expects a BASE64-encoded `key` field.
 *   Providing a plaintext string to the unseal endpoint returns 400
 *   "illegal base64 data at input byte N". This is the current server
 *   contract; callers must base64-encode the key before sending.
 *
 * Tenant creation requires an `id` field; omitting it returns 400 "empty
 * tenant ID". The isolation_type must be "dedicated" for per-tenant barriers.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { b64, waitForHealthExported } from "./fixtures/daemon-helpers.ts";

function tenantId(): string {
  return `e2e-tb-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

test.describe("Per-tenant barrier", () => {
  test("init -> status shows initialized:true, strategy:passphrase", async ({
    request,
    sealedDaemon,
  }) => {
    // Initialize the system barrier first.
    const initResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: b64("sys-barrier-secret") },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(204);
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    const tid = tenantId();

    // Create tenant with explicit id and dedicated isolation.
    const createResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/tenants`,
      {
        data: { id: tid, name: "Barrier Test Corp", isolation_type: "dedicated" },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(createResp.status()).toBe(201);
    const tenant = await createResp.json() as Record<string, unknown>;
    expect(tenant["id"]).toBe(tid);

    // Status before init: sealed:true, initialized:false.
    const beforeInit = await request.get(
      `${sealedDaemon.adminUrl}/v1/tenants/${tid}/barrier/status`,
    );
    expect(beforeInit.status()).toBe(200);
    const preStatus = await beforeInit.json() as Record<string, unknown>;
    expect(preStatus["tenant_id"]).toBe(tid);
    expect(preStatus["initialized"]).toBe(false);
    expect(preStatus["sealed"]).toBe(true);

    // Initialize per-tenant barrier with a passphrase.
    const barrierInit = await request.post(
      `${sealedDaemon.adminUrl}/v1/tenants/${tid}/barrier/init/complete`,
      {
        data: { passphrase: b64("tenant-passphrase") },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(barrierInit.status()).toBe(204);

    // Status after init: initialized:true, sealed:false, strategy:passphrase.
    const afterInit = await request.get(
      `${sealedDaemon.adminUrl}/v1/tenants/${tid}/barrier/status`,
    );
    expect(afterInit.status()).toBe(200);
    const postStatus = await afterInit.json() as Record<string, unknown>;
    expect(postStatus["tenant_id"]).toBe(tid);
    expect(postStatus["initialized"]).toBe(true);
    expect(postStatus["sealed"]).toBe(false);
    expect(postStatus["strategy"]).toBe("passphrase");

    // Cleanup.
    await request.delete(`${sealedDaemon.adminUrl}/v1/tenants/${encodeURIComponent(tid)}`);
  });

  test("nonexistent tenant barrier/status returns 404 (route IS registered)", async ({
    request,
    sealedDaemon,
  }) => {
    // Initialize the system barrier to get a healthy daemon.
    await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: b64("sys-barrier-secret-2") },
        headers: { "Content-Type": "application/json" },
      },
    );
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    // The route IS registered. A nonexistent tenant ID returns 404 {error:"tenant: not found"}.
    // (The old spec incorrectly claimed the route was unregistered returning 405.)
    const resp = await request.get(
      `${sealedDaemon.adminUrl}/v1/tenants/nonexistent-tenant-xyz/barrier/status`,
    );
    expect(resp.status()).toBe(404);
    const body = await resp.json() as Record<string, unknown>;
    expect(typeof body["error"]).toBe("string");
    expect((body["error"] as string).toLowerCase()).toContain("not found");
  });

  test("unseal endpoint rejects plaintext key (base64 required)", async ({
    request,
    sealedDaemon,
  }) => {
    // Initialize system barrier.
    await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: b64("sys-barrier-secret-3") },
        headers: { "Content-Type": "application/json" },
      },
    );
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    const tid = tenantId();

    // Create tenant.
    await request.post(
      `${sealedDaemon.adminUrl}/v1/tenants`,
      {
        data: { id: tid, name: "Unseal Test", isolation_type: "dedicated" },
        headers: { "Content-Type": "application/json" },
      },
    );

    // Initialize per-tenant barrier.
    await request.post(
      `${sealedDaemon.adminUrl}/v1/tenants/${tid}/barrier/init/complete`,
      {
        data: { passphrase: b64("my-tenant-secret") },
        headers: { "Content-Type": "application/json" },
      },
    );

    // Unseal with plaintext key — must return 400 (base64 decode fails).
    // Both `passphrase` and `key` are []byte fields, sent as base64.
    const plainResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/tenants/${tid}/barrier/unseal`,
      {
        data: { key: "my-tenant-secret" },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(plainResp.status()).toBe(400);
    const plainBody = await plainResp.json() as Record<string, unknown>;
    expect(typeof plainBody["error"]).toBe("string");
    expect((plainBody["error"] as string).toLowerCase()).toContain("base64");

    // Cleanup.
    await request.delete(`${sealedDaemon.adminUrl}/v1/tenants/${encodeURIComponent(tid)}`);
  });
});
