// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * officer-claim-page.spec.ts (mocked tier)
 *
 * Full UI coverage for OfficerClaimPage.svelte — the Security Officer (SO)
 * share-claim ceremony page.
 *
 * Routing: App.svelte only parses the officer-claim hash in WEB mode
 * (!isDesktop()), before any seal-status routing:
 *
 *   #/officer/claim?u=<username>[&t=<tenantId>]
 *
 * so this spec uses the web-mode REST mock fixture (no window.go injected)
 * and navigates exactly the way a real officer would:
 *
 *   http://localhost:5173/#/officer/claim?u=so-alice
 *
 * REST wire shapes mirror the real routes proven by
 * e2e/real-backend/officer-claim-auth-enabled.spec.ts and the TS SDK
 * (sdk/typescript/src/rest.ts — responses are deep snake_case→camelCase):
 *
 *   GET  /v1/barrier/officers/claim-status
 *        → { officers: [{ username, cert_thumbprint, share_index,
 *                         auth_factor, enrolled_at, claimed_at,
 *                         share_pending }] }
 *   POST /v1/barrier/officers/{username}/claim/begin   (body {})
 *        → { nonce_hex, expires_at }
 *   POST /v1/barrier/officers/{username}/claim/complete
 *        (body { nonce_hex, signature })
 *        → { share_index, share_bytes }
 *
 * Covered states: loading→idle, already-claimed, officer-not-found error,
 * nonce-ready (begin), cancel, completing spinner, revealed (mask/reveal,
 * clipboard copy), acknowledge (share wiped from the DOM — the
 * security-relevant assertion), begin-failure and complete-failure error
 * surfaces, and the tenant-scoped claim-status route.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";
import type { Page, Route } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Mock data
// ---------------------------------------------------------------------------

const OFFICER = "so-alice";
const TENANT_ID = "tenant-1";
const NONCE_HEX =
  "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90";
const NONCE_EXPIRES_AT = "2026-01-05T12:05:00Z";
const SIGNATURE_B64 = "bW9jay1zaWduYXR1cmUtb3Zlci1ub25jZS1ieXRlcw==";
// Distinctive value so a bare text search over the DOM cannot false-match.
const SHARE_BYTES = "MOCK-SHARE-BYTES-8f3a71-Zm9vYmFyLXNoYXJl";
const SHARE_INDEX = 2;

/** Wire-shape (snake_case) officer entry with a pending share. */
const pendingOfficerWire = {
  username: OFFICER,
  cert_thumbprint: "ab12cd34ef56",
  share_index: SHARE_INDEX,
  auth_factor: "mtls",
  enrolled_at: "2026-01-05T12:00:00Z",
  claimed_at: "",
  share_pending: true,
};

/** Wire-shape officer entry whose share was already claimed. */
const claimedOfficerWire = {
  ...pendingOfficerWire,
  claimed_at: "2026-01-06T09:30:00Z",
  share_pending: false,
};

// ---------------------------------------------------------------------------
// Route helpers (layered on top of the web-mode fixture's base REST mock)
// ---------------------------------------------------------------------------

/** A captured API request for wire-shape assertions. */
interface CapturedRequest {
  method: string;
  url: string;
  postData: unknown;
}

function fulfillJSON(route: Route, status: number, body: unknown): void {
  void route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

/** Mocks GET /v1/barrier/officers/claim-status (system barrier). */
async function mockClaimStatus(
  page: Page,
  officers: Record<string, unknown>[],
): Promise<CapturedRequest[]> {
  const captured: CapturedRequest[] = [];
  await page.route("**/v1/barrier/officers/claim-status", (route) => {
    captured.push({
      method: route.request().method(),
      url: route.request().url(),
      postData: null,
    });
    fulfillJSON(route, 200, { officers });
  });
  return captured;
}

/** Mocks POST /v1/barrier/officers/{username}/claim/begin. */
async function mockClaimBegin(
  page: Page,
  opts: { status?: number; body?: unknown } = {},
): Promise<CapturedRequest[]> {
  const captured: CapturedRequest[] = [];
  const status = opts.status ?? 200;
  const body =
    opts.body ?? { nonce_hex: NONCE_HEX, expires_at: NONCE_EXPIRES_AT };
  await page.route(`**/v1/barrier/officers/${OFFICER}/claim/begin`, (route) => {
    captured.push({
      method: route.request().method(),
      url: route.request().url(),
      postData: route.request().postDataJSON() as unknown,
    });
    fulfillJSON(route, status, body);
  });
  return captured;
}

/** Mocks POST /v1/barrier/officers/{username}/claim/complete. */
async function mockClaimComplete(
  page: Page,
  opts: { status?: number; body?: unknown; delayMs?: number } = {},
): Promise<CapturedRequest[]> {
  const captured: CapturedRequest[] = [];
  const status = opts.status ?? 200;
  const body =
    opts.body ?? { share_index: SHARE_INDEX, share_bytes: SHARE_BYTES };
  await page.route(
    `**/v1/barrier/officers/${OFFICER}/claim/complete`,
    async (route) => {
      captured.push({
        method: route.request().method(),
        url: route.request().url(),
        postData: route.request().postDataJSON() as unknown,
      });
      if (opts.delayMs !== undefined) {
        await new Promise((resolve) => setTimeout(resolve, opts.delayMs));
      }
      fulfillJSON(route, status, body);
    },
  );
  return captured;
}

/** Navigates the way a real officer would: via the claim hash route. */
async function gotoClaimPage(page: Page, tenantId?: string): Promise<void> {
  const t = tenantId !== undefined ? `&t=${tenantId}` : "";
  await page.goto(`/#/officer/claim?u=${OFFICER}${t}`);
  await expect(
    page.getByRole("heading", { name: "Security Officer Share Claim" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Drives the page from idle through a successful claim to "revealed". */
async function claimThroughReveal(page: Page): Promise<void> {
  await gotoClaimPage(page);
  await page.getByTestId("claim-begin-btn").click();
  await expect(page.getByTestId("signature-paste")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByTestId("signature-paste").fill(SIGNATURE_B64);
  await page.getByTestId("claim-complete-btn").click();
  await expect(
    page.getByRole("heading", { name: "Your Share — Save It Now" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

// ---------------------------------------------------------------------------
// 1. Routing & initial states
// ---------------------------------------------------------------------------

test.describe("OfficerClaimPage — routing and initial states", () => {
  test("hash route renders the claim page in idle state and fetches claim-status", async ({
    page,
  }) => {
    const statusCalls = await mockClaimStatus(page, [pendingOfficerWire]);

    await gotoClaimPage(page);

    // Idle state: officer identity, share metadata, one-time warning, CTA.
    await expect(page.getByText("Authenticated as")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Claim Your Shamir Share" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.getByText("is sealed and awaiting your claim"),
    ).toBeVisible();
    const shareIndexStat = page
      .locator(".stat")
      .filter({ hasText: "Share Index" });
    await expect(shareIndexStat).toContainText(`#${SHARE_INDEX}`);
    await expect(page.getByText("exactly once")).toBeVisible();
    await expect(page.getByTestId("claim-begin-btn")).toBeVisible();

    // The page loaded officer info via the claim-status endpoint.
    expect(statusCalls.length).toBeGreaterThanOrEqual(1);
    expect(statusCalls[0]?.method).toBe("GET");
    expect(statusCalls[0]?.url).toContain("/v1/barrier/officers/claim-status");
  });

  test("already-claimed officer sees the claimed notice, not the claim button", async ({
    page,
  }) => {
    await mockClaimStatus(page, [claimedOfficerWire]);

    await gotoClaimPage(page);

    await expect(
      page.getByRole("heading", { name: "Share Already Claimed" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.getByText("It cannot be retrieved again"),
    ).toBeVisible();
    await expect(page.getByTestId("claim-begin-btn")).toHaveCount(0);
  });

  test("unknown officer surfaces a registry error with retry", async ({
    page,
  }) => {
    await mockClaimStatus(page, []);

    await gotoClaimPage(page);

    const errorAlert = page
      .locator('[role="alert"]')
      .filter({ hasText: "not found in registry" });
    await expect(errorAlert).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(errorAlert).toContainText(`Officer "${OFFICER}" not found`);
    await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();
    await expect(page.getByTestId("claim-begin-btn")).toHaveCount(0);
  });

  test("tenant claim route loads tenant-scoped claim-status and shows the tenant", async ({
    page,
  }) => {
    const captured: CapturedRequest[] = [];
    await page.route(
      `**/v1/tenants/${TENANT_ID}/barrier/officers/claim-status`,
      (route) => {
        captured.push({
          method: route.request().method(),
          url: route.request().url(),
          postData: null,
        });
        fulfillJSON(route, 200, { officers: [pendingOfficerWire] });
      },
    );

    await gotoClaimPage(page, TENANT_ID);

    await expect(page.getByText(`(tenant:`)).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(
      page.getByRole("heading", { name: "Claim Your Shamir Share" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    expect(captured.length).toBeGreaterThanOrEqual(1);
    expect(captured[0]?.url).toContain(
      `/v1/tenants/${TENANT_ID}/barrier/officers/claim-status`,
    );
  });
});

// ---------------------------------------------------------------------------
// 2. Begin claim
// ---------------------------------------------------------------------------

test.describe("OfficerClaimPage — begin claim", () => {
  test("Claim My Share POSTs claim/begin and renders the signing challenge", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    const beginCalls = await mockClaimBegin(page);

    await gotoClaimPage(page);
    await page.getByTestId("claim-begin-btn").click();

    // Awaiting-signature state: nonce, expiry, offline signing commands.
    await expect(
      page.getByRole("heading", { name: "Sign the Challenge Nonce" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("challenge-nonce")).toHaveText(NONCE_HEX);
    await expect(page.locator(".mockup-code")).toContainText(NONCE_HEX);
    await expect(page.locator(".mockup-code")).toContainText("openssl dgst");

    // Wire assertion: exactly one POST to the begin route.
    expect(beginCalls).toHaveLength(1);
    expect(beginCalls[0]?.method).toBe("POST");
    expect(beginCalls[0]?.url).toContain(
      `/v1/barrier/officers/${OFFICER}/claim/begin`,
    );

    // Complete is gated on a non-blank signature paste (trim guard).
    await expect(page.getByTestId("claim-complete-btn")).toBeDisabled();
    await page.getByTestId("signature-paste").fill("   ");
    await expect(page.getByTestId("claim-complete-btn")).toBeDisabled();
    await page.getByTestId("signature-paste").fill(SIGNATURE_B64);
    await expect(page.getByTestId("claim-complete-btn")).toBeEnabled();
  });

  test("Cancel returns from the signing challenge to idle", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);

    await gotoClaimPage(page);
    await page.getByTestId("claim-begin-btn").click();
    await expect(page.getByTestId("signature-paste")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await page.getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByTestId("claim-begin-btn")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("challenge-nonce")).toHaveCount(0);
    await expect(page.getByTestId("signature-paste")).toHaveCount(0);
  });

  test("begin failure surfaces the server error and renders no share", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page, {
      status: 500,
      body: { error: "internal seal error" },
    });

    await gotoClaimPage(page);
    await page.getByTestId("claim-begin-btn").click();

    const errorAlert = page
      .locator('[role="alert"]')
      .filter({ hasText: "internal seal error" });
    await expect(errorAlert).toBeVisible({ timeout: LOAD_TIMEOUT });

    // No share material anywhere after a failed begin.
    await expect(page.getByTestId("share-bytes")).toHaveCount(0);
    await expect(page.getByText(SHARE_BYTES)).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "Your Share — Save It Now" }),
    ).toHaveCount(0);

    // Retry re-fetches officer status and restores the idle state.
    await page.getByRole("button", { name: "Retry" }).click();
    await expect(page.getByTestId("claim-begin-btn")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });
});

// ---------------------------------------------------------------------------
// 3. Complete claim
// ---------------------------------------------------------------------------

test.describe("OfficerClaimPage — complete claim", () => {
  test("complete POSTs the signature and reveals the share exactly once", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);
    const completeCalls = await mockClaimComplete(page);

    await claimThroughReveal(page);

    // Wire assertion: the paste + nonce round-trip exactly as the API expects.
    expect(completeCalls).toHaveLength(1);
    expect(completeCalls[0]?.method).toBe("POST");
    expect(completeCalls[0]?.url).toContain(
      `/v1/barrier/officers/${OFFICER}/claim/complete`,
    );
    expect(completeCalls[0]?.postData).toEqual({
      nonce_hex: NONCE_HEX,
      signature: SIGNATURE_B64,
    });

    // One-time warning present; share masked until explicitly revealed.
    await expect(
      page.getByText("This is the only time your share will be shown."),
    ).toBeVisible();
    await expect(page.getByText(`Share #${SHARE_INDEX}`)).toBeVisible();
    await expect(page.getByTestId("share-bytes")).toHaveCount(0);
    await expect(page.getByText("(click Reveal to show)")).toBeVisible();

    // Reveal → bytes visible; Hide → masked again.
    await page.getByRole("button", { name: "Reveal share" }).click();
    await expect(page.getByTestId("share-bytes")).toHaveText(SHARE_BYTES);
    await page.getByRole("button", { name: "Hide share" }).click();
    await expect(page.getByTestId("share-bytes")).toHaveCount(0);

    // Acknowledge is gated on the confirmation checkbox.
    await expect(page.getByTestId("acknowledge-btn")).toBeDisabled();
  });

  test("shows the verifying spinner while claim-complete is in flight", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);
    await mockClaimComplete(page, { delayMs: 700 });

    await gotoClaimPage(page);
    await page.getByTestId("claim-begin-btn").click();
    await page.getByTestId("signature-paste").fill(SIGNATURE_B64);
    await page.getByTestId("claim-complete-btn").click();

    await expect(
      page.getByText("Verifying signature and retrieving share..."),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.getByRole("heading", { name: "Your Share — Save It Now" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("copy button writes the share bytes to the clipboard", async ({
    page,
    context,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);
    await mockClaimComplete(page);

    await claimThroughReveal(page);

    await page
      .getByRole("button", { name: "Copy share to clipboard" })
      .click();
    await expect(page.getByText("Copied")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    const clipboardText = await page.evaluate(() =>
      navigator.clipboard.readText(),
    );
    expect(clipboardText).toBe(SHARE_BYTES);
  });

  test("acknowledge wipes the share from the DOM", async ({ page }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);
    await mockClaimComplete(page);

    await claimThroughReveal(page);

    // Reveal so the bytes are demonstrably in the DOM before the wipe.
    await page.getByRole("button", { name: "Reveal share" }).click();
    await expect(page.getByTestId("share-bytes")).toHaveText(SHARE_BYTES);

    await page.getByTestId("share-acknowledged-checkbox").check();
    await expect(page.getByTestId("acknowledge-btn")).toBeEnabled();
    await page.getByTestId("acknowledge-btn").click();

    // Post-acknowledge state.
    await expect(
      page.getByRole("heading", { name: "Share Claim Complete" }),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      page.getByText("Share acknowledged and cleared from memory."),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Security-relevant: the share text must be gone from the entire DOM.
    await expect(page.getByTestId("share-bytes")).toHaveCount(0);
    await expect(page.getByText(SHARE_BYTES)).toHaveCount(0);
    await expect(page.getByTestId("acknowledge-btn")).toHaveCount(0);
    await expect(
      page.getByTestId("share-acknowledged-checkbox"),
    ).toHaveCount(0);
  });

  test("complete failure surfaces the error and never renders a share", async ({
    page,
  }) => {
    await mockClaimStatus(page, [pendingOfficerWire]);
    await mockClaimBegin(page);
    await mockClaimComplete(page, {
      status: 403,
      body: { error: "signature verification failed" },
    });

    await gotoClaimPage(page);
    await page.getByTestId("claim-begin-btn").click();
    await page.getByTestId("signature-paste").fill(SIGNATURE_B64);
    await page.getByTestId("claim-complete-btn").click();

    const errorAlert = page
      .locator('[role="alert"]')
      .filter({ hasText: "signature verification failed" });
    await expect(errorAlert).toBeVisible({ timeout: LOAD_TIMEOUT });

    // No share material rendered on the failure path.
    await expect(page.getByTestId("share-bytes")).toHaveCount(0);
    await expect(page.getByText(SHARE_BYTES)).toHaveCount(0);
    await expect(page.getByTestId("acknowledge-btn")).toHaveCount(0);
  });
});
