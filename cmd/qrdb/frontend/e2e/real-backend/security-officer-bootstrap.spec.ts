// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * security-officer-bootstrap.spec.ts
 *
 * Share-holder bootstrap against a real Shamir (3-of-5) daemon:
 *
 *   - Shamir initialization with no holders enrolled is refused.
 *   - Enrolment returns the CA-issued certificate and what was enrolled —
 *     never a share or a share index.
 *   - The holder list shows every enrolled holder; after initialization each
 *     has an envelope pending.
 *   - Initialization is share-free: threshold, total_shares, share_count.
 *   - Each holder claims its envelope exactly once (CLI); a second claim fails.
 *   - A claim without a client certificate is 401; with another holder's
 *     certificate it is 403.
 *   - A quorum of holders seals the barrier; a direct seal is refused.
 *
 * Holders claim and submit with the qrdb CLI (see fixtures/officer-bootstrap).
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  newHolder,
  enrolHolders,
  bootstrapShamirBarrier,
  claimEnvelope,
  mustClaim,
  holderContext,
  quorum,
  waitForSealed,
  type Holder,
} from "./fixtures/officer-bootstrap.ts";

function fiveHolders(prefix: string): Holder[] {
  return ["a", "b", "c", "d", "e"].map((s, i) => newHolder(`${prefix}-${s}`, i === 0 ? "so" : "custodian"));
}

test.describe("Share-holder bootstrap", () => {
  test("shamir/initialize with no holders enrolled is refused", async ({ request, shamirDaemon }) => {
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    expect(resp.status()).toBeGreaterThanOrEqual(400);
    const body = (await resp.json()) as Record<string, unknown>;
    expect(typeof body["error"]).toBe("string");
  });

  test("enrolment returns the issued certificate and what was enrolled, never a share", async ({
    request,
    shamirDaemon,
  }) => {
    const holder = newHolder("enrol-alice", "custodian");
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/officers`, {
      data: { username: holder.username, role: holder.role, csr_pem: holder.csrPem },
    });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as Record<string, unknown>;
    expect(String(body["cert_pem"])).toContain("BEGIN CERTIFICATE");
    expect(body["role"]).toBe("custodian");
    expect(body["factors"]).toContain("mtls");
    for (const field of ["share", "shares", "share_bytes", "share_index", "envelope"]) {
      expect(body[field]).toBeUndefined();
    }
  });

  test("the holder list shows every holder, each with an envelope pending after initialization", async ({
    request,
    shamirDaemon,
  }) => {
    const holders = fiveHolders("list");
    await enrolHolders(request, shamirDaemon.adminUrl, holders);

    const before = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/officers`);
    expect(before.status()).toBe(200);
    const listed = ((await before.json()) as { officers: { username: string; share_pending: boolean }[] }).officers;
    expect(listed.map((o) => o.username).sort()).toEqual(holders.map((h) => h.username).sort());
    expect(listed.every((o) => !o.share_pending)).toBe(true);

    const init = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    expect(init.status()).toBe(200);
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const after = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/officers`);
    const pending = ((await after.json()) as { officers: { share_pending: boolean }[] }).officers;
    expect(pending).toHaveLength(5);
    expect(pending.every((o) => o.share_pending)).toBe(true);
  });

  test("initialization is share-free: threshold, total_shares, share_count and nothing else", async ({
    request,
    shamirDaemon,
  }) => {
    await enrolHolders(request, shamirDaemon.adminUrl, fiveHolders("init"));
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as Record<string, unknown>;
    expect(body).toEqual({ threshold: 3, total_shares: 5, share_count: 5 });
  });

  test("each holder claims exactly once; a second claim fails", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    const again = claimEnvelope(shamirDaemon, holders[0]!);
    expect(again.status).not.toBe(0);
    expect(again.stderr + again.stdout).toMatch(/claimed|no pending/i);
  });

  test("a claim without a client certificate is 401", async ({ request, shamirDaemon }) => {
    const holders = fiveHolders("nocert");
    await enrolHolders(request, shamirDaemon.adminUrl, holders);
    await request.post(`${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/officers/nocert-a/claim/begin`);
    expect(resp.status()).toBe(401);
  });

  test("a claim presenting another holder's certificate is 403", async ({ request, playwright, shamirDaemon }) => {
    const holders = fiveHolders("wrong");
    await enrolHolders(request, shamirDaemon.adminUrl, holders);
    await request.post(`${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    // wrong-b's certificate, claiming wrong-a's envelope.
    const ctx = await holderContext(playwright.request, shamirDaemon.adminUrl, holders[1]!);
    try {
      const resp = await ctx.post(`${shamirDaemon.adminUrl}/v1/barrier/officers/wrong-a/claim/begin`);
      expect(resp.status()).toBe(403);
    } finally {
      await ctx.dispose();
    }
    // The rightful holder still claims.
    mustClaim(shamirDaemon, holders[0]!);
  });

  test("a quorum of holders seals the barrier, and a direct seal is refused", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    const direct = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/seal`);
    expect(direct.status()).toBeGreaterThanOrEqual(400);
    await waitForSealed(request, shamirDaemon.adminUrl, false);

    const first = quorum(shamirDaemon, holders, 2, "seal");
    expect(first).toMatchObject({ op: "seal", required: 3, submitted: 2, complete: false });
    const last = quorum(shamirDaemon, holders.slice(2), 1, "seal");
    expect(last.complete).toBe(true);
    await waitForSealed(request, shamirDaemon.adminUrl, true);
  });
});
