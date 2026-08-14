// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * recovery-keys.spec.ts
 *
 * Recovery officer enrollment and recovery key lifecycle.
 *
 * Recovery keys provide an alternative unseal path for disaster recovery.
 * They follow the same officer-claim model as the system Shamir barrier:
 * recovery officers must be registered BEFORE calling recovery/generate;
 * the generate response is share-free; each recovery officer claims their
 * share via the mTLS claim handshake on the recovery routes.
 *
 * Routes under test:
 *   POST /v1/barrier/recovery/officers                           — register recovery SO
 *   POST /v1/barrier/recovery/generate                          — generate recovery keys (share-free)
 *   POST /v1/barrier/recovery/officers/{username}/claim/begin   — recovery claim begin
 *   POST /v1/barrier/recovery/officers/{username}/claim/complete — recovery claim complete
 *   GET  /v1/barrier/recovery/status                            — {has_keys: bool}
 *   DELETE /v1/barrier/recovery                                 — remove recovery key metadata
 *
 * Covered flows:
 *   - POST /v1/barrier/recovery/officers returns {cert_pem, share_index}
 *   - POST /v1/barrier/recovery/generate without registered officers → error
 *   - POST /v1/barrier/recovery/generate with correct officer count
 *     → {threshold, total, share_count} — NO "keys" field (INV-1)
 *   - recovery officer claims their share via mTLS claim handshake
 *   - GET /v1/barrier/recovery/status → {has_keys: true} after generate
 *   - DELETE /v1/barrier/recovery removes recovery key metadata →
 *     GET /v1/barrier/recovery/status → {has_keys: false}
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import {
  generateOfficerKeyAndCSR,
  bootstrapShamirBarrier,
  claimRecoveryShare,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Recovery keys lifecycle", () => {
  // ---------------------------------------------------------------------------
  // recovery/generate without officers registered → error
  // ---------------------------------------------------------------------------

  test("recovery/generate with zero recovery officers registered returns an error", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap the system barrier so the daemon is unsealed.
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["rgen-a", "rgen-b", "rgen-c", "rgen-d", "rgen-e"],
        threshold: 3,
        total: 5,
        secret: "recovery-no-officers-secret",
      },
    );

    for (let i = 0; i < 3; i++) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: shareValues[i] }, headers: { "Content-Type": "application/json" } },
      );
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // No recovery officers registered — generate must fail.
    const genResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`,
      {
        data: { threshold: 2, total: 3 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(genResp.status()).not.toBe(200);
    expect(genResp.status()).toBeGreaterThanOrEqual(400);
  });

  // ---------------------------------------------------------------------------
  // Recovery officer registration
  // ---------------------------------------------------------------------------

  test("POST /v1/barrier/recovery/officers returns {cert_pem, share_index}", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: ["rreg-a", "rreg-b", "rreg-c", "rreg-d", "rreg-e"],
        threshold: 3,
        total: 5,
        secret: "recovery-reg-secret",
      },
    );

    for (let i = 0; i < 3; i++) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: shareValues[i] }, headers: { "Content-Type": "application/json" } },
      );
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Register a recovery officer.
    const key = generateOfficerKeyAndCSR("rec-alice");
    const regResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/officers`,
      {
        data: {
          username: key.username,
          csr_pem: key.csrPem,
        },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(regResp.status()).toBe(200);
    const regBody = await regResp.json() as Record<string, unknown>;

    expect(typeof regBody["cert_pem"]).toBe("string");
    expect((regBody["cert_pem"] as string)).toContain("BEGIN CERTIFICATE");
    expect(typeof regBody["share_index"]).toBe("number");
    expect(regBody["share_index"]).toBe(1);
  });

  // ---------------------------------------------------------------------------
  // INV-1: recovery/generate is share-free
  // ---------------------------------------------------------------------------

  test("recovery/generate returns {threshold, total, share_count} — NO 'keys' or 'shares' field (INV-1)", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rgen2-a",
          "rgen2-b",
          "rgen2-c",
          "rgen2-d",
          "rgen2-e",
        ],
        threshold: 3,
        total: 5,
        secret: "recovery-gen-inv1-secret",
      },
    );

    for (let i = 0; i < 3; i++) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: shareValues[i] }, headers: { "Content-Type": "application/json" } },
      );
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Register 3 recovery officers.
    const recKeys = [
      generateOfficerKeyAndCSR("rinv1-alice"),
      generateOfficerKeyAndCSR("rinv1-bob"),
      generateOfficerKeyAndCSR("rinv1-carol"),
    ];

    for (const k of recKeys) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/recovery/officers`,
        {
          data: { username: k.username, csr_pem: k.csrPem },
          headers: { "Content-Type": "application/json" },
        },
      );
    }

    // Generate recovery keys (2-of-3).
    const genResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`,
      {
        data: { threshold: 2, total: 3 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(genResp.status()).toBe(200);
    const gen = await genResp.json() as Record<string, unknown>;

    // Required fields.
    expect(gen["threshold"]).toBe(2);
    expect(gen["total"]).toBe(3);
    expect(typeof gen["share_count"]).toBe("number");
    expect(gen["share_count"]).toBe(3);

    // INV-1: no share material in the response.
    expect(gen["keys"]).toBeUndefined();
    expect(gen["shares"]).toBeUndefined();
    expect(gen["key_shares"]).toBeUndefined();
  });

  // ---------------------------------------------------------------------------
  // Full recovery officer claim + status flow
  // ---------------------------------------------------------------------------

  test("recovery officer claims their share; recovery/status has_keys:true after generate, false after delete", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rfull-a",
          "rfull-b",
          "rfull-c",
          "rfull-d",
          "rfull-e",
        ],
        threshold: 3,
        total: 5,
        secret: "recovery-full-flow-secret",
      },
    );

    for (let i = 0; i < 3; i++) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: shareValues[i] }, headers: { "Content-Type": "application/json" } },
      );
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Register 3 recovery officers.
    const recKeys = [
      generateOfficerKeyAndCSR("rfull-rec-alice"),
      generateOfficerKeyAndCSR("rfull-rec-bob"),
      generateOfficerKeyAndCSR("rfull-rec-carol"),
    ];

    const recOfficers = [];
    for (const k of recKeys) {
      const regResp = await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/recovery/officers`,
        {
          data: { username: k.username, csr_pem: k.csrPem },
          headers: { "Content-Type": "application/json" },
        },
      );
      expect(regResp.status()).toBe(200);
      const regBody = await regResp.json() as Record<string, unknown>;
      recOfficers.push({
        username: k.username,
        privateKeyPem: k.privateKeyPem,
        publicKeyPem: k.publicKeyPem,
        certPem: regBody["cert_pem"] as string,
        shareIndex: regBody["share_index"] as number,
      });
    }

    // Generate recovery keys (2-of-3).
    const genResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`,
      {
        data: { threshold: 2, total: 3 },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(genResp.status()).toBe(200);

    // Status must show recovery keys configured.
    const statusResp = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/status`,
    );
    expect(statusResp.status()).toBe(200);
    const status = await statusResp.json() as Record<string, unknown>;
    expect(status["has_keys"]).toBe(true);

    // Each recovery officer claims their share.
    for (const officer of recOfficers) {
      const claimed = await claimRecoveryShare(
        playwright.request,
        shamirDaemon.adminUrl,
        officer,
      );
      // share_index matches what was assigned at registration.
      expect(claimed.shareIndex).toBe(officer.shareIndex);
      // shareValue is a non-empty string.
      expect(typeof claimed.shareValue).toBe("string");
      expect(claimed.shareValue.length).toBeGreaterThan(0);
    }

    // Delete recovery keys.
    const deleteResp = await request.delete(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery`,
    );
    expect(deleteResp.status()).toBe(204);

    // Status must now show no recovery keys.
    const statusAfterDelete = await request.get(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/status`,
    );
    expect(statusAfterDelete.status()).toBe(200);
    const afterDelete = await statusAfterDelete.json() as Record<string, unknown>;
    expect(afterDelete["has_keys"]).toBe(false);
  });

  // ---------------------------------------------------------------------------
  // Recovery claim without client cert → 401
  // ---------------------------------------------------------------------------

  test("recovery claim/begin without mTLS client certificate returns 401", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rnocert-a",
          "rnocert-b",
          "rnocert-c",
          "rnocert-d",
          "rnocert-e",
        ],
        threshold: 3,
        total: 5,
        secret: "recovery-nocert-secret",
      },
    );

    for (let i = 0; i < 3; i++) {
      await request.post(
        `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
        { data: { share: shareValues[i] }, headers: { "Content-Type": "application/json" } },
      );
    }

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Register one recovery officer.
    const key = generateOfficerKeyAndCSR("rnocert-alice");
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/officers`,
      {
        data: { username: key.username, csr_pem: key.csrPem },
        headers: { "Content-Type": "application/json" },
      },
    );

    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/generate`,
      {
        data: { threshold: 1, total: 1 },
        headers: { "Content-Type": "application/json" },
      },
    );

    // Attempt claim/begin without a client certificate.
    const resp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/recovery/officers/rnocert-alice/claim/begin`,
      { headers: { "Content-Type": "application/json" } },
    );

    expect(resp.status()).toBe(401);
  });
});
