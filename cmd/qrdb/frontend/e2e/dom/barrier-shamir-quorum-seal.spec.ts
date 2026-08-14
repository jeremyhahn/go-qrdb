// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * barrier-shamir-quorum-seal.spec.ts (real-backend DOM tier)
 *
 * FULL quorum-seal round trip through the real UI against a live qrdbd:
 *
 *   1. Boot a Shamir 2-of-3 daemon with --tls-client-auth=request so the
 *      officer mTLS claim handshake works (the Vite proxy carries no client
 *      cert and keeps working for regular UI traffic).
 *   2. Bootstrap out of band via REST: register 3 officers (CSR signing),
 *      shamir/initialize (share-free, INV-1), then each officer claims their
 *      share over a per-officer mTLS context. This mirrors the real
 *      deployment shape — officers obtain shares through the claim ceremony,
 *      never through the UI.
 *   3. Drive the Seal Manager UI: submit threshold seal votes through the
 *      quorum seal-vote panel. The first vote shows 1/2 progress; the second
 *      reaches quorum and seals the server ("Quorum reached — server
 *      sealed").
 *   4. Verify the backend really sealed (sealed-window listener reports
 *      sealed:true), reload the page, and confirm the app routes to the
 *      Shamir unseal screen.
 *   5. Unseal through the UI by submitting threshold shares, landing back in
 *      the app shell — closing the loop seal → unseal entirely through real
 *      crypto.
 *
 * This is the e2e the mocked tier and the API-only real-backend tier cannot
 * provide: the API tier exercises the REST quorum-seal contract and the DOM
 * shamir spec asserts the seal-vote panel renders, but only this spec drives
 * the actual vote submission and post-seal unseal through the DOM.
 */

import { test, expect } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";
import { bootstrapShamirBarrier } from "../real-backend/fixtures/officer-bootstrap.ts";
import { waitForSealedWindow } from "../real-backend/fixtures/daemon-helpers.ts";

// 2-of-3 keeps the UI interaction count low while still exercising a real
// multi-vote quorum (one vote is insufficient; two distinct shares seal).
const THRESHOLD = 2;
const TOTAL = 3;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({
    seal: "shamir",
    shamirThreshold: THRESHOLD,
    shamirTotal: TOTAL,
    tlsClientAuth: "request",
  });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("shamir: full quorum-seal + unseal round trip through the Seal Manager UI", async ({
  page,
  request,
  playwright,
}) => {
  // Multi-phase flow: daemon boot + officer bootstrap + two listener swaps
  // (seal and unseal) each take several seconds — the 30s default is too
  // tight for the full round trip.
  test.setTimeout(180_000);

  // -------------------------------------------------------------------------
  // Out-of-band bootstrap: register officers, initialize, claim all shares.
  // -------------------------------------------------------------------------
  const { shareValues } = await bootstrapShamirBarrier(
    request,
    playwright.request,
    daemon!.adminUrl,
    {
      officerUsernames: ["ui-vote-a", "ui-vote-b", "ui-vote-c"],
      threshold: THRESHOLD,
      total: TOTAL,
      secret: "dom-quorum-seal-secret",
    },
  );
  await waitForHealthy(daemon!);

  // -------------------------------------------------------------------------
  // The UI loads straight into the app shell (initialized + unsealed).
  // -------------------------------------------------------------------------
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // Shamir barriers present the quorum seal-vote panel, never a single-admin
  // Seal button.
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toHaveCount(0);
  await expect(page.getByTestId("seal-vote-input")).toBeVisible();
  await expect(page.getByTestId("submit-seal-vote-btn")).toBeDisabled();

  // -------------------------------------------------------------------------
  // Vote 1 of 2: accepted, quorum not yet reached.
  // -------------------------------------------------------------------------
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  await page.getByTestId("seal-vote-input").fill(shareValues[0]!);
  await expect(page.getByTestId("submit-seal-vote-btn")).toBeEnabled();
  await page.getByTestId("submit-seal-vote-btn").click();

  // Progress bar appears with 1 / 2; the input clears for the next officer.
  await expect(page.getByTestId("seal-vote-progress")).toContainText(
    `1 / ${THRESHOLD}`,
    { timeout: 15_000 },
  );
  await expect(page.getByTestId("seal-vote-input")).toHaveValue("");
  // Still unsealed after a sub-threshold vote count.
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // -------------------------------------------------------------------------
  // Vote 2 of 2: quorum reached — the server seals.
  // -------------------------------------------------------------------------
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  await page.getByTestId("seal-vote-input").fill(shareValues[1]!);
  await page.getByTestId("submit-seal-vote-btn").click();

  await expect(page.getByText("Quorum reached — server sealed")).toBeVisible({
    timeout: 20_000,
  });

  // The backend really sealed: the daemon swapped back to its sealed-window
  // listener, which reports sealed:true on /v1/barrier/status.
  await waitForSealedWindow(daemon!.adminUrl, 60_000);

  // -------------------------------------------------------------------------
  // Reload: a sealed server routes to the Shamir unseal screen.
  // -------------------------------------------------------------------------
  await page.goto(UI_BASE_URL);
  await expect(
    page.getByText("Provide Shamir key shares to unseal the encryption barrier."),
  ).toBeVisible({ timeout: 20_000 });

  // -------------------------------------------------------------------------
  // Unseal through the UI with threshold shares.
  // -------------------------------------------------------------------------
  // The standalone unseal screen labels the submit button "Submit Share" in
  // Shamir mode ("Unseal" is the passphrase-mode label).
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  await page.locator("#unseal-share").fill(shareValues[1]!);
  await page.getByRole("button", { name: "Submit Share" }).click();

  // Share 1 of 2 accepted; the form stays on the unseal screen.
  await expect(page.locator("#unseal-share")).toHaveValue("", { timeout: 15_000 });

  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  await page.locator("#unseal-share").fill(shareValues[2]!);
  await page.getByRole("button", { name: "Submit Share" }).click();

  // Quorum of unseal shares reached: the UI-driven unseal really unsealed the
  // backend — the daemon resumes full bootstrap and /health returns 200.
  // (Asserted Node-side because the in-page re-initialize races the daemon's
  // listener swap: /health transitions 503 → 404 → 200 over several seconds.)
  await waitForHealthy(daemon!, 60_000);

  // A fresh load now lands in the app shell with an unsealed barrier.
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
  await expect(page.getByText("Unsealed").first()).toBeVisible();
});
