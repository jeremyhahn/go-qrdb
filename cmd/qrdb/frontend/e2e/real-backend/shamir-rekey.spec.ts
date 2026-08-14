// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * shamir-rekey.spec.ts
 *
 * Shamir share rotation via POST /v1/barrier/rekey.
 *
 * Rekey generates a fresh random UNSEAL KEY, re-wraps the (unchanged) root
 * key under it, and splits the unseal key into a fresh M-of-N share set
 * (docs/seal/so-share-distribution-design.md §6.7). The root key itself is
 * preserved — no stored data is re-encrypted. Old unclaimed enclaves are
 * destroyed (INV-10), claim state resets, and officers re-claim new shares.
 *
 * NOTE on old shares: because officers' shares carry the unseal key rather
 * than the root key, persisting the re-wrapped blob CRYPTOGRAPHICALLY
 * REVOKES every previously distributed share — including shares already
 * claimed. A prior-round quorum reconstructs an unseal key that no longer
 * decrypts the wrapped root key (seal.ErrUnsealKeyMismatch → 403), and
 * prior-round seal-votes fail verification the same way
 * (seal.ErrSealVoteVerificationFailed → 403).
 *
 * Covered flows:
 *   - POST /v1/barrier/rekey returns {threshold, total_shares, share_count}
 *     with NO "shares" field (INV-1)
 *   - claim-status after rekey shows all officers as pending (claimed_at absent)
 *   - officers re-claim new, distinct shares after rekey
 *   - new share values seal (quorum) + unseal the barrier correctly
 *   - prior-round shares are revoked: seal-vote → 403, unseal quorum → 403
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  bootstrapShamirBarrier,
  claimShare,
  sealBarrierViaQuorum,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Shamir rekey (rotate shares, revoke prior round)", () => {
  test("rekey returns {threshold, total_shares, share_count} with NO shares field", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register 5 officers, initialize (3-of-5), claim all shares.
    // initialize auto-unseals; the daemon reaches full boot inside bootstrap.
    await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["rk-a", "rk-b", "rk-c", "rk-d", "rk-e"],
        threshold: 3,
        total: 5,
        secret: "rekey-init-secret",
      },
    );

    // Rekey (keep same 3-of-5 parameters). Barrier is unsealed, as required.
    const rekeyResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/rekey`,
      {
        data: { threshold: 3, total: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(rekeyResp.status()).toBe(200);
    const rekey = await rekeyResp.json() as Record<string, unknown>;

    // Required fields.
    expect(rekey["threshold"]).toBe(3);
    expect(rekey["total_shares"]).toBe(5);
    expect(typeof rekey["share_count"]).toBe("number");
    expect(rekey["share_count"]).toBe(5);

    // INV-1: no share bytes in rekey response.
    expect(rekey["shares"]).toBeUndefined();
    expect(rekey["key_shares"]).toBeUndefined();
    expect(rekey["keys"]).toBeUndefined();
  });

  test("claim-status resets to all-pending after rekey; officers re-claim new shares", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register + initialize + claim (barrier ends UNSEALED).
    const { officers, shareValues: oldShares } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rk2-alice",
          "rk2-bob",
          "rk2-carol",
          "rk2-dave",
          "rk2-eve",
        ],
        threshold: 3,
        total: 5,
        secret: "rekey-reset-secret",
      },
    );

    // Rekey.
    const rekeyResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/rekey`,
      {
        data: { threshold: 3, total: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(rekeyResp.status()).toBe(200);

    // Claim-status must show all officers as pending (no claimed_at).
    const statusResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/officers/claim-status`,
    );
    expect(statusResp.status()).toBe(200);
    const statusBody = await statusResp.json() as Record<string, unknown>;
    const officerStatuses = statusBody["officers"] as Record<string, unknown>[];

    for (const o of officerStatuses) {
      // After rekey, claimed_at is reset (absent or zero) and the new
      // share is pending claim.
      expect(o["claimed_at"]).toBeUndefined();
      expect(o["share_pending"]).toBe(true);
    }

    // Re-claim new shares for all officers (claim history was reset).
    const newShares: string[] = [];
    for (const officer of officers) {
      const claimed = await claimShare(
        playwright.request,
        shamirDaemon.adminUrl,
        officer,
      );
      newShares.push(claimed.shareValue);
    }

    // New shares must be distinct from old shares.
    for (let i = 0; i < 5; i++) {
      expect(newShares[i]).not.toBe(oldShares[i]);
    }

    // Seal via quorum using new shares.
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      newShares[0],
      newShares[1],
      newShares[2],
    ]);

    // Unseal with new shares works.
    for (let i = 0; i < 3; i++) {
      const r = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: newShares[i] }, headers: { "Content-Type": "application/json" } },
      );
      expect(r.status()).toBe(200);
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const unsealedStatus = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const unsealed = await unsealedStatus.json() as Record<string, unknown>;
    expect(unsealed["sealed"]).toBe(false);
    expect(unsealed["strategy"]).toBe("shamir");
  });

  test("rekey revokes prior-round shares: old seal-votes and old unseal quorums fail with 403", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap (barrier ends UNSEALED, all 5 old-round shares claimed).
    const { officers, shareValues: oldShares } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rk3-alice",
          "rk3-bob",
          "rk3-carol",
          "rk3-dave",
          "rk3-eve",
        ],
        threshold: 3,
        total: 5,
        secret: "rekey-old-invalid-secret",
      },
    );

    // Rekey while unsealed. Persisting the re-wrapped unseal-key blob
    // cryptographically revokes the entire prior share round (§6.7).
    const rekeyResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/rekey`,
      {
        data: { threshold: 3, total: 5 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(rekeyResp.status()).toBe(200);

    // A seal-vote quorum of prior-round shares must fail verification: votes
    // accumulate without verification until the threshold, then the old
    // quorum reconstructs an unseal key that no longer decrypts the wrapped
    // root key (ErrSealVoteVerificationFailed → 403). The failed round
    // resets the vote accumulator.
    for (let i = 0; i < 2; i++) {
      const v = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/seal-vote`,
        {
          data: { share: oldShares[i] },
          headers: { "Content-Type": "application/json" },
        },
      );
      expect(v.status()).toBe(200);
    }
    const oldVoteResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/seal-vote`,
      {
        data: { share: oldShares[2] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(oldVoteResp.status()).toBe(403);

    // Re-claim the NEW round's shares and seal via quorum with them.
    const newShares: string[] = [];
    for (const officer of officers) {
      const claimed = await claimShare(
        playwright.request,
        shamirDaemon.adminUrl,
        officer,
      );
      newShares.push(claimed.shareValue);
    }
    await sealBarrierViaQuorum(request, shamirDaemon.adminUrl, [
      newShares[0],
      newShares[1],
      newShares[2],
    ]);

    // Submit a threshold of OLD shares via unseal-share. The first two
    // accumulate (no verification before the threshold); the threshold-th
    // submission reconstructs the revoked unseal key and is rejected with
    // ErrUnsealKeyMismatch → 403. The barrier stays sealed.
    for (let i = 0; i < 2; i++) {
      const r = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: oldShares[i] }, headers: { "Content-Type": "application/json" } },
      );
      expect(r.status()).toBe(200);
    }
    const revokedResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: oldShares[2] }, headers: { "Content-Type": "application/json" } },
    );
    expect(revokedResp.status()).toBe(403);

    // The NEW round's shares unseal normally (the failed quorum reset the
    // accumulator, so fresh submissions start a clean round).
    for (let i = 0; i < 3; i++) {
      const r = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: newShares[i] }, headers: { "Content-Type": "application/json" } },
      );
      expect(r.status()).toBe(200);
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 60_000);

    const finalStatus = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/status`,
    );
    const finalBody = await finalStatus.json() as Record<string, unknown>;
    expect(finalBody["sealed"]).toBe(false);
    expect(finalBody["strategy"]).toBe("shamir");
  });
});
