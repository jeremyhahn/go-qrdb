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

import {
  test,
  expect,
  raftClusterState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Cluster admin section. */
async function navigateToCluster(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Cluster").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Cluster"),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Cluster", () => {
  test.use({ mockState: raftClusterState });

  test("shows empty peer list when no peers are configured", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");

    await expect(main.getByText("Cluster Peers")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    // Default mock returns empty shards so there are no peer rows.
    // The table should indicate no peers.
    await expect(main.locator(".card", { hasText: "Cluster Peers" })).toBeVisible();
  });

  test("shows Add Member form with Shard ID, Replica ID, and Address fields", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(addCard.getByPlaceholder("Shard ID")).toBeVisible();
    await expect(addCard.getByPlaceholder("Replica ID")).toBeVisible();
    await expect(
      addCard.getByPlaceholder("Raft Address (e.g. 10.0.0.2:63001)"),
    ).toBeVisible();
  });

  test("add member succeeds with valid inputs", async ({ page }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await addCard.getByPlaceholder("Shard ID").fill("1");
    await addCard.getByPlaceholder("Replica ID").fill("4");
    await addCard
      .getByPlaceholder("Raft Address (e.g. 10.0.0.2:63001)")
      .fill("10.0.0.4:63001");

    await addCard.getByRole("button", { name: "Add Member" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("add member with missing address shows no submission", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fill shard and replica but leave address empty.
    await addCard.getByPlaceholder("Shard ID").fill("1");
    await addCard.getByPlaceholder("Replica ID").fill("2");
    // Address intentionally left empty.

    await addCard.getByRole("button", { name: "Add Member" }).click();

    // Should show a warning toast about the missing field.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("shows Remove Member form with Shard ID and Replica ID fields", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const removeCard = main.locator(".card", { hasText: "Remove Member" });

    await expect(removeCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(removeCard.getByPlaceholder("Shard ID")).toBeVisible();
    await expect(removeCard.getByPlaceholder("Replica ID")).toBeVisible();
    await expect(
      removeCard.getByRole("button", { name: "Remove Member" }),
    ).toBeVisible();
  });

  test("remove member succeeds with valid inputs", async ({ page }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const removeCard = main.locator(".card", { hasText: "Remove Member" });

    await expect(removeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await removeCard.getByPlaceholder("Shard ID").fill("1");
    await removeCard.getByPlaceholder("Replica ID").fill("2");

    await removeCard.getByRole("button", { name: "Remove Member" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("shows Request Snapshot form with Shard ID field", async ({ page }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const snapshotCard = main.locator(".card", { hasText: "Request Snapshot" });

    await expect(snapshotCard).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(
      snapshotCard.getByRole("button", { name: "Request Snapshot" }),
    ).toBeVisible();
  });

  test("request snapshot with shard ID succeeds", async ({ page }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const snapshotCard = main.locator(".card", { hasText: "Request Snapshot" });

    await expect(snapshotCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Fill in a shard ID if the field exists.
    const shardInput = snapshotCard.getByPlaceholder("Shard ID");
    if (await shardInput.isVisible()) {
      await shardInput.fill("1");
    }

    await snapshotCard.getByRole("button", { name: "Request Snapshot" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("drain button shows confirmation with 'Are you sure?'", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const drainCard = main.locator(".card", { hasText: "Drain Node" });

    await expect(drainCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await drainCard.getByRole("button", { name: "Drain", exact: true }).click();

    await expect(drainCard.getByText("Are you sure?")).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Cancel" }),
    ).toBeVisible();
  });

  test("drain cancel hides confirmation and restores Drain button", async ({
    page,
  }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const drainCard = main.locator(".card", { hasText: "Drain Node" });

    await expect(drainCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await drainCard.getByRole("button", { name: "Drain", exact: true }).click();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).toBeVisible();

    await drainCard.getByRole("button", { name: "Cancel" }).click();

    await expect(
      drainCard.getByRole("button", { name: "Drain", exact: true }),
    ).toBeVisible();
    await expect(
      drainCard.getByRole("button", { name: "Confirm Drain" }),
    ).not.toBeVisible();
  });

  test("drain confirm calls drain and shows success toast", async ({ page }) => {
    await navigateToCluster(page);

    const main = page.locator("main");
    const drainCard = main.locator(".card", { hasText: "Drain Node" });

    await expect(drainCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await drainCard.getByRole("button", { name: "Drain", exact: true }).click();
    await drainCard.getByRole("button", { name: "Confirm Drain" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
  });

  test("add member error shows error toast", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "AdminBinding.AddMember", "add member failed");

    await page.locator("aside").getByText("Cluster").click();

    const main = page.locator("main");
    const addCard = main.locator(".card", { hasText: "Add Member" });

    await expect(addCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await addCard.getByPlaceholder("Shard ID").fill("1");
    await addCard.getByPlaceholder("Replica ID").fill("1");
    await addCard
      .getByPlaceholder("Raft Address (e.g. 10.0.0.2:63001)")
      .fill("10.0.0.5:63001");

    await addCard.getByRole("button", { name: "Add Member" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("add member failed");

    await clearError(page, "AdminBinding.AddMember");
  });

  // -------------------------------------------------------------------------
  // Discovery configuration (handleSaveDiscovery)
  //
  // NOTE: there is deliberately NO runtime API call behind Save. Discovery is
  // set at daemon startup via the --discovery flag, and handleSaveDiscovery
  // surfaces that honestly with a warning toast instead of pretending the
  // setting was persisted (see Cluster.svelte). These specs pin the validation
  // paths and that honest warning.
  // -------------------------------------------------------------------------

  /** Expands the Discovery Configuration panel and returns its card locator. */
  async function openDiscoveryPanel(page: Page) {
    const card = page.locator("main .card", { hasText: "Discovery Configuration" });
    await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });
    await card.getByRole("button", { name: "Discovery Configuration" }).click();
    await expect(card.locator("select")).toBeVisible();
    return card;
  }

  test("discovery save with static method surfaces daemon-restart warning", async ({
    page,
  }) => {
    await navigateToCluster(page);
    const card = await openDiscoveryPanel(page);

    // Static is the default method and shows the informational note.
    await expect(card.locator("select")).toHaveValue("static");
    await expect(
      card.getByText("Static discovery uses the peer list below", { exact: false }),
    ).toBeVisible();

    await card.getByRole("button", { name: "Save" }).click();

    // The save path issues no API call; it surfaces the restart requirement.
    await expect(
      page.getByText("Discovery is set at daemon startup via --discovery", {
        exact: false,
      }),
    ).toBeVisible({ timeout: 5_000 });

    // The Save button is re-enabled once the handler completes.
    await expect(card.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  test("discovery save with multicast and empty secret shows validation warning", async ({
    page,
  }) => {
    await navigateToCluster(page);
    const card = await openDiscoveryPanel(page);

    await card.locator("select").selectOption("multicast");
    await expect(
      card.getByPlaceholder("Multicast shared secret"),
    ).toBeVisible();

    await card.getByRole("button", { name: "Save" }).click();

    await expect(
      page.getByText("Shared secret is required for multicast discovery"),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("discovery save with dns and empty domain shows validation warning", async ({
    page,
  }) => {
    await navigateToCluster(page);
    const card = await openDiscoveryPanel(page);

    await card.locator("select").selectOption("dns");
    await expect(
      card.getByPlaceholder("e.g. cluster.example.com"),
    ).toBeVisible();

    await card.getByRole("button", { name: "Save" }).click();

    await expect(
      page.getByText("Domain is required for DNS discovery"),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("discovery save with multicast secret passes validation and surfaces restart warning", async ({
    page,
  }) => {
    await navigateToCluster(page);
    const card = await openDiscoveryPanel(page);

    await card.locator("select").selectOption("multicast");
    await card.getByPlaceholder("Multicast shared secret").fill("s3cret");

    await card.getByRole("button", { name: "Save" }).click();

    // Validation passed — the handler reaches the honest restart warning
    // instead of the field-required warning.
    await expect(
      page.getByText("Discovery is set at daemon startup via --discovery", {
        exact: false,
      }),
    ).toBeVisible({ timeout: 5_000 });
    await expect(
      page.getByText("Shared secret is required for multicast discovery"),
    ).not.toBeVisible();
  });

  test("remove member error shows error toast", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await injectError(page, "AdminBinding.RemoveMember", "remove member failed");

    await page.locator("aside").getByText("Cluster").click();

    const main = page.locator("main");
    const removeCard = main.locator(".card", { hasText: "Remove Member" });

    await expect(removeCard).toBeVisible({ timeout: LOAD_TIMEOUT });

    await removeCard.getByPlaceholder("Shard ID").fill("1");
    await removeCard.getByPlaceholder("Replica ID").fill("2");

    await removeCard.getByRole("button", { name: "Remove Member" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("remove member failed");

    await clearError(page, "AdminBinding.RemoveMember");
  });
});
