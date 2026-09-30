// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * recovery-keys.spec.ts
 *
 * Recovery holders and the recovery key.
 *
 * The recovery key is a second, independent domain of a Shamir barrier, with
 * its own holders. They are enrolled on /v1/barrier/recovery/officers before
 * the key is generated (POST /v1/barrier/recovery/generate, SO PIN); the
 * generation is share-free and seals one envelope to each recovery holder,
 * who claims it with the qrdb CLI. A quorum of recovery holders submitting to
 * the `recover` round unseals a sealed barrier.
 *
 * Routes under test:
 *   POST   /v1/barrier/recovery/officers   — enrol a recovery holder
 *   POST   /v1/barrier/recovery/generate   — generate the recovery key (share-free)
 *   GET    /v1/barrier/recovery/status     — {has_keys}
 *   DELETE /v1/barrier/recovery            — remove the recovery key
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import {
  bootstrapShamirBarrier,
  enrolHolders,
  mustClaim,
  newHolder,
  quorum,
  waitForSealed,
} from "./fixtures/officer-bootstrap.ts";

const SO_PIN = "so-pin-e2e";
const RECOVERY = { domain: "recovery" as const };

test.describe("Recovery holders and the recovery key", () => {
  test("generating with no recovery holders enrolled is refused", async ({ request, shamirDaemon }) => {
    await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`, {
      data: { threshold: 2, so_pin: SO_PIN },
    });
    expect(resp.status()).toBeGreaterThanOrEqual(400);
  });

  test("recovery enrolment returns the issued certificate, never a share", async ({ request, shamirDaemon }) => {
    await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const holder = newHolder("rec-alice");
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/recovery/officers`, {
      data: { username: holder.username, role: holder.role, csr_pem: holder.csrPem },
    });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as Record<string, unknown>;
    expect(String(body["cert_pem"])).toContain("BEGIN CERTIFICATE");
    for (const field of ["share", "shares", "share_index", "envelope"]) expect(body[field]).toBeUndefined();
  });

  test("generation is share-free; holders claim; a recover quorum unseals; delete clears the key", async ({
    request,
    shamirDaemon,
  }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const recovery = ["rec-a", "rec-b", "rec-c"].map((n) => newHolder(n, "custodian"));
    await enrolHolders(request, shamirDaemon.adminUrl, recovery, RECOVERY);

    const gen = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`, {
      data: { threshold: 2, so_pin: SO_PIN },
    });
    expect(gen.status()).toBe(200);
    expect(await gen.json()).toEqual({ threshold: 2, total: 3, share_count: 3 });

    const status = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/recovery/status`);
    expect(((await status.json()) as { has_keys: boolean }).has_keys).toBe(true);
    for (const h of recovery) mustClaim(shamirDaemon, h, RECOVERY);

    // Seal with the unseal holders, then recover with two recovery holders.
    quorum(shamirDaemon, holders, 3, "seal");
    await waitForSealed(request, shamirDaemon.adminUrl, true);
    expect(quorum(shamirDaemon, recovery, 2, "recover", RECOVERY).complete).toBe(true);
    await waitForSealed(request, shamirDaemon.adminUrl, false);

    const del = await request.delete(`${shamirDaemon.adminUrl}/v1/barrier/recovery`);
    expect(del.status()).toBe(204);
    const after = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/recovery/status`);
    expect(((await after.json()) as { has_keys: boolean }).has_keys).toBe(false);
  });

  test("a recovery claim without a client certificate is 401", async ({ request, shamirDaemon }) => {
    await bootstrapShamirBarrier(request, shamirDaemon, 5, 3, SO_PIN);
    const resp = await request.post(`${shamirDaemon.adminUrl}/v1/barrier/recovery/officers/rec-a/claim/begin`);
    expect(resp.status()).toBe(401);
  });
});
