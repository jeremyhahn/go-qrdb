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

// Real-backend roundtrip for the Backups admin view (BackupManager):
// create a backup through the Create Backup modal against a live qrdbd,
// see it in the list, open its detail (served by backups.get), verify it,
// and delete it through the confirm modal. Restore is not driven: it
// overwrites the running daemon's own shard data, which the single-node
// fixture then serves to every later step.

import { join } from "node:path";
import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function openBackupsView(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.getByRole("button", { name: "Backups", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create Backup" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("backup create + list + detail + verify + delete round-trip (UI ↔ live daemon)", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await openBackupsView(page);
  await expect(page.getByText("No backups found.")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Create: a plaintext local backup (the fixture configures no key
  // directory, so an aes256 backup has no key file to resolve).
  const destination = join(daemon!.dataDir, "ui-backup-001");
  await page.getByRole("button", { name: "Create Backup" }).click();
  const modal = page.locator(".modal-box").filter({ hasText: "Create Backup" });
  await expect(modal).toBeVisible();
  await modal.getByPlaceholder("/var/backups/qrdb/backup-001 or s3://bucket/key").fill(destination);
  await modal.locator("select").nth(1).selectOption("none");
  await expect(modal.getByText("This backup will be written in plaintext.")).toBeVisible();
  await modal.getByRole("button", { name: "Create", exact: true }).click();

  const created = page.getByText(/Backup "[^"]+" created/).first();
  await expect(created).toBeVisible({ timeout: LOAD_TIMEOUT });
  const id = /Backup "([^"]+)" created/.exec((await created.textContent()) ?? "")?.[1] ?? "";
  expect(id).not.toBe("");

  // List: the new backup is a row.
  const row = page.getByRole("row").filter({ hasText: id });
  await expect(row).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Detail: the panel shows the server's record for this backup.
  await row.click();
  const detail = page.getByTestId("backup-detail");
  await expect(detail).toContainText(id);
  await expect(detail).toContainText(destination, { timeout: LOAD_TIMEOUT });
  await expect(page.getByTestId("backup-detail-error")).toHaveCount(0);

  // Verify: the checksum round-trips.
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByText("Checksum verified.")).toBeVisible({ timeout: LOAD_TIMEOUT });

  // Delete through the confirm modal; the list is empty again.
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const confirm = page.locator(".modal-box").filter({ hasText: "Delete Backup" });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByText(`Backup "${id}" deleted.`)).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByRole("row").filter({ hasText: id })).toHaveCount(0);
  await expect(page.getByText("Select a backup to view details.")).toBeVisible();

  expect(errors).toEqual([]);
});
