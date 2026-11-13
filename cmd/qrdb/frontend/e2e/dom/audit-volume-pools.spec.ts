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

// Real-backend roundtrip for the Audit Log (AuditLog) and Volume Pools
// (VolumePoolManager) admin views against a live qrdbd. An admin action, a
// backup created through the Backups view, appears in the audit log, and its
// event opens in the detail panel. On a daemon with one configured "dir" pool
// over a committed no-seal barrier, the pool is created in the catalog,
// listed, read and updated in the Volume Pools view, a volume-isolation tenant
// is placed on it from the Tenants view, and the pool's detail lists that
// tenant; the server's state is checked after each step.

import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
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

let daemon: DomDaemon | null = null;

const SO_PIN = "volume-pools-roundtrip-so-pin-0123";
const USER_PIN = "volume-pools-roundtrip-user-pin-0123";

// The volume catalog (and with it the REST volume service) is built only over
// a barrier: under seal "none" every volume-pool route answers 501. The
// volume-pool test therefore boots a committed no-seal barrier with one
// configured "dir" pool and initializes it through the ceremony API.
test.beforeEach(async ({ request }, testInfo) => {
  if (!testInfo.title.startsWith("volume pool")) {
    daemon = await startDaemon({ seal: "none" });
    await waitForHealthy(daemon);
    return;
  }
  daemon = await startDaemon({ seal: "none-committed", volumePools: ["fast"] });
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

const POOL = "fast";
const TENANT_ID = "vol-tenant";

/** Generates a P-256 CSR for `cn` with openssl; the key is discarded. */
function makeCSR(cn: string): string {
  const dir = mkdtempSync(join(tmpdir(), "qrdb-csr-"));
  try {
    return execFileSync(
      "openssl",
      [
        "req", "-new",
        "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-keyout", join(dir, "key.pem"),
        "-subj", `/CN=${cn}`,
      ],
      { encoding: "utf8" },
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

async function expectToast(page: Page, text: string): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

async function openView(page: Page, nav: string, heading: string): Promise<void> {
  await page.getByRole("button", { name: nav, exact: true }).click();
  await expect(page.getByRole("heading", { name: heading, exact: true }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("volume pool: the configured pool is listed, read, updated and assigned to a tenant (UI ↔ live daemon)", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  const getJSON = async (path: string): Promise<{ status: number; body: Record<string, unknown> }> => {
    const r = await request.get(`${daemon!.adminUrl}${path}`, { ignoreHTTPSErrors: true });
    const text = await r.text();
    return { status: r.status(), body: text === "" ? {} : (JSON.parse(text) as Record<string, unknown>) };
  };

  // The node maps "fast" but the replicated catalog starts empty.
  await openView(page, "Volume Pools", "Volume Pools");
  await expect(page.getByText("No volume pools in the catalog.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  expect((await getJSON("/v1/volume-pools/list")).body.pools).toEqual([]);

  // Create: the catalog entry for the node's configured "dir" pool.
  await page.getByRole("button", { name: "Create Pool" }).first().click();
  await page.locator("#volume-pool-name").fill(POOL);
  await page.locator("#volume-pool-provider").fill("dir");
  await page.locator("#volume-pool-description").fill("fast tier");
  await page.getByTestId("volume-pool-create-submit").click();
  await expectToast(page, `Volume pool "${POOL}" created`);
  const created = await getJSON(`/v1/volume-pools/${POOL}`);
  expect(created.status).toBe(200);
  expect(created.body).toMatchObject({ name: POOL, provider: "dir", description: "fast tier", overcommit: 1 });

  // List and get: the pool's row opens its detail.
  const row = page.getByRole("row").filter({ hasText: POOL }).first();
  await expect(row).toBeVisible({ timeout: LOAD_TIMEOUT });
  await row.click();
  await expect(page.getByTestId("volume-pool-detail-name")).toHaveText(POOL, { timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("volume-pool-description")).toHaveText("fast tier");

  // Update: description and overcommit.
  await page.locator("#volume-pool-edit-description").fill("fast tier, updated");
  await page.locator("#volume-pool-edit-overcommit").fill("2");
  await page.getByTestId("volume-pool-save").click();
  await expectToast(page, `Volume pool "${POOL}" updated`);
  await expect(page.getByTestId("volume-pool-description")).toHaveText("fast tier, updated");
  await expect(page.getByTestId("volume-pool-overcommit")).toHaveText("2x");
  const updated = await getJSON(`/v1/volume-pools/${POOL}`);
  expect(updated.body).toMatchObject({ name: POOL, description: "fast tier, updated", overcommit: 2 });

  // Assign: a volume-isolation tenant created on the pool in the Tenants view.
  await page.getByRole("button", { name: "Tenants", exact: true }).click();
  await page.getByRole("button", { name: "Create Tenant" }).click();
  await expect(page.getByRole("heading", { name: "Create Tenant" })).toBeVisible();
  await page.locator("#tenant-id").fill(TENANT_ID);
  await page.locator("#tenant-name").fill(TENANT_ID);
  await page.getByTestId("create-tenant-isolation-select").selectOption("volume");
  // The pool field turns from free text into a picker once the catalog loads.
  const poolPicker = page.locator("select#tenant-volume-pool");
  await expect(poolPicker).toBeVisible({ timeout: LOAD_TIMEOUT });
  await poolPicker.selectOption(POOL);
  // Over a barrier a tenant is created with its first admin's credential.
  await page.getByRole("textbox", { name: "Admin Username" }).fill("vol-admin");
  await page.getByRole("textbox", { name: "Admin CSR PEM" }).fill(makeCSR("vol-admin"));
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expectToast(page, `Tenant "${TENANT_ID}" created.`);
  const poolTenants = await getJSON(`/v1/volume-pools/${POOL}/tenants`);
  expect(poolTenants.body.tenants).toEqual([expect.objectContaining({ tenantId: TENANT_ID })]);
  const tenantRecord = await getJSON(`/v1/tenants/${TENANT_ID}`);
  expect(tenantRecord.body).toMatchObject({ id: TENANT_ID, isolation_type: "volume", volume_pool: POOL });
  expect((await getJSON(`/v1/volume-pools/${POOL}`)).body).toMatchObject({ tenantCount: 1 });

  // listTenants: the pool's detail lists the assigned tenant.
  await openView(page, "Volume Pools", "Volume Pools");
  await page.getByRole("row").filter({ hasText: POOL }).first().click();
  await expect(page.getByTestId("volume-pool-tenants")).toContainText(TENANT_ID, { timeout: LOAD_TIMEOUT });

  expect(errors).toEqual([]);
});

test("a backup create appears in the audit log (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // An audited admin action: create a plaintext local backup.
  await openView(page, "Backups", "Backups");
  const destination = join(daemon!.dataDir, "ui-audit-backup");
  await page.getByRole("button", { name: "Create Backup" }).click();
  const modal = page.locator(".modal-box").filter({ hasText: "Create Backup" });
  await expect(modal).toBeVisible();
  await modal.getByPlaceholder("/var/backups/qrdb/backup-001 or s3://bucket/key").fill(destination);
  await modal.locator("select").nth(1).selectOption("none");
  await modal.getByRole("button", { name: "Create", exact: true }).click();
  const created = page.getByText(/Backup "[^"]+" created/).first();
  await expect(created).toBeVisible({ timeout: LOAD_TIMEOUT });
  const id = /Backup "([^"]+)" created/.exec((await created.textContent()) ?? "")?.[1] ?? "";
  expect(id).not.toBe("");

  // Audit Log: filtered to this backup, its create is listed (the server
  // records a "permitted" decision and then the "success" outcome).
  const resource = `backup:${id}`;
  await openView(page, "Audit Log", "Audit Log");
  await expect(page.getByRole("row").filter({ hasText: "posture-change" }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByPlaceholder("Filter by resource").fill(resource);
  await page.getByPlaceholder("Filter by action").fill("backup.create");
  await page.getByRole("button", { name: "Apply", exact: true }).click();
  await expect(page.getByRole("row").filter({ hasText: "posture-change" })).toHaveCount(0, {
    timeout: LOAD_TIMEOUT,
  });
  const events = page.getByRole("row").filter({ hasText: resource });
  await expect(events).toHaveCount(2, { timeout: LOAD_TIMEOUT });
  const success = events.filter({ hasText: "success" });
  await expect(success).toHaveCount(1);

  // Selecting the event opens its detail with the server's record.
  await success.click();
  await expect(page.getByRole("cell", { name: resource, exact: true }).last()).toBeVisible();
  await expect(page.getByRole("cell", { name: "backup.create", exact: true }).last()).toBeVisible();
  await expect(page.getByText(destination).last()).toBeVisible();

  expect(errors).toEqual([]);
});
