// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * shamir-rekey.spec.ts
 *
 * Rekey and revocation of a Shamir (3-of-5) barrier's holders.
 *
 * A rekey is opened by an administrator with the SO PIN
 * (POST /v1/barrier/rekey/start) and completed by a fresh quorum of the
 * current holders submitting to the `rekey` round. It redistributes a new
 * unseal key: every holder claims a new envelope, and the old envelopes can
 * no longer complete a quorum. Revocation (POST /v1/barrier/officers/revoke)
 * is a rekey that leaves one holder out of the new share set.
 */

import { copyFileSync } from "fs";
import { test, expect } from "./fixtures/sealed-daemon.ts";
import {
  bootstrapShamirBarrier,
  claimEnvelope,
  mustClaim,
  mustSubmit,
  quorum,
  submitShare,
  type Holder,
} from "./fixtures/officer-bootstrap.ts";

const SO_PIN = "so-pin-e2e";

test.describe("Shamir rekey and revocation", () => {
  test("opening a rekey answers the round, share-free", async ({ request, shamirDaemon }) => {
    await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);

    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/rekey/start`, {
      data: { domain: "unseal", threshold: 2, so_pin: SO_PIN },
    });
    expect(resp.status()).toBe(200);
    const round = (await resp.json()) as Record<string, unknown>;
    expect(round).toMatchObject({ domain: "unseal", op: "rekey", required: 3, submitted: 0, new_threshold: 2 });
    for (const field of ["share", "shares", "envelope"]) expect(round[field]).toBeUndefined();
  });

  test("opening a rekey without the SO PIN is refused", async ({ request, shamirDaemon }) => {
    await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/rekey/start`, {
      data: { domain: "unseal", threshold: 2, so_pin: "wrong" },
    });
    expect(resp.status()).toBe(401);
  });

  test("a completed rekey issues new envelopes and retires the old ones", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const oldEnvelopes = holders.map((h) => {
      const copy = `${h.envelopePath}.old`;
      copyFileSync(h.envelopePath, copy);
      return { ...h, envelopePath: copy } as Holder;
    });

    const start = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/rekey/start`, {
      data: { domain: "unseal", threshold: 3, so_pin: SO_PIN },
    });
    expect(start.status()).toBe(200);
    const done = quorum(shamirDaemon, holders, 3, "rekey");
    expect(done).toMatchObject({ complete: true, threshold: 3, total_shares: 5, share_count: 5 });

    // Every holder has a new envelope pending and claims it.
    const list = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/officers`);
    const officers = ((await list.json()) as { officers: { share_pending: boolean }[] }).officers;
    expect(officers.every((o) => o.share_pending)).toBe(true);
    for (const h of holders) mustClaim(shamirDaemon, h);

    // The new envelopes seal it...
    expect(quorum(shamirDaemon, holders, 3, "seal").complete).toBe(true);
    // ...and the old ones cannot complete an unseal: their shares rebuild a
    // key that no longer opens the barrier.
    mustSubmit(shamirDaemon, oldEnvelopes[0]!, "unseal");
    mustSubmit(shamirDaemon, oldEnvelopes[1]!, "unseal");
    const third = submitShare(shamirDaemon, oldEnvelopes[2]!, "unseal");
    expect(third.status).not.toBe(0);
  });

  test("revoking a holder leaves it out of the new share set", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const revoked = holders[4]!;

    const start = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/officers/revoke`, {
      data: { domain: "unseal", username: revoked.username, so_pin: SO_PIN },
    });
    expect(start.status()).toBe(200);
    expect(((await start.json()) as { remove: string[] }).remove).toEqual([revoked.username]);

    // The revoked holder cannot take part in its own revocation.
    expect(submitShare(shamirDaemon, revoked, "rekey").status).not.toBe(0);

    const done = quorum(shamirDaemon, holders, 3, "rekey");
    expect(done).toMatchObject({ complete: true, total_shares: 4, share_count: 4 });

    const list = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/officers`);
    const names = ((await list.json()) as { officers: { username: string }[] }).officers.map((o) => o.username);
    expect(names).not.toContain(revoked.username);
    expect(claimEnvelope(shamirDaemon, revoked).status).not.toBe(0);
  });
});
