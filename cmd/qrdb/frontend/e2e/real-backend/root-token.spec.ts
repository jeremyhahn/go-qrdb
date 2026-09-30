// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * root-token.spec.ts
 *
 * A root token is minted by a quorum of share holders: each submits its share
 * to the `root-token` round (qrdb barrier quorum submit --op root-token), and
 * the submission that reaches the threshold returns the token. There is no
 * route that takes shares directly.
 *
 * Covered flows:
 *   - below the threshold a submission is counted and returns no token;
 *   - the threshold-reaching submission returns a non-empty token;
 *   - a holder cannot count twice toward the same round.
 */

import { test, expect } from "./fixtures/sealed-daemon.ts";
import { bootstrapShamirBarrier, mustSubmit, submitShare } from "./fixtures/officer-bootstrap.ts";

test.describe("Root token generation", () => {
  test("the threshold-reaching submission returns the root token", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    const first = mustSubmit(shamirDaemon, holders[0]!, "root-token");
    expect(first).toMatchObject({ op: "root-token", required: 3, submitted: 1, complete: false });
    expect(first.root_token ?? "").toBe("");

    mustSubmit(shamirDaemon, holders[1]!, "root-token");
    const last = mustSubmit(shamirDaemon, holders[2]!, "root-token");
    expect(last.complete).toBe(true);
    expect(typeof last.root_token).toBe("string");
    expect(last.root_token!.length).toBeGreaterThan(0);
  });

  test("a holder counts once: a second submission to the round is refused", async ({ request, shamirDaemon }) => {
    const { holders } = await bootstrapShamirBarrier(request, shamirDaemon, 5, 3);

    mustSubmit(shamirDaemon, holders[0]!, "root-token");
    const again = submitShare(shamirDaemon, holders[0]!, "root-token");
    expect(again.status).not.toBe(0);
    expect(again.stderr + again.stdout).toMatch(/already/i);
  });
});
