// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * officer-claim-auth-enabled.spec.ts
 *
 * Auth-enabled variant of the Security Officer share-claim flow. Every other
 * sealed-daemon spec runs with --auth-enabled=false, which means the
 * identity-gated claim authorization (permission.IdentityGated in
 * pkg/auth/middleware/mapping.go) was never exercised end-to-end through the
 * REAL auth middleware. This spec closes that gap using the authShamirDaemon
 * fixture (--auth-enabled=true, --tls-client-auth omitted so the auth-derived
 * VerifyClientCertIfGiven TLS posture applies).
 *
 * What this proves:
 *   1. Officer registration + shamir/initialize succeed during the sealed
 *      window (the sealed-window server runs WITHOUT auth middleware — no
 *      user store exists pre-bootstrap), and every officer can then claim
 *      their share through the post-unseal FULL server whose auth middleware
 *      is active: the claim routes short-circuit RBAC via
 *      permission.IdentityGated, requiring only the enrolled mTLS identity
 *      (cluster-CA-signed officer cert passes VerifyClientCertIfGiven; the
 *      handler's thumbprint match is the per-officer authorization).
 *   2. The RBAC middleware is genuinely active on that same server:
 *      GET /v1/barrier/officers (permission.SealOfficerList) rejects an
 *      anonymous caller with 401 and authorizes the bootstrapped admin
 *      mTLS identity with 200.
 *   3. Identity-gated does NOT mean identity-blind: a claim/begin presented
 *      with a DIFFERENT enrolled officer's certificate is rejected with 403
 *      (thumbprint mismatch) even though that certificate authenticates.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  generateOfficerKeyAndCSR,
  registerOfficers,
  bootstrapShamirBarrier,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Officer share claim with auth enabled (identity-gated authz)", () => {
  // ---------------------------------------------------------------------------
  // Full claim flow through the real auth middleware
  // ---------------------------------------------------------------------------

  test("all officers claim their shares through the auth-enabled full server (permission.IdentityGated e2e)", async ({
    request,
    playwright,
    authShamirDaemon,
  }) => {
    // bootstrapShamirBarrier registers 5 officers and initializes the barrier
    // during the (auth-exempt) sealed window, waits for the post-unseal full
    // server, then claims each share over the officer's own mTLS session —
    // the claims traverse the REAL auth middleware on this daemon.
    const { shareValues, officers } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      authShamirDaemon.adminUrl,
      {
        officerUsernames: ["auth-a", "auth-b", "auth-c", "auth-d", "auth-e"],
        threshold: 3,
        total: 5,
        secret: "auth-enabled-claim-secret",
      },
    );

    expect(officers).toHaveLength(5);
    expect(shareValues).toHaveLength(5);

    // Every claimed share value is a distinct non-empty combinable share.
    for (const v of shareValues) {
      expect(typeof v).toBe("string");
      expect(v.length).toBeGreaterThan(0);
    }
    expect(new Set(shareValues).size).toBe(5);

    // The barrier is unsealed and reports the configured Shamir parameters.
    // GET /v1/barrier/status maps to permission.SealStatus — RBAC-gated on
    // the auth-enabled full server (default-deny), so the check requires the
    // bootstrapped admin mTLS identity; an anonymous probe must get 401.
    const anonStatusResp = await request.get(
      `${authShamirDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(anonStatusResp.status()).toBe(401);

    const adminCtx = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: authShamirDaemon.adminUrl,
          certPath: authShamirDaemon.adminCertPath,
          keyPath: authShamirDaemon.adminKeyPath,
        },
      ],
    });
    try {
      const statusResp = await adminCtx.get(
        `${authShamirDaemon.adminUrl}/v1/barrier/status`,
      );
      expect(statusResp.status()).toBe(200);
      const status = (await statusResp.json()) as Record<string, unknown>;
      expect(status["sealed"]).toBe(false);
      expect(status["strategy"]).toBe("shamir");
      expect(status["shamir_threshold"]).toBe(3);
      expect(status["shamir_total"]).toBe(5);
    } finally {
      await adminCtx.dispose();
    }
  });

  // ---------------------------------------------------------------------------
  // RBAC default-deny is genuinely active on the same server
  // ---------------------------------------------------------------------------

  test("RBAC-gated officer list: anonymous → 401, admin mTLS identity → 200 (default-deny active)", async ({
    request,
    playwright,
    authShamirDaemon,
  }) => {
    await bootstrapShamirBarrier(
      request,
      playwright.request,
      authShamirDaemon.adminUrl,
      {
        officerUsernames: ["rbac-a", "rbac-b", "rbac-c", "rbac-d", "rbac-e"],
        threshold: 3,
        total: 5,
        secret: "auth-enabled-rbac-secret",
      },
    );

    // GET /v1/barrier/officers maps to permission.SealOfficerList — a normal
    // RBAC-gated route, NOT identity-gated. On the auth-enabled full server
    // an anonymous request must be rejected. (On the sealed-window server
    // this same route is reachable anonymously; the contrast here is the
    // point: the post-unseal server enforces RBAC.)
    const anonResp = await request.get(
      `${authShamirDaemon.adminUrl}/v1/barrier/officers`,
    );
    expect(anonResp.status()).toBe(401);

    // The bootstrapped admin mTLS identity is authorized for the same route.
    const adminCtx = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: authShamirDaemon.adminUrl,
          certPath: authShamirDaemon.adminCertPath,
          keyPath: authShamirDaemon.adminKeyPath,
        },
      ],
    });
    try {
      const adminResp = await adminCtx.get(
        `${authShamirDaemon.adminUrl}/v1/barrier/officers`,
      );
      expect(adminResp.status()).toBe(200);
      const body = (await adminResp.json()) as Record<string, unknown>;
      expect(Array.isArray(body["officers"])).toBe(true);
      const officerList = body["officers"] as Record<string, unknown>[];
      expect(officerList.length).toBeGreaterThanOrEqual(5);
      // No share material in the listing.
      for (const o of officerList) {
        expect(o["share_bytes"]).toBeUndefined();
        expect(o["shares"]).toBeUndefined();
      }
    } finally {
      await adminCtx.dispose();
    }
  });

  // ---------------------------------------------------------------------------
  // Identity-gated ≠ identity-blind: wrong officer cert → 403 under auth
  // ---------------------------------------------------------------------------

  test("claim/begin with another enrolled officer's cert returns 403 under auth (thumbprint binding)", async ({
    request,
    playwright,
    authShamirDaemon,
  }) => {
    // Register + initialize manually (no claims) so the shares stay pending.
    const keys = [
      generateOfficerKeyAndCSR("xauth-alice"),
      generateOfficerKeyAndCSR("xauth-bob"),
      generateOfficerKeyAndCSR("xauth-carol"),
      generateOfficerKeyAndCSR("xauth-dave"),
      generateOfficerKeyAndCSR("xauth-eve"),
    ];
    const officers = await registerOfficers(
      request,
      authShamirDaemon.adminUrl,
      keys,
    );

    const initResp = await request.post(
      `${authShamirDaemon.adminUrl}/v1/barrier/shamir/initialize`,
      {
        data: { secret: "auth-wrong-cert-secret", threshold: 3, total_shares: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(200);
    // initialize auto-unseals and swaps the sealed-window listener for the
    // full (auth-middleware) server; wait out the swap.
    await waitForHealthExported(authShamirDaemon.adminUrl, 60_000);

    // Present bob's cluster-CA-signed cert for alice's claim. The cert
    // AUTHENTICATES (passes VerifyClientCertIfGiven and the identity-gated
    // middleware), but the handler's thumbprint binding must reject it.
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const bob = officers[1]!;
    const ctx = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: authShamirDaemon.adminUrl,
          cert: Buffer.from(bob.certPem),
          key: Buffer.from(bob.privateKeyPem),
        },
      ],
    });
    try {
      const resp = await ctx.post(
        `${authShamirDaemon.adminUrl}/v1/barrier/officers/xauth-alice/claim/begin`,
        { headers: { "Content-Type": "application/json" } },
      );
      // ErrCertThumbprintMismatch → 403 (NOT 401: the identity authenticated).
      expect(resp.status()).toBe(403);
    } finally {
      await ctx.dispose();
    }
  });
});
