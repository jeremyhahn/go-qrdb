// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

/** Mock state with raft server mode so the Rebalance nav item is visible. */
const rebalanceState = {
  ...secondLaunchEmbeddedState,
  serverMode: "raft" as const,
};

/** Navigate to the admin Rebalance view. */
async function navigateToRebalance(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Rebalance", { exact: true }).click();
  await expect(page.locator(".breadcrumbs").getByText("Rebalance")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

test.describe("Rebalance Panel", () => {
  test.use({ mockState: rebalanceState });

  test("loads and displays the imbalance report", async ({ page }) => {
    await navigateToRebalance(page);

    await expect(page.getByText("Imbalance Score")).toBeVisible();
    await expect(page.getByText("0.420")).toBeVisible();
    await expect(page.getByText("node-1")).toBeVisible();
    await expect(page.getByText("node-2")).toBeVisible();
  });

  test("shows hotspot warning when hotspots are present", async ({ page }) => {
    await navigateToRebalance(page);

    await expect(page.getByText(/Hotspots: 127\.0\.0\.1:9002/)).toBeVisible();
  });

  test("generate plan shows moves and enables execute", async ({ page }) => {
    await navigateToRebalance(page);

    await page.getByRole("button", { name: "Generate Plan" }).click();

    await expect(page.getByText(/Plan generated: 1 move/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("button", { name: "Execute Plan" })).toBeVisible();
  });

  test("execute plan shows the completion result", async ({ page }) => {
    await navigateToRebalance(page);

    await page.getByRole("button", { name: "Generate Plan" }).click();
    await expect(page.getByText(/Plan generated/)).toBeVisible({ timeout: 5_000 });

    await page.getByRole("button", { name: "Execute Plan" }).click();
    await expect(page.locator(".modal").getByRole("heading", { name: "Execute Rebalance Plan" })).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Execute" }).click();

    await expect(page.getByText(/Rebalance complete/)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("Last Execution Result")).toBeVisible();
  });

  test("error on report shows an error toast", async ({ page }) => {
    await navigateToRebalance(page);

    await injectError(page, "RebalanceBinding.GetImbalanceReport", "rebalance report unavailable");
    await page.getByRole("button", { name: "Refresh Report" }).click();

    await expect(page.getByText("rebalance report unavailable")).toBeVisible({ timeout: 5_000 });

    await clearError(page, "RebalanceBinding.GetImbalanceReport");
  });

  test("error on plan generation shows an error toast", async ({ page }) => {
    await navigateToRebalance(page);

    await injectError(page, "RebalanceBinding.CreatePlan", "rebalance plan failed: quorum unavailable");
    await page.getByRole("button", { name: "Generate Plan" }).click();

    await expect(page.getByText("rebalance plan failed: quorum unavailable")).toBeVisible({
      timeout: 5_000,
    });

    await clearError(page, "RebalanceBinding.CreatePlan");
  });

  test("cancel execute confirmation does not execute the plan", async ({ page }) => {
    await navigateToRebalance(page);

    await page.getByRole("button", { name: "Generate Plan" }).click();
    await expect(page.getByText(/Plan generated/)).toBeVisible({ timeout: 5_000 });

    await page.getByRole("button", { name: "Execute Plan" }).click();
    await expect(page.locator(".modal").getByRole("heading", { name: "Execute Rebalance Plan" })).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();

    await expect(page.getByText("Last Execution Result")).not.toBeVisible();
  });

  test("clicking a move row shows every shard stats field", async ({ page }) => {
    await navigateToRebalance(page);

    await page.getByRole("button", { name: "Generate Plan" }).click();
    await expect(page.getByText(/Plan generated/)).toBeVisible({ timeout: 5_000 });

    await page.getByRole("row", { name: /node-2\s+node-1/ }).click();

    const panel = page.getByTestId("shard-stats-panel");
    await expect(panel).toBeVisible({ timeout: 5_000 });
    await expect(panel.getByText("Shard 1 Statistics")).toBeVisible();
    await expect(panel.getByRole("row", { name: /Shard ID/ })).toContainText("1");
    await expect(panel.getByRole("row", { name: /Keys/ })).toContainText("1000");
    await expect(panel.getByRole("row", { name: /Bytes/ })).toContainText("1.0 MB (1048576)");
    await expect(panel.getByRole("row", { name: /Leader Replica/ })).toContainText("1");
    await expect(panel.getByRole("row", { name: /Replicas/ })).toContainText("3");

    await panel.getByRole("button", { name: "Close" }).click();
    await expect(panel).toHaveCount(0);
  });

  test("shard stats error shows the error state", async ({ page }) => {
    await navigateToRebalance(page);

    await page.getByRole("button", { name: "Generate Plan" }).click();
    await expect(page.getByText(/Plan generated/)).toBeVisible({ timeout: 5_000 });

    await injectError(page, "RebalanceBinding.GetShardStats", "shard 1 not found");
    await page.getByRole("row", { name: /node-2\s+node-1/ }).click();

    const errorBox = page.getByTestId("shard-stats-error");
    await expect(errorBox).toBeVisible({ timeout: 5_000 });
    await expect(errorBox).toContainText("shard 1 not found");

    await clearError(page, "RebalanceBinding.GetShardStats");
  });
});
