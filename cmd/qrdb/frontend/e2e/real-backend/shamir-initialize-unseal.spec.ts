// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * shamir-initialize-unseal.spec.ts
 *
 * End-to-end M-of-N (3-of-5) seal and unseal through the holder quorum.
 *
 * Initialization splits the unseal key to the enrolled holders and leaves the
 * barrier unsealed. Sealing and unsealing a Shamir barrier are both quorum
 * rounds: each holder opens its envelope and submits its share with a factor
 * proof (qrdb barrier quorum submit), and the submission that reaches the
 * threshold performs the operation.
 *
 * Covered flows:
 *   - initialization is share-free and leaves the barrier unsealed;
 *   - a seal quorum seals it; an unseal quorum unseals it;
 *   - below the threshold a submission is counted and changes nothing;
 *   - an unseal submission while unsealed is refused;
 *   - the status reports the Shamir scheme throughout.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import {
  bootstrapShamirBarrier,
  mustSubmit,
  quorum,
  submitShare,
  waitForSealed,
} from "./fixtures/officer-bootstrap.ts";

test.describe("Shamir initialize + quorum seal and unseal (3-of-5)", () => {
  test("seal quorum then unseal quorum; status tracks both", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    const status = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/status`);
    expect(await status.json()).toMatchObject({ sealed: false, strategy: "shamir", shamir_threshold: 3, shamir_total: 5 });

    expect(quorum(shamirDaemon, holders, 3, "seal").complete).toBe(true);
    await waitForSealed(request, shamirDaemon.adminUrl, true);

    // Any three holders unseal it, not necessarily those that sealed it.
    const unsealed = quorum(shamirDaemon, holders.slice(2), 3, "unseal");
    expect(unsealed.complete).toBe(true);
    await waitForSealed(request, shamirDaemon.adminUrl, false);
  });

  test("below the threshold a submission is counted and the barrier stays sealed", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);
    quorum(shamirDaemon, holders, 3, "seal");
    await waitForSealed(request, shamirDaemon.adminUrl, true);

    expect(mustSubmit(shamirDaemon, holders[0]!, "unseal")).toMatchObject({ submitted: 1, required: 3, complete: false });
    expect(mustSubmit(shamirDaemon, holders[1]!, "unseal")).toMatchObject({ submitted: 2, complete: false });
    const status = await request.get(`${shamirDaemon.adminUrl}/v1/barrier/status`);
    expect(((await status.json()) as { sealed: boolean }).sealed).toBe(true);
  });

  test("an unseal submission to an unsealed barrier is refused", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    const res = submitShare(shamirDaemon, holders[0]!, "unseal");
    expect(res.status).not.toBe(0);
    expect(res.stderr + res.stdout).toMatch(/unsealed/i);
  });
});
