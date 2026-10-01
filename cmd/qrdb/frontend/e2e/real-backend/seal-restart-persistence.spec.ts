// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * seal-restart-persistence.spec.ts
 *
 * Validates that barrier-encrypted data persists across a daemon restart.
 *
 * Flow:
 *   1. Initialize passphrase barrier (daemon enters sealed-window, then full boot)
 *   2. Write a KV entry while unsealed
 *   3. Seal the barrier
 *   4. Restart the daemon (daemon comes back SEALED — serves /v1/barrier/status 200,
 *      /health 503)
 *   5. Unseal with the same secret
 *   6. Read the KV entry — value must match (proves data persisted and the
 *      barrier-encrypted storage was correctly re-opened)
 *
 * The restart() helper in SealedDaemonInfo uses waitForSealedWindow so it
 * resolves as soon as the sealed-window listener is ready, not /health 200.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { b64, waitForHealthExported, waitForSealedWindow } from "./fixtures/daemon-helpers.ts";

function kvKey(label: string): string {
  return `seal-persist-${label}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

test.describe("Seal + restart persistence", () => {
  test("KV data written before seal is readable after restart + unseal", async ({
    request,
    sealedDaemon,
  }) => {
    const secret = "restart-persist-secret";
    const key = kvKey("main");
    const value = "barrier-encrypted-value";

    // 1) Initialize.
    const initResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: b64(secret) },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(204);
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    // 2) Write KV entry.
    const putResp = await request.put(
      `${sealedDaemon.adminUrl}/kv/${encodeURIComponent(key)}`,
      {
        data: value,
        headers: { "Content-Type": "text/plain" },
      },
    );
    expect(putResp.ok()).toBe(true);

    // Confirm write.
    const readBeforeSeal = await request.get(
      `${sealedDaemon.adminUrl}/kv/${encodeURIComponent(key)}`,
    );
    expect(readBeforeSeal.ok()).toBe(true);
    expect(await readBeforeSeal.text()).toBe(value);

    // 3) Seal.
    const sealResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/seal`,
    );
    expect(sealResp.status()).toBe(204);

    // 4) Restart — daemon comes back in sealed-window mode.
    // sealedDaemon.restart() internally uses waitForSealedWindow so it
    // resolves when /v1/barrier/status returns {sealed:true}.
    await sealedDaemon.restart();

    // Confirm sealed after restart.
    const sealedStatus = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(sealedStatus.status()).toBe(200);
    const sealedBody = await sealedStatus.json() as Record<string, unknown>;
    expect(sealedBody["sealed"]).toBe(true);

    // 5) Unseal.
    const unsealResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/unseal`,
      {
        data: { secret: b64(secret) },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(unsealResp.status()).toBe(204);
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    // 6) Read KV — value must be the same as before restart.
    const readAfterUnseal = await request.get(
      `${sealedDaemon.adminUrl}/kv/${encodeURIComponent(key)}`,
    );
    expect(readAfterUnseal.ok()).toBe(true);
    expect(await readAfterUnseal.text()).toBe(value);
  });

  test("daemon comes back sealed after restart: /health 503, barrier status sealed:true", async ({
    request,
    sealedDaemon,
  }) => {
    const secret = "restart-sealed-verify-secret";

    // Initialize and wait for full health.
    const initResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: b64(secret) },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(204);
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    // Seal.
    await request.post(`${sealedDaemon.adminUrl}/v1/barrier/seal`);

    // Restart.
    await sealedDaemon.restart();

    // /health must be 503 while sealed.
    const health = await request.get(`${sealedDaemon.adminUrl}/health`);
    expect(health.status()).toBe(503);
    const healthBody = await health.json() as Record<string, unknown>;
    expect(healthBody["error"]).toBe("barrier sealed");

    // /v1/barrier/status must report sealed:true with the stored strategy.
    const statusResp = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(statusResp.status()).toBe(200);
    const status = await statusResp.json() as Record<string, unknown>;
    expect(status["sealed"]).toBe(true);
    // After restart the strategy field reflects the stored configuration.
    expect(typeof status["initialized_at"]).toBe("string");
  });
});
