// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * shamir-initialize-unseal.spec.ts
 *
 * End-to-end M-of-N Shamir threshold unseal flow.
 *
 * The daemon is started with seal.enabled=true + strategy=shamir (3-of-5).
 * Officers must be registered BEFORE calling POST /v1/barrier/shamir/initialize.
 * Initialize distributes shares to officer enclaves AND auto-unseals the
 * barrier (the daemon proceeds to full boot). Each officer then claims their
 * share via the mTLS claim handshake.
 *
 * Because initialize leaves the barrier UNSEALED, exercising the unseal-share
 * paths requires sealing first — and a shamir barrier can only be sealed via
 * a quorum of POST /v1/barrier/shamir/seal-vote calls (POST /v1/barrier/seal
 * returns 422 seal.ErrShamirBarrierRequiresQuorum).
 *
 * Covered flows:
 *   - shamir/initialize is share-free (INV-1); barrier auto-unseals
 *   - seal-vote quorum transitions the barrier to sealed
 *   - unseal-share: submitted increments, complete flips at threshold
 *   - batch unseal via POST /v1/barrier/shamir/unseal-shares
 *   - unseal-share on an UNSEALED barrier -> 409 Conflict
 *   - status reflects sealed:false + strategy:shamir after unseal
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  bootstrapShamirBarrier,
  sealBarrierViaQuorum,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Shamir initialize + unseal (3-of-5)", () => {
  test("shamir/initialize is share-free + auto-unseals; seal quorum then threshold unseal", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register 5 officers, initialize, claim all shares.
    // initialize auto-unseals the barrier and the daemon reaches full boot.
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["init-a", "init-b", "init-c", "init-d", "init-e"],
        threshold: 3,
        total: 5,
        secret: "shamir-init-secret",
      },
    );

    // Post-bootstrap the barrier is UNSEALED: submitting an unseal share now
    // must be rejected with 409 Conflict (seal.ErrAlreadyUnsealed).
    const conflictResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      {
        data: { share: shareValues[0] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(conflictResp.status()).toBe(409);

    // Seal via seal-vote quorum (the only way to seal a shamir barrier).
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      shareValues[0],
      shareValues[1],
      shareValues[2],
    ]);

    // Submit threshold shares one at a time to unseal.
    const r1 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      {
        data: { share: shareValues[0] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(r1.status()).toBe(200);
    const p1 = await r1.json() as Record<string, unknown>;
    expect(p1["submitted"]).toBe(1);
    expect(p1["required"]).toBe(3);
    expect(p1["complete"]).toBe(false);

    const r2 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      {
        data: { share: shareValues[1] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(r2.status()).toBe(200);
    const p2 = await r2.json() as Record<string, unknown>;
    expect(p2["submitted"]).toBe(2);
    expect(p2["complete"]).toBe(false);

    const r3 = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      {
        data: { share: shareValues[2] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(r3.status()).toBe(200);
    const p3 = await r3.json() as Record<string, unknown>;
    expect(p3["submitted"]).toBe(3);
    expect(p3["required"]).toBe(3);
    expect(p3["complete"]).toBe(true);

    // Daemon reaches full health after unseal.
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    // Status: unsealed + shamir.
    const statusResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(statusResp.status()).toBe(200);
    const status = await statusResp.json() as Record<string, unknown>;
    expect(status["sealed"]).toBe(false);
    expect(status["strategy"]).toBe("shamir");
    expect(typeof status["initialized_at"]).toBe("string");
  });

  test("unseal-share progression: submitted increments, complete flips at threshold; re-seal then batch unseal", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register + initialize + claim (barrier ends UNSEALED).
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "unseal-a",
          "unseal-b",
          "unseal-c",
          "unseal-d",
          "unseal-e",
        ],
        threshold: 3,
        total: 5,
        secret: "unseal-progression-secret",
      },
    );

    // Seal via quorum so the unseal-share progression can be exercised.
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      shareValues[0],
      shareValues[1],
      shareValues[2],
    ]);

    // First unseal cycle: single shares.
    for (let i = 0; i < 3; i++) {
      const r = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        {
          data: { share: shareValues[i] },
          headers: { "Content-Type": "application/json" },
        },
      );
      expect(r.status()).toBe(200);
      const p = await r.json() as Record<string, unknown>;
      expect(p["submitted"]).toBe(i + 1);
      expect(p["required"]).toBe(3);
      if (i < 2) {
        expect(p["complete"]).toBe(false);
      } else {
        expect(p["complete"]).toBe(true);
      }
    }

    // Wait for full health.
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    // Second cycle: re-seal via quorum, then batch unseal.
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      shareValues[0],
      shareValues[1],
      shareValues[2],
    ]);

    // Confirm sealed.
    const sealedStatus = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const sealed = await sealedStatus.json() as Record<string, unknown>;
    expect(sealed["sealed"]).toBe(true);

    const batchResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-shares`,
      {
        data: { shares: [shareValues[0], shareValues[1], shareValues[2]] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(batchResp.status()).toBe(204);

    // Daemon must be unsealed.
    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const finalStatus = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const final = await finalStatus.json() as Record<string, unknown>;
    expect(final["sealed"]).toBe(false);
    expect(final["strategy"]).toBe("shamir");
  });

  test("batch unseal via unseal-shares submits threshold shares at once -> 204 + unsealed", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register + initialize + claim (barrier ends UNSEALED).
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "batch-a",
          "batch-b",
          "batch-c",
          "batch-d",
          "batch-e",
        ],
        threshold: 3,
        total: 5,
        secret: "batch-unseal-secret",
      },
    );

    // Seal via quorum.
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      shareValues[0],
      shareValues[1],
      shareValues[2],
    ]);

    // Batch unseal with threshold shares.
    const batchResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-shares`,
      {
        data: { shares: [shareValues[0], shareValues[1], shareValues[2]] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(batchResp.status()).toBe(204);

    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const statusResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const status = await statusResp.json() as Record<string, unknown>;
    expect(status["sealed"]).toBe(false);
    expect(status["strategy"]).toBe("shamir");
  });
});
