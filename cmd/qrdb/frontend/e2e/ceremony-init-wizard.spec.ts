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
 * ceremony-init-wizard.spec.ts (mocked tier)
 *
 * `qrdb init` is the ONLY system bootstrap, so ServerInitWizard drives
 * client.ceremony.initialize end to end (not the former two-call
 * seal.initialize/initializeShamir + officers.registerOfficer flow, which
 * a ceremony-backed system barrier now refuses -- go-qrdb#4578 review item
 * 2). These specs pin both barrier modes:
 *
 *   - passphrase: barrier + admin (CSR path) -> POSTs secret and
 *     admin_csr_pem to /v1/ceremony/initialize in one call, shows the
 *     issued admin certificate.
 *   - shamir: barrier + officers (collected locally, no per-holder
 *     network call) + admin (generate path) -> POSTs officers and
 *     admin_generate together, shows each holder's issued certificate and
 *     the claim-status panel, polled from /v1/barrier/officers.
 *
 * No live qrdbd is required; all REST calls are intercepted via
 * page.route(), the same pattern web-mode-server-init-wizard.spec.ts uses.
 */

import { expect } from "@playwright/test";
import { test as base } from "./fixtures/api-guard.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;
const CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-csr\n-----END CERTIFICATE REQUEST-----";
const ADMIN_CSR = "-----BEGIN CERTIFICATE REQUEST-----\nmock-admin-csr\n-----END CERTIFICATE REQUEST-----";

// ---------------------------------------------------------------------------
// Route injection — sealed + uninitialized server
// ---------------------------------------------------------------------------

interface CeremonyCall {
  body: Record<string, unknown>;
}

/**
 * Registers page.route() handlers simulating a sealed, uninitialized
 * qrdbd and captures every POST /v1/ceremony/initialize body, so a test
 * can assert the exact request the wizard sent.
 */
async function injectSealedUninitializedMock(page: Page): Promise<CeremonyCall[]> {
  const calls: CeremonyCall[] = [];

  await page.route("**/health", (route) => {
    void route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "sealed" }) });
  });

  // /status is not on the sealed-window allowlist: refused as the REST
  // sealed-window gate does, which connect() tolerates.
  await page.route((url) => url.pathname === "/status", (route) => {
    void route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "barrier sealed", code: "SEALED" }),
    });
  });

  await page.route("**/v1/barrier/status", (route) => {
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ sealed: true, strategy: "passphrase", hardware_backed: false }),
    });
  });

  await page.route("**/v1/ceremony/initialize", (route) => {
    const body = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
    calls.push({ body });
    const officers = (body.officers as Array<{ username: string; role?: string }> | undefined) ?? [];
    const holders = officers.map((o) => ({
      username: o.username,
      role: o.role ?? "so",
      cert_pem: `-----BEGIN CERTIFICATE-----\n${o.username}\n-----END CERTIFICATE-----`,
    }));
    const isGenerate = body.admin_generate === true;
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        spki_pin: "mockpin",
        ca_cert_pem: "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
        tls_cert_pem: "-----BEGIN CERTIFICATE-----\ntls\n-----END CERTIFICATE-----",
        state: holders.length > 0 ? "enrolling" : "operational",
        share_count: holders.length,
        holders,
        admin_cert_pem: "-----BEGIN CERTIFICATE-----\nadmin\n-----END CERTIFICATE-----",
        // The REST path returns the generated key to the caller directly
        // (there is no "local machine" of its own to save it to, unlike
        // the desktop Wails binding) -- base64, matching the wire
        // convention secret/encryption_key already use.
        admin_key_pem: isGenerate ? "bW9jay1hZG1pbi1rZXk=" : undefined,
        admin_credential_source: isGenerate ? "generated" : "csr",
      }),
    });
  });

  await page.route("**/v1/barrier/officers", (route) => {
    const officers = calls.at(-1)?.body.officers as Array<{ username: string; role?: string }> | undefined;
    void route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        officers: (officers ?? []).map((o, i) => ({
          username: o.username,
          role: o.role ?? "so",
          cert_thumbprint: `mock-${o.username}`,
          share_index: i + 1,
          designated: "mtls",
          factors: ["mtls"],
          encryption_kem: "",
          enrolled_at: "2026-10-01T00:00:00Z",
          claimed_at: "0001-01-01T00:00:00Z",
          share_pending: true,
        })),
      }),
    });
  });

  return calls;
}

const test = base.extend<{ ceremonyCalls: CeremonyCall[] }>({
  // auto: true so every test in this file gets the sealed+uninitialized
  // route mocks even when it never reads ceremonyCalls itself -- a
  // fixture a test does not reference as a parameter is never
  // instantiated otherwise, which would leave /health and the other
  // routes unmocked and hitting vite's dev-server proxy for a real
  // backend that is not running.
  ceremonyCalls: [async ({ page }, use) => {
    const calls = await injectSealedUninitializedMock(page);
    await use(calls);
  }, { auto: true }],
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function gotoWizard(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Server Initialization" })).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("Global Encryption Barrier")).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("ServerInitWizard — ceremony.initialize", () => {
  test("passphrase barrier + CSR admin credential: one POST carries both", async ({ page, ceremonyCalls }) => {
    await gotoWizard(page);

    // Barrier step: passphrase mode is the default.
    await page.locator("#setup-passphrase").fill("S3cur3P@ss!");
    await page.locator("#setup-passphrase-confirm").fill("S3cur3P@ss!");
    await page.getByRole("button", { name: "Next" }).click();

    // Admin step (no officers step for passphrase mode).
    await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByTestId("admin-mode-csr").check();
    await page.getByTestId("admin-csr-pem").fill(ADMIN_CSR);
    await page.getByTestId("admin-next-btn").click();

    await expect(page.getByRole("heading", { name: "Server Initialized" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(page.getByTestId("admin-cert-pem-issued")).toHaveValue(/BEGIN CERTIFICATE/);
    // Dev-generate's save-notice must NOT appear on the CSR path.
    await expect(page.getByTestId("admin-key-saved-notice")).toHaveCount(0);

    expect(ceremonyCalls).toHaveLength(1);
    const body = ceremonyCalls[0]!.body;
    expect(body.secret).toBe(Buffer.from("S3cur3P@ss!").toString("base64"));
    expect(body.admin_csr_pem).toBe(ADMIN_CSR);
    expect(body.officers).toBeUndefined();

    await page.getByTestId("open-database-btn").click();
  });

  test("shamir barrier with locally-collected holders + generated admin credential", async ({ page, ceremonyCalls }) => {
    await gotoWizard(page);

    // Barrier step: switch to Shamir, 2-of-2.
    await page.getByText("Shamir (M-of-N)").click();
    await page.locator("#setup-shamir-threshold").fill("2");
    await page.locator("#setup-shamir-shares").fill("2");
    await page.getByRole("button", { name: "Next" }).click();

    // Officers step: no network call happens here at all -- holders are
    // collected locally, not registered individually (the two-call flow
    // ceremony-backed Initialize retired).
    await expect(page.getByRole("heading", { name: "Enrol Share Holders" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByTestId("officer-username-0").fill("alice");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await page.getByTestId("officer-username-1").fill("bob");
    await page.getByTestId("officer-role-1").selectOption("custodian");
    await page.getByTestId("officer-csr-1").fill(CSR);
    expect(ceremonyCalls).toHaveLength(0);
    await page.getByTestId("officers-next-btn").click();

    // Admin step: the dev-only generate path.
    await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    await page.getByTestId("admin-mode-generate").check();
    await expect(page.getByTestId("admin-generate-warning")).toContainText("dev/test/evaluation ONLY");
    await page.getByTestId("admin-next-btn").click();

    await expect(page.getByRole("heading", { name: "Server Initialized" })).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Exactly one call, carrying both holders and admin_generate together.
    expect(ceremonyCalls).toHaveLength(1);
    const body = ceremonyCalls[0]!.body;
    expect(body.admin_generate).toBe(true);
    expect(body.threshold).toBe(2);
    expect(body.officers).toEqual([
      { username: "alice", role: "so", csr_pem: CSR },
      { username: "bob", role: "custodian", csr_pem: CSR },
    ]);

    // The REST response's admin_key_pem is never relevant on the desktop
    // path, but this test runs the REST (web-mode) client, which DOES
    // receive it; the page must still never print it anywhere.
    await expect(page.locator("body")).not.toContainText("mock-admin-key");
    await expect(page.locator("body")).not.toContainText("PRIVATE KEY");

    // Both holders' certificates are shown, issued in the same response.
    await expect(page.getByTestId("holder-cert-alice")).toHaveValue(/BEGIN CERTIFICATE/);
    await expect(page.getByTestId("holder-cert-bob")).toHaveValue(/BEGIN CERTIFICATE/);

    // Claim status, polled from /v1/barrier/officers.
    await expect(page.getByTestId("officer-claim-status-table")).toContainText("alice");
    await expect(page.getByTestId("officer-claim-status-table")).toContainText("bob");

    await page.getByTestId("open-database-btn").click();
  });

  test("shamir officers step blocks Next until every holder is filled in", async ({ page }) => {
    await gotoWizard(page);
    await page.getByText("Shamir (M-of-N)").click();
    await page.locator("#setup-shamir-threshold").fill("2");
    await page.locator("#setup-shamir-shares").fill("2");
    await page.getByRole("button", { name: "Next" }).click();

    await expect(page.getByRole("heading", { name: "Enrol Share Holders" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    const next = page.getByTestId("officers-next-btn");
    await expect(next).toBeDisabled();

    await page.getByTestId("officer-username-0").fill("alice");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await expect(next).toBeDisabled();

    await page.getByTestId("officer-username-1").fill("bob");
    await page.getByTestId("officer-csr-1").fill(CSR);
    await expect(next).toBeEnabled();
  });

  test("admin step blocks Initialize until a CSR or certificate is provided", async ({ page }) => {
    await gotoWizard(page);
    await page.locator("#setup-passphrase").fill("S3cur3P@ss!");
    await page.locator("#setup-passphrase-confirm").fill("S3cur3P@ss!");
    await page.getByRole("button", { name: "Next" }).click();

    await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible({ timeout: LOAD_TIMEOUT });
    const next = page.getByTestId("admin-next-btn");
    // CSR is the default mode; empty CSR blocks Next.
    await expect(next).toBeDisabled();
    await page.getByTestId("admin-csr-pem").fill(ADMIN_CSR);
    await expect(next).toBeEnabled();

    // Switching to BYOC with no certificate blocks it again.
    await page.getByTestId("admin-mode-byoc").check();
    await expect(next).toBeDisabled();
    await page.getByTestId("admin-cert-pem").fill("-----BEGIN CERTIFICATE-----\nbyoc\n-----END CERTIFICATE-----");
    await expect(next).toBeEnabled();

    // The dev-only generate path needs no input at all.
    await page.getByTestId("admin-mode-generate").check();
    await expect(next).toBeEnabled();
  });

  test("a ceremony.initialize refusal is shown on the admin step, without losing the collected holders", async ({ page }) => {
    await gotoWizard(page);
    await page.getByText("Shamir (M-of-N)").click();
    await page.locator("#setup-shamir-threshold").fill("2");
    await page.locator("#setup-shamir-shares").fill("2");
    await page.getByRole("button", { name: "Next" }).click();
    await page.getByTestId("officer-username-0").fill("alice");
    await page.getByTestId("officer-csr-0").fill(CSR);
    await page.getByTestId("officer-username-1").fill("bob");
    await page.getByTestId("officer-csr-1").fill(CSR);
    await page.getByTestId("officers-next-btn").click();

    // Override the route to refuse this one call.
    await page.route("**/v1/ceremony/initialize", (route) => {
      void route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "ceremony: duplicate username" }) });
    });

    await page.getByTestId("admin-mode-generate").check();
    await page.getByTestId("admin-next-btn").click();

    await expect(page.getByTestId("admin-apply-error")).toContainText("duplicate username");
    // Back on the admin step (not bounced all the way back to officers),
    // and the already-collected holders are not lost.
    await expect(page.getByRole("heading", { name: "Bootstrap Admin Credential" })).toBeVisible();
    await page.getByTestId("admin-back-btn").click();
    await expect(page.getByTestId("officer-username-0")).toHaveValue("alice");
    await expect(page.getByTestId("officer-username-1")).toHaveValue("bob");
  });
});
