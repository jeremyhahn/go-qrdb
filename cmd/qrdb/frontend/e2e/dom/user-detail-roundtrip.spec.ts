// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * user-detail-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Users admin view against a live qrdbd (committed "none"
 * barrier initialized through /v1/ceremony/initialize, auth off →
 * system-admin context) and checks every change on the server:
 *
 *   1. Create a user (username + email), then change its username, email
 *      and display name in UserDetail; GET /users/{id} returns them.
 *   2. Disable, then enable; after each the server's lifecycle and the UI
 *      badge agree.
 *   3. Make a self-signed certificate with openssl, compute its SPKI SHA-256
 *      thumbprint, bind it in the UI; the server lists the binding and the
 *      UserManager thumbprint lookup opens this user. Remove the binding;
 *      the lookup now finds nobody.
 *   4. A malformed thumbprint is refused and the server's error is shown.
 */

import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { createHash, X509Certificate } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

const USERNAME = "alice-detail";
const RENAMED = "alice-renamed";
const EMAIL = "alice@example.com";
const NEW_EMAIL = "alice.renamed@example.com";
const DISPLAY_NAME = "Alice Renamed";

let daemon: DomDaemon | null = null;

// The fixture's seal "none" (subsystem off) is refused by the server's
// config validation, so the daemon runs a committed "none" barrier (auth
// still off) and is initialized through the ceremony before each test.
test.beforeEach(async ({ request }) => {
  daemon = await startDaemon({ seal: "none-committed" });
  const resp = await request.post(`${daemon.adminUrl}/v1/ceremony/initialize`, {
    data: { so_pin: "user-detail-so-pin", user_pin: "user-detail-user-pin", admin_generate: true },
  });
  expect(resp.ok(), `ceremony initialize: ${resp.status()} ${await resp.text()}`).toBeTruthy();
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Waits for a toast of the given kind whose text contains `text`. */
async function expectToast(
  page: Page,
  kind: "success" | "error" | "warning" | "info",
  text: string | RegExp,
): Promise<void> {
  await expect(
    page.locator(`.toast .alert.alert-${kind}`).filter({ hasText: text }).last(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Loads the app shell and opens the Users admin view. */
async function openUsersView(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Users", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create User" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Opens UserDetail for the user whose username is shown in the table. */
async function openUser(page: Page, username: string): Promise<void> {
  await page.getByRole("cell", { name: username, exact: true }).click();
  await expect(page.getByRole("heading", { name: "Edit User" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByTestId("user-lifecycle-badge")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

interface WireUser {
  id: string | number;
  username: string;
  email?: string;
  name?: string;
  lifecycle?: string;
  cert_bindings?: { thumbprint: string; kind: string; not_after?: string }[];
}

/** Reads one user from the live server. */
async function getUser(request: APIRequestContext, adminUrl: string, id: string): Promise<WireUser> {
  const resp = await request.get(`${adminUrl}/users/${encodeURIComponent(id)}`);
  expect(resp.ok(), `GET /users/${id}: ${resp.status()}`).toBeTruthy();
  return (await resp.json()) as WireUser;
}

/** Finds a user id by username on the live server. */
async function userIdByName(
  request: APIRequestContext,
  adminUrl: string,
  username: string,
): Promise<string> {
  const resp = await request.get(`${adminUrl}/users/by-username/${encodeURIComponent(username)}`);
  expect(resp.ok(), `GET /users/by-username/${username}: ${resp.status()}`).toBeTruthy();
  const body = (await resp.json()) as WireUser;
  expect(body.username).toBe(username);
  return String(body.id);
}

/**
 * Makes a self-signed P-256 certificate with openssl and returns the
 * base64url SHA-256 of its SubjectPublicKeyInfo, the thumbprint the server
 * binds and looks certificates up by.
 */
function selfSignedSpkiThumbprint(): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-user-cert-"));
  try {
    const keyPath = join(dir, "key.pem");
    const certPath = join(dir, "cert.pem");
    execFileSync(
      "openssl",
      [
        "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-keyout", keyPath, "-out", certPath,
        "-subj", `/CN=${RENAMED}`, "-days", "1",
      ],
      { stdio: "ignore" },
    );
    const cert = new X509Certificate(readFileSync(certPath));
    const spki = cert.publicKey.export({ type: "spki", format: "der" });
    return createHash("sha256").update(spki).digest("base64url");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test("user update, disable/enable and cert bindings round-trip (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  expect(daemon).not.toBeNull();
  const adminUrl = daemon!.adminUrl;

  await openUsersView(page);

  // --- Create -----------------------------------------------------------
  await page.getByRole("button", { name: "Create User" }).click();
  await expect(page.getByRole("heading", { name: "Create User" })).toBeVisible();
  await page.getByPlaceholder("Enter username").fill(USERNAME);
  await page.getByPlaceholder("user@example.com").fill(EMAIL);
  await page.locator("#user-detail-tenant").selectOption("system");
  await page.getByRole("checkbox", { name: /^user system/ }).check();
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.locator(".toast .alert").last()).toContainText(`User "${USERNAME}" created`, {
    timeout: LOAD_TIMEOUT,
  });
  const userId = await userIdByName(request, adminUrl, USERNAME);

  // --- 1. Update --------------------------------------------------------
  await openUser(page, USERNAME);
  await expect(page.getByPlaceholder("user@example.com")).toHaveValue(EMAIL);
  await page.getByPlaceholder("Enter username").fill(RENAMED);
  await page.getByPlaceholder("user@example.com").fill(NEW_EMAIL);
  await page.getByPlaceholder("Optional display name").fill(DISPLAY_NAME);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expectToast(page, "success", `User "${RENAMED}" updated`);
  const updated = await getUser(request, adminUrl, userId);
  expect(updated.username).toBe(RENAMED);
  expect(updated.email).toBe(NEW_EMAIL);
  expect(updated.name).toBe(DISPLAY_NAME);

  // --- 2. Disable then enable ------------------------------------------
  await openUser(page, RENAMED);
  const badge = page.getByTestId("user-lifecycle-badge");

  await page.getByTestId("user-disable").click();
  await expectToast(page, "success", `User "${RENAMED}" disabled`);
  await expect(badge).toHaveText("disabled");
  expect((await getUser(request, adminUrl, userId)).lifecycle).toBe("disabled");

  await page.getByTestId("user-enable").click();
  await expectToast(page, "success", `User "${RENAMED}" enabled`);
  await expect(badge).toHaveText("active");
  expect((await getUser(request, adminUrl, userId)).lifecycle).toBe("active");

  // --- 4. A refused binding shows the server's error ---------------------
  await page.getByTestId("user-binding-thumbprint").fill("not-a-thumbprint!");
  await page.getByTestId("user-binding-add").click();
  const refused = await request.post(`${adminUrl}/users/${userId}/cert-bindings`, {
    data: { thumbprint: "not-a-thumbprint!" },
  });
  expect(refused.ok()).toBeFalsy();
  const refusedBody = (await refused.json()) as { error?: string; message?: string };
  const serverMsg = (refusedBody.message ?? refusedBody.error ?? "").trim();
  expect(serverMsg).not.toBe("");
  await expectToast(page, "error", serverMsg);
  await expect(page.getByTestId("user-cert-bindings-empty")).toBeVisible();

  // --- 3. Cert binding: add, list, look up, remove ------------------------
  const thumbprint = selfSignedSpkiThumbprint();
  await page.getByTestId("user-binding-thumbprint").fill(thumbprint);
  await page.getByTestId("user-binding-add").click();
  await expectToast(page, "success", "Certificate binding added");
  await expect(page.getByTestId("user-binding-thumbprint-value")).toHaveText(thumbprint);

  const bound = await getUser(request, adminUrl, userId);
  expect((bound.cert_bindings ?? []).map((b) => b.thumbprint)).toContain(thumbprint);

  // Lookup by thumbprint from UserManager opens this user.
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await page.getByTestId("user-thumbprint-input").fill(thumbprint);
  await page.getByTestId("user-thumbprint-submit").click();
  await expect(page.getByRole("heading", { name: "Edit User" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByPlaceholder("Enter username")).toHaveValue(RENAMED);
  await expect(page.getByTestId("user-binding-thumbprint-value")).toHaveText(thumbprint);

  // Remove the binding; the server no longer lists it.
  await page.getByRole("button", { name: `Remove binding ${thumbprint}`, exact: true }).click();
  await expectToast(page, "success", "Certificate binding removed");
  await expect(page.getByTestId("user-cert-bindings-empty")).toBeVisible();
  const unbound = await getUser(request, adminUrl, userId);
  expect((unbound.cert_bindings ?? []).map((b) => b.thumbprint)).not.toContain(thumbprint);

  // The lookup now finds nobody, in the UI and on the server.
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await page.getByTestId("user-thumbprint-input").fill(thumbprint);
  await page.getByTestId("user-thumbprint-submit").click();
  await expect(page.getByTestId("user-thumbprint-not-found")).toBeVisible({ timeout: LOAD_TIMEOUT });
  const gone = await request.get(
    `${adminUrl}/users/by-cert-thumbprint/${encodeURIComponent(thumbprint)}`,
  );
  expect(gone.status()).toBe(404);
});
