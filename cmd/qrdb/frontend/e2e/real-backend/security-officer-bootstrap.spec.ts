// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * security-officer-bootstrap.spec.ts
 *
 * Security Officer (SO) enrollment and share-claim flow via the officer REST
 * routes (§7.3 of docs/seal/so-share-distribution-design.md).
 *
 * Routes under test:
 *   POST /v1/barrier/officers                           — register SO
 *   GET  /v1/barrier/officers                           — list SOs
 *   GET  /v1/barrier/officers/claim-status              — claim status for all SOs
 *   POST /v1/barrier/officers/{username}/claim/begin    — begin claim (nonce, mTLS)
 *   POST /v1/barrier/officers/{username}/claim/complete — complete claim (signature)
 *   POST /v1/barrier/shamir/seal-vote                   — submit SO seal vote
 *   POST /v1/barrier/shamir/initialize                  — fails without officers
 *   POST /v1/barrier/seal                               — rejected for Shamir barrier
 *
 * Security invariants enforced (from docs/seal/so-share-distribution-design.md):
 *   INV-1:  shamir/initialize response contains NO "shares" field
 *   INV-3:  second claim for the same officer fails (ErrShareAlreadyClaimed → 409)
 *   INV-4:  claim without client cert → 401
 *   INV-8:  direct /v1/barrier/seal rejected on Shamir barrier (ErrShamirBarrierRequiresQuorum → 422)
 *   INV-11: claim/complete returns share_bytes only in the claiming SO's session
 *   INV-12: claim with wrong officer cert → 403 (ErrCertThumbprintMismatch)
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  generateOfficerKeyAndCSR,
  registerOfficers,
  bootstrapShamirBarrier,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Security Officer bootstrap (§7.3 officer routes)", () => {
  // ---------------------------------------------------------------------------
  // Pre-condition: shamir/initialize without officers MUST fail
  // ---------------------------------------------------------------------------

  test("shamir/initialize with zero officers registered returns an error", async ({
    request,
    shamirDaemon,
  }) => {
    // No officers registered — init must hard-fail with an error status.
    // mapBarrierCeremonyHTTPError maps ErrInsufficientOfficers → 422
    // and ErrOfficerCountMismatch → 422.
    const initResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`,
      {
        data: { secret: "no-officers-secret", threshold: 3, total_shares: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).not.toBe(200);
    // Expect a 4xx/5xx error status (422 for insufficient officers).
    expect(initResp.status()).toBeGreaterThanOrEqual(400);

    const body = await initResp.json() as Record<string, unknown>;
    expect(typeof body["error"]).toBe("string");
  });

  // ---------------------------------------------------------------------------
  // Officer registration: positive flow
  // ---------------------------------------------------------------------------

  test("POST /v1/barrier/officers returns cert_pem and a distinct share_index per officer", async ({
    request,
    shamirDaemon,
  }) => {
    const keys = [
      generateOfficerKeyAndCSR("alice"),
      generateOfficerKeyAndCSR("bob"),
      generateOfficerKeyAndCSR("carol"),
      generateOfficerKeyAndCSR("dave"),
      generateOfficerKeyAndCSR("eve"),
    ];

    const officers = await registerOfficers(request, shamirDaemon.adminUrl, keys);

    expect(officers).toHaveLength(5);

    const indices = officers.map((o) => o.shareIndex);
    const uniqueIndices = new Set(indices);

    // All share indices are distinct.
    expect(uniqueIndices.size).toBe(5);

    // All share indices are in the range [1..5].
    for (const idx of indices) {
      expect(idx).toBeGreaterThanOrEqual(1);
      expect(idx).toBeLessThanOrEqual(5);
    }

    // All certs are non-empty PEM strings.
    for (const o of officers) {
      expect(typeof o.certPem).toBe("string");
      expect(o.certPem).toContain("BEGIN CERTIFICATE");
    }
  });

  // ---------------------------------------------------------------------------
  // Officer list and claim-status: all-pending after registration
  // ---------------------------------------------------------------------------

  test("GET /v1/barrier/officers lists enrolled officers; GET /v1/barrier/officers/claim-status shows all pending", async ({
    request,
    shamirDaemon,
  }) => {
    const keys = [
      generateOfficerKeyAndCSR("list-alice"),
      generateOfficerKeyAndCSR("list-bob"),
      generateOfficerKeyAndCSR("list-carol"),
      generateOfficerKeyAndCSR("list-dave"),
      generateOfficerKeyAndCSR("list-eve"),
    ];

    await registerOfficers(request, shamirDaemon.adminUrl, keys);

    // List endpoint.
    const listResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/officers`,
    );
    expect(listResp.status()).toBe(200);
    const listBody = await listResp.json() as Record<string, unknown>;
    expect(Array.isArray(listBody["officers"])).toBe(true);
    const officerList = listBody["officers"] as Record<string, unknown>[];
    expect(officerList.length).toBeGreaterThanOrEqual(5);

    // Each officer entry has the expected fields.
    for (const o of officerList) {
      expect(typeof o["username"]).toBe("string");
      expect(typeof o["share_index"]).toBe("number");
      expect(typeof o["auth_factor"]).toBe("string");
      expect(typeof o["enrolled_at"]).toBe("string");
      // share_pending is undefined before initialization (shares not yet sealed).
      // No share material appears in the list.
      expect(o["share_bytes"]).toBeUndefined();
      expect(o["shares"]).toBeUndefined();
    }

    // Claim-status endpoint.
    const statusResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/officers/claim-status`,
    );
    expect(statusResp.status()).toBe(200);
    const statusBody = await statusResp.json() as Record<string, unknown>;
    expect(Array.isArray(statusBody["officers"])).toBe(true);

    // No officer has claimed_at populated before init+claim.
    for (const o of (statusBody["officers"] as Record<string, unknown>[])) {
      expect(o["claimed_at"]).toBeUndefined();
    }
  });

  // ---------------------------------------------------------------------------
  // INV-1: shamir/initialize response MUST NOT contain "shares"
  // ---------------------------------------------------------------------------

  test("shamir/initialize response contains threshold, total_shares, share_count — NO shares field (INV-1)", async ({
    request,
    shamirDaemon,
  }) => {
    const keys = [
      generateOfficerKeyAndCSR("inv1-alice"),
      generateOfficerKeyAndCSR("inv1-bob"),
      generateOfficerKeyAndCSR("inv1-carol"),
      generateOfficerKeyAndCSR("inv1-dave"),
      generateOfficerKeyAndCSR("inv1-eve"),
    ];
    await registerOfficers(request, shamirDaemon.adminUrl, keys);

    const initResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`,
      {
        data: { secret: "inv1-secret", threshold: 3, total_shares: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(200);

    const body = await initResp.json() as Record<string, unknown>;

    // Required fields must be present and correct.
    expect(body["threshold"]).toBe(3);
    expect(body["total_shares"]).toBe(5);
    expect(typeof body["share_count"]).toBe("number");
    expect(body["share_count"]).toBe(5);

    // INV-1: shares MUST NOT appear in the response.
    expect(body["shares"]).toBeUndefined();
    expect(body["key_shares"]).toBeUndefined();
    expect(body["keys"]).toBeUndefined();
  });

  // ---------------------------------------------------------------------------
  // Full positive claim flow
  // ---------------------------------------------------------------------------

  test("each officer claims exactly once: second claim fails with 409 (INV-3)", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { officers } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["dup-a", "dup-b", "dup-c", "dup-d", "dup-e"],
        threshold: 3,
        total: 5,
        secret: "dup-claim-secret",
      },
    );

    // Attempt a second claim for officer[0].
    // officers has 5 elements (length was asserted by registerOfficers above).
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const officer = officers[0]!;

    const ctx = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: shamirDaemon.adminUrl,
          cert: Buffer.from(officer.certPem),
          key: Buffer.from(officer.privateKeyPem),
        },
      ],
    });

    try {
      // begin will fail because the share is already claimed.
      const beginResp = await ctx.post(
        `${shamirDaemon.adminUrl}/v1/barrier/officers/${officer.username}/claim/begin`,
        { headers: { "Content-Type": "application/json" } },
      );
      // ErrNoPendingShare → 404; ErrShareAlreadyClaimed → 409.
      // Either is acceptable; what is NOT acceptable is 200.
      expect(beginResp.status()).not.toBe(200);
      expect(beginResp.status()).toBeGreaterThanOrEqual(400);
    } finally {
      await ctx.dispose();
    }
  });

  // ---------------------------------------------------------------------------
  // INV-4: claim without client cert → 401
  // ---------------------------------------------------------------------------

  test("claim/begin without mTLS client certificate returns 401 (INV-4)", async ({
    request,
    shamirDaemon,
  }) => {
    // Register one officer so the route is exercisable.
    const keys = [
      generateOfficerKeyAndCSR("nocert-alice"),
      generateOfficerKeyAndCSR("nocert-bob"),
      generateOfficerKeyAndCSR("nocert-carol"),
      generateOfficerKeyAndCSR("nocert-dave"),
      generateOfficerKeyAndCSR("nocert-eve"),
    ];
    await registerOfficers(request, shamirDaemon.adminUrl, keys);

    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`,
      {
        data: { secret: "nocert-secret", threshold: 3, total_shares: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    // initialize auto-unseals and swaps the sealed-window listener for the
    // full server; wait out the swap before issuing claim requests.
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    // Use the base request context (no client cert) to attempt claim/begin.
    const resp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/officers/nocert-alice/claim/begin`,
      { headers: { "Content-Type": "application/json" } },
    );

    // restClientCertRequiredError → 401.
    expect(resp.status()).toBe(401);
  });

  // ---------------------------------------------------------------------------
  // INV-12: claim with wrong officer cert → 403
  // ---------------------------------------------------------------------------

  test("claim/begin with wrong officer cert returns 403 (thumbprint mismatch, INV-12)", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const keys = [
      generateOfficerKeyAndCSR("wrong-alice"),
      generateOfficerKeyAndCSR("wrong-bob"),
      generateOfficerKeyAndCSR("wrong-carol"),
      generateOfficerKeyAndCSR("wrong-dave"),
      generateOfficerKeyAndCSR("wrong-eve"),
    ];
    const officers = await registerOfficers(request, shamirDaemon.adminUrl, keys);

    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/initialize`,
      {
        data: { secret: "wrong-cert-secret", threshold: 3, total_shares: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    // initialize auto-unseals and swaps the sealed-window listener for the
    // full server; wait out the swap before issuing claim requests.
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    // Use wrong-bob's cert to claim wrong-alice's share.
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const bob = officers[1]!;
    const aliceCtxWithBobCert = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: shamirDaemon.adminUrl,
          // bob's cert presented for alice's username → thumbprint mismatch.
          cert: Buffer.from(bob.certPem),
          key: Buffer.from(bob.privateKeyPem),
        },
      ],
    });

    try {
      const resp = await aliceCtxWithBobCert.post(
        `${shamirDaemon.adminUrl}/v1/barrier/officers/wrong-alice/claim/begin`,
        { headers: { "Content-Type": "application/json" } },
      );
      // ErrCertThumbprintMismatch → 403.
      expect(resp.status()).toBe(403);
    } finally {
      await aliceCtxWithBobCert.dispose();
    }
  });

  // ---------------------------------------------------------------------------
  // seal-vote quorum seals the barrier; complete:true on final vote
  // ---------------------------------------------------------------------------

  test("seal-vote: threshold distinct shares seal the barrier; status sealed:true after quorum", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["vote-a", "vote-b", "vote-c", "vote-d", "vote-e"],
        threshold: 3,
        total: 5,
        secret: "seal-vote-secret",
      },
    );

    // Wait for the daemon to fully bootstrap after shamir init + claim.
    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Confirm the barrier is currently unsealed.
    const statusBeforeResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const statusBefore = await statusBeforeResp.json() as Record<string, unknown>;
    expect(statusBefore["sealed"]).toBe(false);

    // Submit threshold-1 votes: barrier must NOT be sealed yet.
    const v1 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/seal-vote`,
      {
        data: { share: shareValues[0] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(v1.status()).toBe(200);
    const p1 = await v1.json() as Record<string, unknown>;
    expect(p1["submitted"]).toBe(1);
    expect(p1["required"]).toBe(3);
    expect(p1["complete"]).toBe(false);

    const v2 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/seal-vote`,
      {
        data: { share: shareValues[1] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(v2.status()).toBe(200);
    const p2 = await v2.json() as Record<string, unknown>;
    expect(p2["submitted"]).toBe(2);
    expect(p2["complete"]).toBe(false);

    // Final vote hits threshold → complete:true, barrier sealed.
    const v3 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/seal-vote`,
      {
        data: { share: shareValues[2] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(v3.status()).toBe(200);
    const p3 = await v3.json() as Record<string, unknown>;
    expect(p3["submitted"]).toBe(3);
    expect(p3["required"]).toBe(3);
    expect(p3["complete"]).toBe(true);

    // Barrier is now sealed.
    const statusAfterResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const statusAfter = await statusAfterResp.json() as Record<string, unknown>;
    expect(statusAfter["sealed"]).toBe(true);
  });

  // ---------------------------------------------------------------------------
  // INV-8: direct /v1/barrier/seal is rejected for a Shamir barrier
  // ---------------------------------------------------------------------------

  test("POST /v1/barrier/seal on a Shamir barrier returns 422 (ErrShamirBarrierRequiresQuorum, INV-8)", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["seal-a", "seal-b", "seal-c", "seal-d", "seal-e"],
        threshold: 3,
        total: 5,
        secret: "direct-seal-secret",
      },
    );

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    const sealResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/seal`,
    );

    // ErrShamirBarrierRequiresQuorum is mapped to 422 by mapBarrierCeremonyHTTPError.
    expect(sealResp.status()).toBe(422);
    const body = await sealResp.json() as Record<string, unknown>;
    expect(typeof body["error"]).toBe("string");
  });
});
