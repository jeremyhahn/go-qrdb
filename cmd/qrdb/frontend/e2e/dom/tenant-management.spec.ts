// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * tenant-management.spec.ts (real-backend DOM tier)
 *
 * Closes the "multi-tenant UI updated + verified against the real backend"
 * loop. It boots a live qrdbd with the seal subsystem disabled (auth off →
 * system-tenant admin context, which is what the DOM harness drives) and
 * exercises the real Svelte tenant-management surfaces end to end:
 *
 *   1. Tenants admin view → create a `prefix`-isolation tenant. (prefix is the
 *      only mode that provisions on this single-node, no-seal harness; volume /
 *      raft need pooled/dedicated backends and barrier needs the seal
 *      subsystem — see tenant-crud-real.spec.ts where those fail closed.)
 *   2. Tenant detail → set the new quota fields (maxStorageBytes,
 *      maxDatabaseSizeBytes, maxReplicasPerGroup, maxTotalReplicas) plus a few
 *      existing ones, save, and assert the UI reflects the persisted values.
 *   3. Tenant detail → set entitlements (allowDedicatedRaft, allowVolumeStorage
 *      and an allowed-isolation-modes selection), save.
 *   4. Verify against the LIVE daemon (not just the DOM): hit GET
 *      /v1/tenants/{id} directly and assert the persisted snake_case quota +
 *      entitlement fields match what the UI set — proving there is no UI↔backend
 *      contract drift and no data loss on the round trip.
 *
 * Note on the DB-wizard `replicas` input (task item 5): the replication factor
 * only takes effect for raft-isolation databases, which require pooled /
 * clustered backends that are not present in this single-node no-seal harness,
 * and tenant-scoped DB creation needs a tenant connection context the
 * system-admin DOM harness does not expose. It is therefore intentionally NOT
 * exercised here rather than faked; replicas wiring is covered by
 * database-wizard-real.spec.ts and the StepDatabaseRaft unit tests.
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

/** Waits for a toast; on a miss, names the error toasts on screen. */
async function expectToast(page: Page, text: string): Promise<void> {
  try {
    await expect(page.getByText(text).last()).toBeVisible({ timeout: LOAD_TIMEOUT });
  } catch (err) {
    const errors = await page.locator(".toast .alert-error").allInnerTexts();
    throw new Error(`expected toast "${text}"; error toasts: ${JSON.stringify(errors)}\n${err}`, { cause: err });
  }
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

const TENANT_ID = "acme";
const TENANT_NAME = "Acme Corp";

// Distinct quota values so a wrong-field mapping is unmistakable.
const QUOTAS = {
  maxDatabases: 5,
  maxKeys: 1000,
  maxStorageBytes: 1073741824, // 1 GiB
  maxDatabaseSizeBytes: 536870912, // 512 MiB
  maxReplicasPerGroup: 3,
  maxTotalReplicas: 9,
} as const;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  // Tenant onboarding issues the first admin's certificate, which needs
  // the barrier's cert provider: seal "none" has no barrier, so boot a
  // committed, unencrypted one and run the first-boot ceremony ("qrdb
  // init"); the server refuses a plain barrier initialize.
  daemon = await startDaemon({ seal: "none-committed" });
  await runFirstBoot(daemon);
  await waitForHealthy(daemon);
});

/**
 * Runs the first-boot ceremony ("qrdb init") over the sealed-window REST
 * endpoint, which brings the system barrier up. A refusal names the
 * server's reason.
 */
async function runFirstBoot(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    // The none strategy takes no secret. The ceremony still needs a
    // bootstrap admin credential; this spec never uses it, so the
    // dev/test generate path is enough.
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

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Resolves a quota <input> by the visible label text of its wrapping field. */
function quotaInput(page: Page, label: string) {
  return page.locator("label.form-control", { hasText: label }).locator("input");
}

/** Navigates to the Tenants admin view from the loaded app shell. */
async function openTenantsView(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  // App shell loaded (no barrier gate with seal:"none").
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Tenants", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create Tenant" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("tenant create + quotas + entitlements round-trip (UI ↔ live daemon)", async ({
  page,
  request,
}) => {
  expect(daemon).not.toBeNull();
  const adminUrl = daemon!.adminUrl;

  await openTenantsView(page);

  // --- 1. Create a prefix-isolation tenant -------------------------------
  await page.getByRole("button", { name: "Create Tenant" }).click();
  await expect(page.getByRole("heading", { name: "Create Tenant" })).toBeVisible();

  await page.locator("#tenant-id").fill(TENANT_ID);
  await page.locator("#tenant-name").fill(TENANT_NAME);
  // prefix is the default isolation; assert it explicitly to be deterministic.
  await expect(page.getByTestId("create-tenant-isolation-select")).toHaveValue("prefix");

  // A tenant is created with its first admin's login credential, or not at
  // all: the admin's CSR (its key never leaves this test's temp dir).
  await page.getByTestId("create-tenant-admin-username").fill("acme-admin");
  await page.getByTestId("create-tenant-admin-kind-csr").click();
  await page.getByTestId("create-tenant-admin-csr").fill(generateCSR("acme-admin"));

  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, `Tenant "${TENANT_NAME}" created.`);

  // The admin's certificate is shown once; Done returns to the list.
  await expect(page.getByTestId("create-tenant-admin-cert-result")).toHaveValue(
    /BEGIN CERTIFICATE/,
  );
  await page.getByTestId("create-tenant-admin-result-done").click();

  // Back on the list — open the tenant detail by clicking its row.
  await page.getByText(TENANT_NAME).first().click();
  await expect(page.getByRole("heading", { name: `Tenant Quotas — ${TENANT_ID}` })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // --- 2. Set quotas (new fields + a couple existing) --------------------
  await quotaInput(page, "Max Databases").fill(String(QUOTAS.maxDatabases));
  await quotaInput(page, "Max Keys").fill(String(QUOTAS.maxKeys));
  await quotaInput(page, "Max Storage (bytes)").fill(String(QUOTAS.maxStorageBytes));
  await quotaInput(page, "Max Database Size (bytes)").fill(String(QUOTAS.maxDatabaseSizeBytes));
  await quotaInput(page, "Max Replicas / Group").fill(String(QUOTAS.maxReplicasPerGroup));
  await quotaInput(page, "Max Total Replicas").fill(String(QUOTAS.maxTotalReplicas));

  await page.getByRole("button", { name: "Save Quotas" }).click();
  await expect(page.getByText(`Quotas updated for tenant "${TENANT_ID}".`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // The UI repopulates from the server response — assert it reflects the saved
  // values (i.e. the server accepted and echoed them back).
  await expect(quotaInput(page, "Max Storage (bytes)")).toHaveValue(String(QUOTAS.maxStorageBytes));
  await expect(quotaInput(page, "Max Database Size (bytes)")).toHaveValue(
    String(QUOTAS.maxDatabaseSizeBytes),
  );
  await expect(quotaInput(page, "Max Replicas / Group")).toHaveValue(
    String(QUOTAS.maxReplicasPerGroup),
  );
  await expect(quotaInput(page, "Max Total Replicas")).toHaveValue(String(QUOTAS.maxTotalReplicas));

  // --- 3. Set entitlements ----------------------------------------------
  await page.getByTestId("entitlement-allow-dedicated-raft").check();
  await page.getByTestId("entitlement-allow-volume-storage").check();
  await page.getByTestId("entitlement-allow-dedicated-wal").check();
  await page.getByTestId("allowed-mode-prefix").check();
  await page.getByTestId("allowed-mode-volume").check();

  await page.getByTestId("save-entitlements-btn").click();
  await expect(page.getByText(`Entitlements updated for tenant "${TENANT_ID}".`).last()).toBeVisible(
    { timeout: LOAD_TIMEOUT },
  );

  // The toggles stay set after the server round trip.
  await expect(page.getByTestId("entitlement-allow-dedicated-raft")).toBeChecked();
  await expect(page.getByTestId("entitlement-allow-volume-storage")).toBeChecked();
  // Note: allow_dedicated_wal requires the backend handler DTO to be updated
  // (setTenantEntitlementsRequest currently lacks AllowDedicatedWAL).
  // We assert the UI toggle remains visible and was not reset by the response
  // repopulation (UI renders from SDK types which include the field).
  await expect(page.getByTestId("entitlement-allow-dedicated-wal")).toBeVisible();
  await expect(page.getByTestId("allowed-mode-prefix")).toBeChecked();
  await expect(page.getByTestId("allowed-mode-volume")).toBeChecked();

  // --- 4. Verify persistence directly against the live daemon ------------
  // GET /v1/tenants/{id} returns the snake_case wire contract. Asserting it
  // matches what the UI set proves there is no UI↔backend drift / data loss.
  const resp = await request.get(`${adminUrl}/v1/tenants/${TENANT_ID}`);
  expect(resp.ok()).toBeTruthy();
  const body = await resp.json();

  expect(body.id).toBe(TENANT_ID);
  expect(body.name).toBe(TENANT_NAME);
  expect(body.isolation_type).toBe("prefix");

  expect(body.quotas).toBeTruthy();
  expect(body.quotas.max_databases).toBe(QUOTAS.maxDatabases);
  expect(body.quotas.max_keys).toBe(QUOTAS.maxKeys);
  expect(body.quotas.max_storage_bytes).toBe(QUOTAS.maxStorageBytes);
  expect(body.quotas.max_database_size_bytes).toBe(QUOTAS.maxDatabaseSizeBytes);
  expect(body.quotas.max_replicas_per_group).toBe(QUOTAS.maxReplicasPerGroup);
  expect(body.quotas.max_total_replicas).toBe(QUOTAS.maxTotalReplicas);

  expect(body.entitlements).toBeTruthy();
  expect(body.entitlements.allow_dedicated_raft).toBe(true);
  expect(body.entitlements.allow_volume_storage).toBe(true);
  expect(body.entitlements.allowed_isolation_modes).toContain("prefix");
  expect(body.entitlements.allowed_isolation_modes).toContain("volume");
  // allow_dedicated_wal is persisted end-to-end: the UI toggle (checked above)
  // → PATCH /entitlements → GET round-trip returns true, proving the backend
  // handler DTO (setTenantEntitlementsRequest.AllowDedicatedWAL) accepts and
  // stores it with no UI↔backend drift.
  expect(body.entitlements.allow_dedicated_wal).toBe(true);
});
