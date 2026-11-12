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
// event opens in the detail panel. The fixture daemon wires no volume service
// (the volume-pool routes answer 501 "service not implemented: volume"), so
// the Volume Pools view is checked for its empty catalog state rather than a
// create/delete round-trip.

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

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
  await waitForHealthy(daemon);
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function openView(page: Page, nav: string, heading: string): Promise<void> {
  await page.getByRole("button", { name: nav, exact: true }).click();
  await expect(page.getByRole("heading", { name: heading, exact: true }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("a backup create appears in the audit log; volume pools show the empty catalog (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });

  // Volume Pools: no volume service on this daemon, so the catalog is empty.
  await openView(page, "Volume Pools", "Volume Pools");
  await expect(page.getByText("No volume pools in the catalog.")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("button", { name: "Create Pool" }).first()).toBeVisible();

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
