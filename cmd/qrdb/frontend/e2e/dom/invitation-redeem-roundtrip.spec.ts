// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * invitation-redeem-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives invitations.invite and invitations.redeem against a live qrdbd:
 *
 *   0. The daemon boots and runs the first-boot ceremony: invitations are
 *      served only by an initialized server.
 *   1. The admin opens Users, clicks Invite User and reads the one-time token
 *      and the tenant the modal shows.
 *   2. The invitee opens Redeem Invitation from the Connection Manager (the
 *      sidebar's "Add server"), enters the tenant, the token and a CSR over a
 *      key they generated, and gets a certificate.
 *   3. The daemon lists the new user (GET /users/list) as active, with the
 *      enrolled key bound, and the certificate it returned is a PEM
 *      certificate.
 */

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
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

const INVITEE = "e2e-invitee";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  // Invitations are wired only on an initialized server (a user service over
  // a tenant store): run the first-boot ceremony before the UI connects.
  daemon = await startDaemon({ seal: "none-committed" });
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
});

// Runs the first-boot ceremony (what `qrdb init` does) with the none
// strategy and a server-generated admin credential this throwaway daemon
// discards.
async function initializeCeremony(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    const body = JSON.stringify({
      so_pin: "so-pin-dom-e2e",
      user_pin: "user-pin-dom-e2e",
      admin_generate: true,
    });
    const req = https.default.request(
      `${d.adminUrl}/v1/ceremony/initialize`,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: {
          "Content-Type": "application/json",
          "Content-Length": Buffer.byteLength(body),
        },
      },
      (res) => {
        let text = "";
        res.on("data", (c: Buffer) => (text += c.toString()));
        res.on("end", () => {
          if (res.statusCode && res.statusCode < 300) resolve();
          else
            reject(
              new Error(
                `ceremony initialize returned ${res.statusCode}: ${text.slice(0, 300)}`,
              ),
            );
        });
      },
    );
    req.on("error", reject);
    req.end(body);
  });
}

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Generates a P-256 key and a CSR over it with openssl; returns the CSR PEM. */
function makeCSR(commonName: string): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-invite-"));
  try {
    const csr = join(dir, "req.csr");
    execFileSync(
      "openssl",
      [
        "req",
        "-new",
        "-newkey",
        "ec",
        "-pkeyopt",
        "ec_paramgen_curve:P-256",
        "-nodes",
        "-keyout",
        join(dir, "key.pem"),
        "-out",
        csr,
        "-subj",
        `/CN=${commonName}`,
      ],
      { stdio: "pipe" },
    );
    return readFileSync(csr, "utf8");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** Expects a toast; on a miss, reports the toasts on screen. */
async function expectToast(page: Page, text: string | RegExp): Promise<string> {
  const toast = page.locator(".toast .alert").filter({ hasText: text }).last();
  try {
    await expect(toast).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(
      `expected toast ${String(text)}; toasts shown: ${JSON.stringify(shown)}\n${err}`,
    );
  }
  return toast.innerText();
}

test("invite in Users, redeem in the Connection Manager (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  expect(daemon).not.toBeNull();

  // --- Invite --------------------------------------------------------------------
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Users", exact: true }).click();
  await page.getByRole("button", { name: "Invite User" }).click();
  await page.locator("#invite-username").fill(INVITEE);
  await page.getByRole("button", { name: "Invite", exact: true }).click();
  const tokenField = page.locator(".modal input[readonly]").first();
  // The modal switches to its "Invitation created" view; a failure is an
  // error toast, which is caught here before it auto-dismisses.
  const created = page.getByText("Invitation created");
  await expect(created.or(page.locator(".toast .alert")).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  if (!(await created.isVisible())) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(`invite failed; toasts shown: ${JSON.stringify(shown)}`);
  }
  const token = await tokenField.inputValue();
  expect(token).not.toBe("");
  const tenantID = (await page.getByTestId("invite-tenant").innerText()).trim();
  expect(tenantID).not.toBe("");
  await page.getByRole("button", { name: "Done", exact: true }).click();

  // --- Redeem --------------------------------------------------------------------
  const csrPEM = makeCSR(INVITEE);
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  // The sidebar's "Add server" opens the Connection Manager.
  await page.getByRole("button", { name: "Add server", exact: true }).click();
  await page.getByRole("button", { name: "Redeem Invitation" }).first().click();
  await page.locator("#redeem-server").fill(new URL(daemon!.adminUrl).host);
  await page.locator("#redeem-tenant").fill(tenantID);
  await page.locator("#redeem-token").fill(token);
  await page.locator("#redeem-csr").fill(csrPEM);
  await page.getByRole("button", { name: "Redeem", exact: true }).click();
  const result = page.getByTestId("redeem-result");
  try {
    await expect(result).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const shown = await page.getByTestId("redeem-error").allInnerTexts();
    throw new Error(
      `redeem failed; error shown: ${JSON.stringify(shown)}\n${err}`,
    );
  }
  await expect(page.getByTestId("redeem-username")).toHaveText(INVITEE);
  const userID = (await page.getByTestId("redeem-user-id").innerText()).trim();
  expect(userID).not.toBe("");
  await expect(page.getByTestId("redeem-cert-pem")).toHaveValue(
    /^-----BEGIN CERTIFICATE-----/,
  );

  // --- Server state --------------------------------------------------------------
  const resp = await request.get(`${daemon!.adminUrl}/users/list`, {
    ignoreHTTPSErrors: true,
  });
  expect(
    resp.ok(),
    `GET /v1/users: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
  const listed = (await resp.json()) as {
    users: {
      id: string;
      username: string;
      tenant_id: string;
      lifecycle: string;
      cert_bindings?: unknown[];
    }[];
  };
  const invitee = listed.users.find((u) => u.username === INVITEE);
  expect(invitee, `GET /users/list: no ${INVITEE}`).toBeDefined();
  expect(invitee!.id).toBe(userID);
  expect(invitee!.tenant_id).toBe(tenantID);
  // Redemption activates the pending account and binds the enrolled key.
  expect(invitee!.lifecycle).toBe("active");
  expect(invitee!.cert_bindings?.length ?? 0).toBe(1);
});
