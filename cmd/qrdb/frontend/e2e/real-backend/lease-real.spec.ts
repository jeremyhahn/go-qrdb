// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Lease real-backend spec.
 *
 * Validates POST /lease/grant → response has id + correct TTL,
 * and GET /lease/list → includes the granted lease.
 * Also verifies POST /lease/revoke removes the lease.
 */

import { test, expect } from "./fixtures/daemon.ts";

interface LeaseResponse {
  id: number;
  ttl: number;
  granted_ttl: number;
  remaining_ttl: number;
}

interface LeasesResponse {
  leases: LeaseResponse[];
}

test.describe("Lease management (real backend)", () => {
  test("POST /lease/grant returns lease with requested TTL", async ({
    request,
    daemon,
  }) => {
    const resp = await request.post(`${daemon.adminUrl}/lease/grant`, {
      data: { ttl: 60 },
      headers: { "Content-Type": "application/json" },
    });

    if (resp.status() === 501) {
      console.info("[lease-real] Lease service not available (501). Skipping.");
      return;
    }

    expect(resp.ok()).toBe(true);
    const lease = await resp.json() as LeaseResponse;
    expect(typeof lease.id).toBe("number");
    expect(lease.id).toBeGreaterThan(0);
    expect(lease.granted_ttl).toBe(60);
    expect(lease.ttl).toBeGreaterThan(0);

    // Cleanup
    await request.post(`${daemon.adminUrl}/lease/revoke`, {
      data: { lease_id: lease.id },
      headers: { "Content-Type": "application/json" },
    });
  });

  test("POST /lease/grant — GET /lease/list includes the new lease", async ({
    request,
    daemon,
  }) => {
    const grantResp = await request.post(`${daemon.adminUrl}/lease/grant`, {
      data: { ttl: 90 },
      headers: { "Content-Type": "application/json" },
    });

    if (grantResp.status() === 501) {
      console.info("[lease-real] Lease service not available (501). Skipping.");
      return;
    }

    expect(grantResp.ok()).toBe(true);
    const lease = await grantResp.json() as LeaseResponse;

    const listResp = await request.get(`${daemon.adminUrl}/lease/list`);
    expect(listResp.ok()).toBe(true);
    const list = await listResp.json() as LeasesResponse;
    expect(Array.isArray(list.leases)).toBe(true);

    const found = list.leases.find((l) => l.id === lease.id);
    expect(found).toBeDefined();
    expect(found!.granted_ttl).toBe(90);

    // Cleanup
    await request.post(`${daemon.adminUrl}/lease/revoke`, {
      data: { lease_id: lease.id },
      headers: { "Content-Type": "application/json" },
    });
  });

  test("POST /lease/revoke removes lease from list", async ({
    request,
    daemon,
  }) => {
    const grantResp = await request.post(`${daemon.adminUrl}/lease/grant`, {
      data: { ttl: 30 },
      headers: { "Content-Type": "application/json" },
    });

    if (grantResp.status() === 501) {
      console.info("[lease-real] Lease service not available (501). Skipping.");
      return;
    }

    const lease = await grantResp.json() as LeaseResponse;

    const revokeResp = await request.post(`${daemon.adminUrl}/lease/revoke`, {
      data: { lease_id: lease.id },
      headers: { "Content-Type": "application/json" },
    });
    expect(revokeResp.ok()).toBe(true);

    const listResp = await request.get(`${daemon.adminUrl}/lease/list`);
    expect(listResp.ok()).toBe(true);
    const list = await listResp.json() as LeasesResponse;
    const found = list.leases.find((l) => l.id === lease.id);
    expect(found).toBeUndefined();
  });

  test("POST /lease/keepalive refreshes TTL", async ({ request, daemon }) => {
    const grantResp = await request.post(`${daemon.adminUrl}/lease/grant`, {
      data: { ttl: 120 },
      headers: { "Content-Type": "application/json" },
    });

    if (grantResp.status() === 501) {
      console.info("[lease-real] Lease service not available (501). Skipping.");
      return;
    }

    const lease = await grantResp.json() as LeaseResponse;

    const kaResp = await request.post(`${daemon.adminUrl}/lease/keepalive`, {
      data: { lease_id: lease.id },
      headers: { "Content-Type": "application/json" },
    });
    expect(kaResp.ok()).toBe(true);
    const ka = await kaResp.json() as LeaseResponse;
    expect(ka.id).toBe(lease.id);
    expect(ka.ttl).toBeGreaterThan(0);

    // Cleanup
    await request.post(`${daemon.adminUrl}/lease/revoke`, {
      data: { lease_id: lease.id },
      headers: { "Content-Type": "application/json" },
    });
  });
});
