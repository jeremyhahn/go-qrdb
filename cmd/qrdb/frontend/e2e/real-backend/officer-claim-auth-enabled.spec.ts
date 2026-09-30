// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * officer-claim-auth-enabled.spec.ts
 *
 * The share-holder claim through a daemon whose auth middleware is active.
 *
 * Every other sealed-daemon spec runs with --auth-enabled=false. Here holders
 * are enrolled and the barrier initialized during the sealed window (which
 * runs without the auth middleware: no user store exists yet), and then every
 * holder claims its envelope through the FULL server, whose auth middleware
 * is on. The claim routes are identity-gated: the middleware requires only an
 * authenticated caller, and the handler checks the certificate against the
 * enrolled holder and verifies the nonce signature.
 *
 * What this proves:
 *   1. Holders claim through the auth-enabled server (CLI, holder cert).
 *   2. RBAC default-deny is active on the same server: the holder list and
 *      the barrier status are 401 anonymously and 200 for the admin identity.
 *   3. Identity-gated is not identity-blind: another holder's certificate is
 *      authenticated but refused (403) for a claim that is not its own.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  bootstrapShamirBarrier,
  enrolHolders,
  holderContext,
  newHolder,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Share-holder claim with auth enabled (identity-gated authz)", () => {
  test("every holder claims through the auth-enabled full server", async ({ request, playwright, authShamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, authShamirDaemon, 5, 3);
    expect(holders).toHaveLength(5);

    const anon = await request.get(`${authShamirDaemon.adminUrl}/v1/barrier/status`);
    expect(anon.status()).toBe(401);

    const admin = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        { origin: authShamirDaemon.adminUrl, certPath: authShamirDaemon.adminCertPath, keyPath: authShamirDaemon.adminKeyPath },
      ],
    });
    try {
      const status = await admin.get(`${authShamirDaemon.adminUrl}/v1/barrier/status`);
      expect(status.status()).toBe(200);
      expect(await status.json()).toMatchObject({ sealed: false, strategy: "shamir", shamir_threshold: 3, shamir_total: 5 });

      // Every envelope was claimed.
      const list = await admin.get(`${authShamirDaemon.adminUrl}/v1/barrier/officers`);
      const officers = ((await list.json()) as { officers: { share_pending: boolean }[] }).officers;
      expect(officers.every((o) => !o.share_pending)).toBe(true);
    } finally {
      await admin.dispose();
    }
  });

  test("the holder list is RBAC-gated: anonymous 401, admin 200, never a share", async ({
    request,
    playwright,
    authShamirDaemon,
  }) => {
    await bootstrapShamirBarrier(request, authShamirDaemon, 5, 3);

    const anon = await request.get(`${authShamirDaemon.adminUrl}/v1/barrier/officers`);
    expect(anon.status()).toBe(401);

    const admin = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        { origin: authShamirDaemon.adminUrl, certPath: authShamirDaemon.adminCertPath, keyPath: authShamirDaemon.adminKeyPath },
      ],
    });
    try {
      const resp = await admin.get(`${authShamirDaemon.adminUrl}/v1/barrier/officers`);
      expect(resp.status()).toBe(200);
      const officers = ((await resp.json()) as { officers: Record<string, unknown>[] }).officers;
      expect(officers.length).toBeGreaterThanOrEqual(5);
      for (const o of officers) {
        for (const field of ["share", "shares", "share_bytes", "envelope"]) expect(o[field]).toBeUndefined();
      }
    } finally {
      await admin.dispose();
    }
  });

  test("another holder's certificate authenticates but is refused for this claim (403)", async ({
    request,
    playwright,
    authShamirDaemon,
  }) => {
    const holders = ["xa", "xb", "xc", "xd", "xe"].map((s) => newHolder(`xauth-${s}`));
    await enrolHolders(request, authShamirDaemon.adminUrl, holders);
    const init = await request.post(`${authShamirDaemon.adminUrl}/v1/barrier/shamir/initialize`, {
      data: { threshold: 3, total_shares: 5 },
    });
    expect(init.status()).toBe(200);
    await waitForHealthExported(authShamirDaemon.adminUrl, 60_000);

    const ctx = await holderContext(playwright.request, authShamirDaemon.adminUrl, holders[1]!);
    try {
      const resp = await ctx.post(`${authShamirDaemon.adminUrl}/v1/barrier/officers/xauth-xa/claim/begin`);
      expect(resp.status()).toBe(403);
    } finally {
      await ctx.dispose();
    }
  });
});
