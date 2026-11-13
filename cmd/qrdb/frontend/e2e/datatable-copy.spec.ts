/*
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0
*/

/**
 * DataTable cell copy button vs row click (#1478).
 *
 * A cell's copy button copies the cell value and nothing else; a click
 * anywhere else on the cell, or Enter on a focused row, opens the row.
 * Driven through the KV Explorer, whose row click opens the "Details"
 * slide panel.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Locator, Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const copyKvState = {
  ...secondLaunchEmbeddedState,
  databases: [{ name: "copy-kv", backend: "pebble", status: "open" }],
};

async function openKvWithEntry(page: Page, key: string, value: string): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("copy-kv").click();
  await expect(
    page.locator(".breadcrumbs").getByText("copy-kv", { exact: false }),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });

  await page.getByRole("button", { name: "Create", exact: true }).first().click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page.locator("textarea[placeholder='Enter value (supports JSON)']").fill(value);
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({ timeout: 5_000 });
  await expect(page.getByText(`Created "${key}"`)).not.toBeVisible({ timeout: LOAD_TIMEOUT });
}

function keyCell(page: Page, key: string): Locator {
  return page
    .locator("table tbody td")
    .filter({ has: page.getByText(key, { exact: true }) })
    .first();
}

function detailsPanel(page: Page): Locator {
  return page.getByRole("dialog", { name: "Details" });
}

test.describe("DataTable cell copy button", () => {
  test.use({ mockState: copyKvState });

  test.beforeEach(async ({ context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  });

  test("clicking a cell's text opens the row", async ({ page }) => {
    await openKvWithEntry(page, "copy/a", "val-a");

    await keyCell(page, "copy/a").getByText("copy/a", { exact: true }).click();
    await expect(detailsPanel(page)).toBeVisible();
  });

  test("the copy button copies the value and does not open the row", async ({ page }) => {
    await openKvWithEntry(page, "copy/b", "val-b");
    const cell = keyCell(page, "copy/b");
    const copy = cell.getByTestId("cell-copy");

    // The button is hidden and takes no clicks until the cell is hovered.
    await expect(copy).toHaveCSS("opacity", "0");
    await expect(copy).toHaveCSS("pointer-events", "none");

    await cell.hover();
    await expect(copy).toHaveCSS("pointer-events", "auto");

    // Its hit area sits beside the text, never over it.
    const textBox = await cell.getByText("copy/b", { exact: true }).boundingBox();
    const copyBox = await copy.boundingBox();
    expect(textBox).not.toBeNull();
    expect(copyBox).not.toBeNull();
    expect(copyBox!.x).toBeGreaterThanOrEqual(textBox!.x + textBox!.width);

    await copy.click();
    await expect(page.getByText("Copied to clipboard").last()).toBeVisible();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("copy/b");
    await expect(detailsPanel(page)).not.toBeVisible();
  });

  test("Enter on a focused row opens it; Enter on the copy button only copies", async ({ page }) => {
    await openKvWithEntry(page, "copy/c", "val-c");

    const copy = keyCell(page, "copy/c").getByTestId("cell-copy");
    await copy.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText("Copied to clipboard").last()).toBeVisible();
    await expect(detailsPanel(page)).not.toBeVisible();

    const row = page.locator("table tbody tr").filter({ hasText: "copy/c" }).first();
    await row.focus();
    await page.keyboard.press("Enter");
    await expect(detailsPanel(page)).toBeVisible();
  });
});
