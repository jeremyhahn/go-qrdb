// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * tenant-update-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the tenant update (TenantDetail → client.tenants.update) and tenant
 * delete (TenantManager → client.tenants.delete) surfaces against a live
 * qrdbd (committed, unencrypted barrier brought up by the first-boot
 * ceremony; auth off → system-tenant admin context), and checks every
 * outcome on the server over REST:
 *
 *   1. update: rename a prefix tenant in the info card, Save, and GET
 *      /v1/tenants/{id} returns the new name;
 *   2. refused update: ask for an isolation type this single-node server
 *      cannot provision; the server's error is shown in a toast and the
 *      tenant is unchanged on the server;
 *   3. delete: select the tenant row, Delete Selected, confirm the dialog;
 *      the row leaves the UI list and the server no longer has the tenant.
 */

import { execFileSync } from "node:child_process";
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

const LOAD_TIMEOUT = 15_000;

const TENANT_ID = "acme";
const TENANT_NAME = "Acme Corp";
const RENAMED = "Acme Holdings";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  // Tenant onboarding issues the first admin's certificate, which needs the
  // barrier's cert provider: boot a committed, unencrypted barrier and run
  // the first-boot ceremony ("qrdb init").
  daemon = await startDaemon({ seal: "none-committed" });
  await runFirstBoot(daemon);
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/**
 * Runs the first-boot ceremony over the sealed-window REST endpoint, which
 * brings the system barrier up. A refusal names the server's reason.
 */
async function runFirstBoot(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    const body = JSON.stringify({
      so_pin: "dom-tenant-so-pin",
      user_pin: "dom-tenant-user-pin",
      admin_generate: true,
    });
    const req = https.default.request(
      `${d.adminUrl}/v1/ceremony/initialize`,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(body) },
      },
      (res) => {
        let text = "";
        res.setEncoding("utf8");
        res.on("data", (chunk: string) => (text += chunk));
        res.on("end", () => {
          if (res.statusCode && res.statusCode < 300) resolve();
          else reject(new Error(`ceremony initialize returned ${res.statusCode}: ${text}`));
        });
      },
    );
    req.on("error", reject);
    req.end(body);
  });
}

/**
 * Returns the PEM of a fresh EC P-256 CSR for `cn`, made with openssl in a
 * temp dir that is removed before returning: the key is never kept.
 */
function generateCSR(cn: string): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  try {
    const keyPath = join(dir, "key.pem");
    const csrPath = join(dir, "csr.pem");
    execFileSync("openssl", ["ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", keyPath]);
    execFileSync("openssl", ["req", "-new", "-key", keyPath, "-out", csrPath, "-subj", `/CN=${cn}`]);
    return readFileSync(csrPath, "utf8").trim();
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** Expects a toast of the given kind whose text matches; returns it. */
async function expectToast(page: Page, kind: "success" | "error", text: string | RegExp) {
  const toast = page.locator(`.toast .alert-${kind}`).filter({ hasText: text }).last();
  try {
    await expect(toast).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const shown = await page.locator(".toast .alert").allInnerTexts();
    throw new Error(`expected ${kind} toast ${String(text)}; toasts: ${JSON.stringify(shown)}\n${err}`);
  }
  return toast;
}

/** Loads the app shell and opens the Tenants admin view. */
async function openTenantsView(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Tenants", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create Tenant" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Creates the prefix-isolation tenant with its first admin's CSR. */
async function createTenant(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create Tenant" }).click();
  await expect(page.getByRole("heading", { name: "Create Tenant" })).toBeVisible();
  await page.locator("#tenant-id").fill(TENANT_ID);
  await page.locator("#tenant-name").fill(TENANT_NAME);
  await expect(page.getByTestId("create-tenant-isolation-select")).toHaveValue("prefix");
  await page.getByTestId("create-tenant-admin-username").fill("acme-admin");
  await page.getByTestId("create-tenant-admin-kind-csr").click();
  await page.getByTestId("create-tenant-admin-csr").fill(generateCSR("acme-admin"));
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, "success", `Tenant "${TENANT_NAME}" created.`);
  await expect(page.getByTestId("create-tenant-admin-cert-result")).toHaveValue(
    /BEGIN CERTIFICATE/,
  );
  await page.getByTestId("create-tenant-admin-result-done").click();
}

/** Opens the tenant detail by clicking the list row showing `name`. */
async function openTenant(page: Page, name: string): Promise<void> {
  await page.getByText(name).first().click();
  await expect(page.getByRole("heading", { name: `Tenant Quotas — ${TENANT_ID}` })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** The info card's Name input. */
function nameInput(page: Page) {
  return page
    .locator("label.form-control")
    .filter({ has: page.getByText("Name", { exact: true }) })
    .locator("input");
}

/** GET /v1/tenants/{id} on the live daemon. */
async function serverTenant(request: APIRequestContext, adminUrl: string) {
  const resp = await request.get(`${adminUrl}/v1/tenants/${TENANT_ID}`);
  return { status: resp.status(), body: resp.ok() ? await resp.json() : null };
}

test("tenant rename round-trips through the server (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  const adminUrl = daemon!.adminUrl;
  await openTenantsView(page);
  await createTenant(page);
  await openTenant(page, TENANT_NAME);

  await expect(nameInput(page)).toHaveValue(TENANT_NAME);
  await nameInput(page).fill(RENAMED);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expectToast(page, "success", `Tenant "${TENANT_ID}" updated.`);
  // The form repopulates from the server's response.
  await expect(nameInput(page)).toHaveValue(RENAMED);

  const { status, body } = await serverTenant(request, adminUrl);
  expect(status).toBe(200);
  expect(body.id).toBe(TENANT_ID);
  expect(body.name).toBe(RENAMED);
  expect(body.isolation_type).toBe("prefix");

  // Back on the list the tenant shows its new name.
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await expect(page.getByText(RENAMED).first()).toBeVisible({ timeout: LOAD_TIMEOUT });
});

test("refused tenant update shows the server's error and changes nothing", async ({
  page,
  request,
}) => {
  const adminUrl = daemon!.adminUrl;
  await openTenantsView(page);
  await createTenant(page);
  await openTenant(page, TENANT_NAME);

  // A barrier-isolated tenant needs the system-managed barrier entitlement,
  // which a new tenant does not have: the server refuses the change.
  await page.getByTestId("tenant-isolation-select").selectOption("barrier");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const toast = await expectToast(page, "error", /PATCH|PUT|update|isolation/i);
  await expect(toast).not.toHaveText(/^\s*Failed to update tenant\s*$/);

  const { status, body } = await serverTenant(request, adminUrl);
  expect(status).toBe(200);
  expect(body.name).toBe(TENANT_NAME);
  expect(body.isolation_type).toBe("prefix");
});

test("tenant delete removes it from the server and the list (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  const adminUrl = daemon!.adminUrl;
  await openTenantsView(page);
  await createTenant(page);

  const row = page.getByRole("row").filter({ hasText: TENANT_ID });
  await expect(row).toHaveCount(1, { timeout: LOAD_TIMEOUT });
  await row.getByRole("checkbox").check();
  await page.getByRole("button", { name: /Delete Selected/ }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Delete", exact: true }).click();
  await expectToast(page, "success", "Deleted 1 tenant.");

  await expect(page.getByRole("row").filter({ hasText: TENANT_ID })).toHaveCount(0);

  const { status } = await serverTenant(request, adminUrl);
  expect(status).toBe(404);
  const list = await request.get(`${adminUrl}/v1/tenants`);
  expect(list.ok(), `GET /v1/tenants ${list.status()}: ${await list.text()}`).toBeTruthy();
  expect(JSON.stringify(await list.json())).not.toContain(`"${TENANT_ID}"`);
});
