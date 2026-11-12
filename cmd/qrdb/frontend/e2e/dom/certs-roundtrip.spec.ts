// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * certs-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Client-certificate lifecycle against a live daemon through the
 * Certificates view:
 *   1. a fresh daemon lists no issued certificates;
 *   2. an account created through the REST API gets a certificate issued
 *      from a CSR (web mode has no local key generation, so the spec makes
 *      the CSR with openssl);
 *   3. after a reload the list (filtered by "All tenants", i.e. the caller's
 *      own tenant) shows the certificate's serial, username, tenant and
 *      Active status, read back from the server;
 *   4. revoking it by serial turns the status to Revoked, also after reload.
 */

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const USERNAME = "cert-alice";
const SO_PIN = "certs-roundtrip-so-pin-0123";
const USER_PIN = "certs-roundtrip-user-pin-0123";
const P12_PASS = "certs-roundtrip-p12-pass";

let daemon: DomDaemon | null = null;

// The certificate provider and enrolment services are built only over a
// barrier, so the daemon boots with a committed no-seal barrier that the
// spec initializes through the ceremony API (SO and user PINs are required).
test.beforeEach(async ({ request }) => {
  daemon = await startDaemon({ seal: "none-committed" });
  const init = await request.post(`${daemon.adminUrl}/v1/ceremony/initialize`, {
    data: { so_pin: SO_PIN, user_pin: USER_PIN, admin_generate: true },
    ignoreHTTPSErrors: true,
  });
  expect(init.ok(), `ceremony init: ${init.status()} ${await init.text()}`).toBeTruthy();
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Generates a P-256 key and a CSR for `cn` with openssl, both per run. */
function makeCSR(cn: string): { csr: string; key: string } {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  try {
    const csr = execFileSync(
      "openssl",
      [
        "req", "-new",
        "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-keyout", join(dir, "key.pem"),
        "-subj", `/CN=${cn}`,
      ],
      { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] },
    );
    return { csr, key: readFileSync(join(dir, "key.pem"), "utf8") };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** The leaf serial (upper-case hex) OpenSSL reads from a PKCS#12 archive. */
function pkcs12Serial(b64: string, passphrase: string): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-p12-"));
  try {
    const p12 = join(dir, "export.p12");
    writeFileSync(p12, Buffer.from(b64, "base64"));
    const leaf = execFileSync(
      "openssl",
      ["pkcs12", "-in", p12, "-passin", `pass:${passphrase}`, "-nokeys", "-clcerts"],
      { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
    );
    const out = execFileSync("openssl", ["x509", "-noout", "-serial"], {
      input: leaf,
      encoding: "utf8",
    });
    return out.trim().replace(/^serial=/, "");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** Opens the Certificates admin view. */
async function openCerts(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByRole("button", { name: "Certificates", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Certificates", exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("a client certificate is issued, listed, exported as PKCS#12 and revoked against the live backend", async ({
  page,
  request,
}) => {
  await openCerts(page);
  await expect(page.getByText("No certificates issued.")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // --- The account must exist before a certificate can be issued. --------
  const created = await request.post(`${daemon!.adminUrl}/users`, {
    data: { username: USERNAME, email: `${USERNAME}@example.com` },
    ignoreHTTPSErrors: true,
  });
  expect(created.ok(), `create user: ${created.status()} ${await created.text()}`).toBeTruthy();

  // --- Issue from a CSR through the view. ----------------------------------
  const csr = makeCSR(USERNAME);
  await page.getByRole("button", { name: "Issue", exact: true }).first().click();
  await page.locator("input[placeholder='e.g. alice']").first().fill(USERNAME);
  await page
    .locator("textarea[placeholder='-----BEGIN CERTIFICATE REQUEST-----']")
    .fill(csr.csr);
  await page.locator("input[placeholder='8760h']").fill("8760h");
  await page.getByRole("button", { name: "Issue", exact: true }).last().click();
  await expect(page.getByText(`Certificate issued for "${USERNAME}"`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText("Certificate PEM", { exact: true })).toBeVisible();
  const certPEM = await page.locator("textarea[readonly]").first().inputValue();
  expect(certPEM).toContain("BEGIN CERTIFICATE");

  // --- A reload lists it from the server, with its detail columns. -------
  await page.reload();
  await openCerts(page);
  const row = page.locator("table tbody tr").filter({ hasText: USERNAME });
  await expect(row).toHaveCount(1, { timeout: LOAD_TIMEOUT });
  await expect(row.locator(".badge", { hasText: "Active" })).toBeVisible();
  await expect(row.locator("td").nth(2)).toHaveText("system");
  const serial = (await row.locator("td").first().innerText()).trim();
  expect(serial).not.toBe("");

  // --- The row detail shows the server's record. ----------------------------
  await row.getByRole("button", { name: "Details", exact: true }).click();
  const detail = page.getByTestId("cert-detail");
  const field = (name: string) =>
    detail.locator("dt", { hasText: name }).locator("xpath=following-sibling::dd[1]");
  await expect(field("Serial")).toHaveText(serial);
  await expect(field("Tenant")).toHaveText("system");
  await expect(field("Validity remaining")).toHaveText(/^36[45] days$/);

  // --- Export the issued certificate and its key as PKCS#12. -----------------
  // The archive is built in the browser; the server only reports the serial.
  await page.getByRole("button", { name: "Export PKCS#12", exact: true }).first().click();
  await page.locator("textarea[placeholder='Paste certificate PEM here']").fill(certPEM);
  await page.locator("textarea[placeholder='Paste private key PEM here']").fill(csr.key);
  await page.locator("input[placeholder='PKCS#12 passphrase']").fill(P12_PASS);
  await page.getByRole("button", { name: "Export PKCS#12", exact: true }).last().click();
  await expect(page.getByText("Certificate exported successfully").last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  const pkcs12 = await page
    .locator("div", { has: page.getByText("PKCS#12 Data (Base64)", { exact: true }) })
    .locator("textarea[readonly]")
    .last()
    .inputValue();
  expect(pkcs12).not.toBe("");

  const listed = await request.get(`${daemon!.adminUrl}/certs/list`, { ignoreHTTPSErrors: true });
  expect(listed.ok(), `list certs: ${listed.status()}`).toBeTruthy();
  const records = ((await listed.json()) as { certs: { serial: string; username: string }[] }).certs;
  const record = records.find((c) => c.username === USERNAME);
  expect(record?.serial).toBe(serial);
  expect(BigInt(`0x${pkcs12Serial(pkcs12, P12_PASS)}`)).toBe(BigInt(`0x${record!.serial}`));

  // --- Revoke by serial. ---------------------------------------------------
  await page.getByRole("button", { name: "Revoke", exact: true }).first().click();
  await page.locator("input[placeholder='e.g. 1a2b3c4d...']").fill(serial);
  await page.getByRole("button", { name: "Revoke", exact: true }).last().click();
  await expect(page.getByText(`Revoking serial "${serial}" is permanent.`)).toBeVisible();
  await page.getByRole("button", { name: "Confirm Revoke", exact: true }).click();
  await expect(page.getByText(`Certificate ${serial} revoked`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  await page.reload();
  await openCerts(page);
  await expect(
    page.locator("table tbody tr").filter({ hasText: serial }).locator(".badge", { hasText: "Revoked" }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
});
