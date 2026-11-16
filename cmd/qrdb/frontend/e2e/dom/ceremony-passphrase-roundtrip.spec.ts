// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * ceremony-passphrase-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Seal Manager's ceremony card and Change Passphrase card
 * (coverage-matrix group 10: ceremony.status, ceremony.signCSR,
 * seal.changePassphrase) against a live passphrase-sealed qrdbd initialized
 * through POST /v1/ceremony/initialize:
 *
 *  - the ceremony state and CA SPKI pin the card shows equal GET
 *    /v1/ceremony/status;
 *  - a holder CSR signed in the UI under the SO PIN returns a certificate
 *    whose subject names the requested holder, issued by the ceremony CA;
 *  - a passphrase changed in the UI is the one the barrier unseals with: after
 *    a seal, the old passphrase is refused and the new one unseals.
 */

import { execFileSync } from "node:child_process";
import { X509Certificate } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const PASSPHRASE = "dom-ceremony-secret-1";
const NEW_PASSPHRASE = "dom-ceremony-secret-2";
const SO_PIN = "dom-ceremony-so-pin";
const USER_PIN = "dom-ceremony-user-pin";

let daemon: DomDaemon | null = null;

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function getJSON(request: APIRequestContext, path: string): Promise<Record<string, unknown>> {
  const res = await request.get(`${daemon!.adminUrl}${path}`, { ignoreHTTPSErrors: true });
  expect(res.status(), `${path}: ${await res.text()}`).toBe(200);
  return (await res.json()) as Record<string, unknown>;
}

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({ timeout: 15_000 });
}

async function initAndOpenSealManager(
  page: Page,
  request: APIRequestContext,
  data: Record<string, unknown> = { secret: Buffer.from(PASSPHRASE).toString("base64") },
): Promise<void> {
  const init = await request.post(`${daemon!.adminUrl}/v1/ceremony/initialize`, {
    data: { so_pin: SO_PIN, user_pin: USER_PIN, admin_generate: true, ...data },
    ignoreHTTPSErrors: true,
  });
  expect(init.ok(), `ceremony init: ${init.status()} ${await init.text()}`).toBeTruthy();
  await waitForHealthy(daemon!);
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(page.getByRole("heading", { name: "Seal Manager" })).toBeVisible();
}

/** A fresh ECDSA P-256 CSR for the holder `cn`, made with openssl. */
function makeCSR(cn: string): string {
  const dir = mkdtempSync(join(tmpdir(), "dom-csr-"));
  try {
    const key = join(dir, "key.pem");
    const csr = join(dir, "csr.pem");
    execFileSync("openssl", ["ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", key]);
    execFileSync("openssl", ["req", "-new", "-key", key, "-subj", `/CN=${cn}`, "-out", csr]);
    return readFileSync(csr, "utf8");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test("passphrase node: the ceremony card shows the operational state and CA pin GET ceremony status reports", async ({ page, request }) => {
  daemon = await startDaemon({ seal: "passphrase" });
  await initAndOpenSealManager(page, request);

  const status = await getJSON(request, "/v1/ceremony/status");
  expect(status.state).toBe("operational");
  await expect(page.getByTestId("ceremony-state")).toHaveText("operational", { timeout: 15_000 });
  expect(typeof status.spki_pin).toBe("string");
  await expect(page.getByTestId("ceremony-spki-pin")).toHaveText(String(status.spki_pin));

  // An operational ceremony issues no holder certificates: the UI reports
  // the server's refusal and shows none.
  await page.getByTestId("csr-username").fill("dom-late-holder");
  await page.getByTestId("csr-so-pin").fill(SO_PIN);
  await page.getByTestId("csr-pem").fill(makeCSR("dom-late-holder"));
  await page.getByTestId("csr-sign-btn").click();
  await expectToast(page, /enrolling state/);
  await expect(page.getByTestId("csr-cert")).toHaveCount(0);
});

test("shamir node: an enrolling ceremony card matches GET ceremony status and signs a holder CSR", async ({ page, request }) => {
  daemon = await startDaemon({ seal: "shamir" });
  const officers = ["dom-so-1", "dom-so-2", "dom-so-3"].map((username) => ({ username, role: "so", csr_pem: makeCSR(username) }));
  await initAndOpenSealManager(page, request, { threshold: 2, officers });

  const status = await getJSON(request, "/v1/ceremony/status");
  expect(status.state).toBe("enrolling");
  await expect(page.getByTestId("ceremony-state")).toHaveText("enrolling", { timeout: 15_000 });
  await expect(page.getByTestId("ceremony-state")).toHaveText(String(status.state), { timeout: 15_000 });
  const pin = (status.spki_pin as string | undefined) ?? "none";
  await expect(page.getByTestId("ceremony-spki-pin")).toHaveText(pin);

  // A wrong SO PIN is refused and no certificate is shown.
  const csr = makeCSR("dom-holder-1");
  await page.getByTestId("csr-username").fill("dom-holder-1");
  await page.getByTestId("csr-so-pin").fill("not-the-so-pin");
  await page.getByTestId("csr-role").selectOption("custodian");
  await page.getByTestId("csr-pem").fill(csr);
  await page.getByTestId("csr-sign-btn").click();
  await expect(page.locator(".toast .alert-error").first()).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("csr-cert")).toHaveCount(0);

  // The SO PIN signs it: the certificate names the holder.
  await page.getByTestId("csr-so-pin").fill(SO_PIN);
  const signed = page.waitForResponse((r) => r.url().includes("/v1/ceremony/") && r.request().method() === "POST");
  await page.getByTestId("csr-sign-btn").click();
  const signRes = await signed;
  expect(signRes.status(), `sign CSR: ${await signRes.text()}`).toBe(200);
  await expectToast(page, "CSR signed");
  const certPem = (await page.getByTestId("csr-cert-pem").textContent()) ?? "";
  const cert = new X509Certificate(certPem);
  expect(cert.subject).toContain("CN=dom-holder-1");
  expect(cert.subject).not.toBe(cert.issuer);
});

test("a passphrase changed in the UI is the one the barrier unseals with", async ({ page, request }) => {
  daemon = await startDaemon({ seal: "passphrase" });
  await initAndOpenSealManager(page, request);
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // A wrong current passphrase is refused.
  await page.getByTestId("cp-current").fill("not-the-passphrase");
  await page.getByTestId("cp-new").fill(NEW_PASSPHRASE);
  await page.getByTestId("cp-confirm").fill(NEW_PASSPHRASE);
  await page.getByTestId("cp-so-pin").fill(SO_PIN);
  await page.getByTestId("cp-submit").click();
  await expect(page.locator(".toast .alert-error").first()).toBeVisible({ timeout: 15_000 });

  await page.getByTestId("cp-current").fill(PASSPHRASE);
  await page.getByTestId("cp-new").fill(NEW_PASSPHRASE);
  await page.getByTestId("cp-confirm").fill(NEW_PASSPHRASE);
  await page.getByTestId("cp-so-pin").fill(SO_PIN);
  await page.getByTestId("cp-submit").click();
  await expectToast(page, "Passphrase changed");
  await expect(page.getByTestId("cp-current")).toHaveValue("");

  // Seal through the UI.
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  const soPin = page.getByTestId("seal-so-pin");
  if (await soPin.isVisible()) await soPin.fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(page.getByText("Enter the barrier passphrase to unseal the encryption barrier.")).toBeVisible({ timeout: 15_000 });
  expect((await getJSON(request, "/v1/barrier/status")).sealed).toBe(true);

  // The old passphrase no longer unseals.
  await page.getByPlaceholder("Enter passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();
  await expect(page.locator(".toast .alert-error").first()).toBeVisible({ timeout: 15_000 });
  expect((await getJSON(request, "/v1/barrier/status")).sealed).toBe(true);

  // The new one does.
  await page.getByPlaceholder("Enter passphrase").fill(NEW_PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();
  await expect(page.getByRole("button", { name: "Seal", exact: true })).toBeVisible({ timeout: 15_000 });
  expect((await getJSON(request, "/v1/barrier/status")).sealed).toBe(false);
});
