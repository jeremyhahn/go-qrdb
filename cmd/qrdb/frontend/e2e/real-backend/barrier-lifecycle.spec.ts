// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-lifecycle.spec.ts
 *
 * End-to-end software-barrier (passphrase) seal/unseal lifecycle.
 *
 * The daemon is started with seal.enabled=true and an uninitialized barrier.
 * It enters a sealed-window mode: the /v1/barrier/* routes are available and
 * /health returns 503 {"error":"barrier sealed","code":"SEALED"} until the
 * operator calls POST /v1/barrier/initialize. After initialization the daemon
 * resumes full bootstrap (system tenant, roles, admin user) and /health
 * returns 200.
 *
 * Covered flows:
 *   - /health returns 503 while sealed, 200 after init
 *   - GET /v1/barrier/status reflects initialization and seal state
 *   - POST /v1/barrier/initialize -> 204
 *   - POST /v1/barrier/seal -> 204
 *   - POST /v1/barrier/unseal with wrong secret -> 403 (bad credential, not 500)
 *   - POST /v1/barrier/unseal with correct secret -> 204
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { waitForHealthExported } from "./fixtures/daemon-helpers.ts";

test.describe("Barrier lifecycle (passphrase strategy)", () => {
  test("GET /v1/barrier/status returns sealed:true before initialization", async ({
    request,
    sealedDaemon,
  }) => {
    const resp = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(resp.status()).toBe(200);
    const body = await resp.json() as Record<string, unknown>;
    expect(body["sealed"]).toBe(true);
    // Pre-init: strategy is empty string, no initialized_at field
    expect(body["strategy"]).toBe("");
    expect(body["initialized_at"]).toBeUndefined();
  });

  test("/health returns 503 while sealed, 200 after init and bootstrap", async ({
    request,
    sealedDaemon,
  }) => {
    // 1) Before init: /health must be 503.
    const sealedHealth = await request.get(
      `${sealedDaemon.adminUrl}/health`,
    );
    expect(sealedHealth.status()).toBe(503);
    const sealedBody = await sealedHealth.json() as Record<string, unknown>;
    expect(sealedBody["error"]).toBe("barrier sealed");
    expect(sealedBody["code"]).toBe("SEALED");

    // 2) Initialize the barrier.
    const initResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret: "lifecycle-e2e-secret" },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(204);

    // 3) After init the daemon resumes bootstrap. /health eventually returns 200.
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    const readyHealth = await request.get(`${sealedDaemon.adminUrl}/health`);
    expect(readyHealth.status()).toBe(200);
    const readyBody = await readyHealth.json() as Record<string, unknown>;
    expect(readyBody["status"]).toBe("ok");
  });

  test("full seal/unseal cycle: init -> seal -> wrong-secret 403 -> correct-secret 204 -> unsealed", async ({
    request,
    sealedDaemon,
  }) => {
    const secret = "cycle-e2e-secret";

    // 1) Initialize.
    const initResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/initialize`,
      {
        data: { secret },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(initResp.status()).toBe(204);
    await waitForHealthExported(sealedDaemon.adminUrl, 30_000);

    // 2) Status: unsealed, strategy=passphrase, has initialized_at.
    const statusAfterInit = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(statusAfterInit.status()).toBe(200);
    const afterInit = await statusAfterInit.json() as Record<string, unknown>;
    expect(afterInit["sealed"]).toBe(false);
    expect(afterInit["strategy"]).toBe("passphrase");
    expect(typeof afterInit["initialized_at"]).toBe("string");

    // 3) Seal.
    const sealResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/seal`,
    );
    expect(sealResp.status()).toBe(204);

    // 4) Status: sealed again, initialized_at still present.
    const statusAfterSeal = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(statusAfterSeal.status()).toBe(200);
    const afterSeal = await statusAfterSeal.json() as Record<string, unknown>;
    expect(afterSeal["sealed"]).toBe(true);
    expect(afterSeal["strategy"]).toBe("passphrase");

    // 5) Unseal with wrong secret -> 403 Forbidden. A wrong passphrase is
    //    client-supplied bad credential material, not a server fault, so it
    //    must NOT surface as a 500. (Regression guard for the leaked-500 bug.)
    const wrongResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/unseal`,
      {
        data: { secret: "wrong-secret" },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(wrongResp.status()).toBe(403);
    expect(wrongResp.status()).not.toBe(500);
    const wrongBody = await wrongResp.json() as Record<string, unknown>;
    expect(typeof wrongBody["error"]).toBe("string");

    // 6) Unseal with correct secret -> 204.
    const unsealResp = await request.post(
      `${sealedDaemon.adminUrl}/v1/barrier/unseal`,
      {
        data: { secret },
        headers: { "Content-Type": "application/json" },
      },
    );
    expect(unsealResp.status()).toBe(204);

    // 7) Status: unsealed.
    const statusAfterUnseal = await request.get(
      `${sealedDaemon.adminUrl}/v1/barrier/status`,
    );
    expect(statusAfterUnseal.status()).toBe(200);
    const afterUnseal = await statusAfterUnseal.json() as Record<string, unknown>;
    expect(afterUnseal["sealed"]).toBe(false);
    expect(afterUnseal["strategy"]).toBe("passphrase");
  });
});
