// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * root-token.spec.ts
 *
 * Root token generation via POST /v1/barrier/root-token.
 *
 * A root token is a one-time privileged token generated from Shamir shares.
 * Providing >= threshold valid claimed share values succeeds; fewer than
 * threshold fails (server returns a quorum error).
 *
 * Covered flows:
 *   - POST /v1/barrier/root-token with >= threshold claimed share values
 *     → 200 {token, created_at}
 *   - token is a non-empty string; created_at is an RFC3339 timestamp
 *   - POST /v1/barrier/root-token with fewer than threshold shares
 *     → error status (not 200)
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";
import { bootstrapShamirBarrier } from "./fixtures/officer-bootstrap.ts";

test.describe("Root token generation", () => {
  test("root-token with >= threshold claimed shares returns 200 with token and created_at", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register 5 officers, initialize (3-of-5), claim all shares.
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rtoken-a",
          "rtoken-b",
          "rtoken-c",
          "rtoken-d",
          "rtoken-e",
        ],
        threshold: 3,
        total: 5,
        secret: "root-token-secret",
      },
    );

    // Unseal the barrier so root-token generation is available.
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[0] }, headers: { "Content-Type": "application/json" } },
    );
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[1] }, headers: { "Content-Type": "application/json" } },
    );
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[2] }, headers: { "Content-Type": "application/json" } },
    );

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Generate root token with >= threshold share values.
    const tokenResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/root-token`,
      {
        data: { shares: [shareValues[0], shareValues[1], shareValues[2]] },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(tokenResp.status()).toBe(200);
    const tokenBody = await tokenResp.json() as Record<string, unknown>;

    // token is a non-empty string.
    expect(typeof tokenBody["token"]).toBe("string");
    expect((tokenBody["token"] as string).length).toBeGreaterThan(0);

    // created_at is a non-empty RFC3339 timestamp string.
    expect(typeof tokenBody["created_at"]).toBe("string");
    expect((tokenBody["created_at"] as string).length).toBeGreaterThan(0);

    // Verify the timestamp parses as a valid date.
    const ts = new Date(tokenBody["created_at"] as string);
    expect(ts.getTime()).not.toBeNaN();
  });

  test("root-token with fewer than threshold shares returns an error (quorum not met)", async ({
    request,
    playwright,
    shamirDaemon,
  }) => {
    // Bootstrap: register 5 officers, initialize (3-of-5), claim all shares.
    const { shareValues } = await bootstrapShamirBarrier(
      request,
      playwright.request,
      shamirDaemon.adminUrl,
      {
        officerUsernames: [
          "rtoken2-a",
          "rtoken2-b",
          "rtoken2-c",
          "rtoken2-d",
          "rtoken2-e",
        ],
        threshold: 3,
        total: 5,
        secret: "root-token-quorum-secret",
      },
    );

    // Unseal first.
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[0] }, headers: { "Content-Type": "application/json" } },
    );
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[1] }, headers: { "Content-Type": "application/json" } },
    );
    await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/shamir/unseal-share`,
      { data: { share: shareValues[2] }, headers: { "Content-Type": "application/json" } },
    );

    await waitForHealthExported(shamirDaemon.adminUrl, 30_000);

    // Only 2 shares — below threshold of 3.
    const tokenResp = await request.post(
      `${shamirDaemon.adminUrl}/v1/barrier/root-token`,
      {
        data: { shares: [shareValues[0], shareValues[1]] },
        headers: { "Content-Type": "application/json" },
      },
    );

    // Must not succeed.
    expect(tokenResp.status()).not.toBe(200);
    expect(tokenResp.status()).toBeGreaterThanOrEqual(400);

    const errBody = await tokenResp.json() as Record<string, unknown>;
    expect(typeof errBody["error"]).toBe("string");
  });
});
