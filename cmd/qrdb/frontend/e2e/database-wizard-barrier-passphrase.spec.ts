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

/**
 * Database creation wizard — barrier passphrase.
 *
 * The passphrase barrier collects a passphrase and its confirmation and sends
 * them to DatabaseBinding.Create as `barrier: { enabled: true, mode:
 * "passphrase", secret }`. The passphrase is write-only on the client too:
 * the summary says it is set, never what it is, and no page shows it.
 *
 * Flow on a standalone server: Name → Storage → Protocols → Barrier →
 * Summary → Create.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;
const PASSPHRASE = "wizard-barrier-passphrase-7c2e";

/** Records every DatabaseBinding.Create request; optionally fails the call. */
async function captureCreates(page: Page, fail: boolean): Promise<void> {
  await page.addInitScript((failCreate: boolean) => {
    const w = window as unknown as Record<string, unknown>;
    w.__capturedDbCreateRequests = [];
    const db = (
      w.go as Record<string, Record<string, Record<string, unknown>>>
    )?.main?.DatabaseBinding;
    if (db && typeof db.Create === "function") {
      const orig = db.Create as (...args: unknown[]) => Promise<unknown>;
      db.Create = async (...args: unknown[]): Promise<unknown> => {
        (w.__capturedDbCreateRequests as unknown[]).push(
          JSON.parse(JSON.stringify(args[0])),
        );
        if (failCreate) {
          throw new Error("database: barrier refused for this database");
        }
        return orig(...args);
      };
    }
  }, fail);
}

async function capturedCreates(page: Page): Promise<Record<string, unknown>[]> {
  await page.waitForFunction(
    () =>
      ((window as unknown as Record<string, unknown>)
        .__capturedDbCreateRequests as unknown[]).length > 0,
    { timeout: STEP_TIMEOUT },
  );
  return page.evaluate(
    () =>
      (window as unknown as Record<string, unknown>)
        .__capturedDbCreateRequests as Record<string, unknown>[],
  );
}

/**
 * Records every console message the page writes, so a test can assert the
 * passphrase never reaches one.
 */
function captureConsole(page: Page): string[] {
  const messages: string[] = [];
  page.on("console", (msg) => {
    messages.push(msg.text());
  });
  return messages;
}

/** Fails when any console message holds the passphrase. */
function expectNoPassphraseLogged(messages: readonly string[]): void {
  expect(messages.length).toBeGreaterThan(0);
  for (const m of messages) {
    expect(m).not.toContain(PASSPHRASE);
  }
}

/** Opens the wizard and walks to the Barrier step. */
async function toBarrierStep(page: Page, dbName: string): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await page.locator("#db-name").fill(dbName);
  const next = page.locator("main").getByRole("button", { name: "Next" });
  await next.click();
  await expect(page.getByRole("heading", { name: "Storage Backend" })).toBeVisible({
    timeout: STEP_TIMEOUT,
  });
  await page.locator("#data-dir-input").fill("/tmp/qrdb-wizard-barrier");
  await next.click();
  await expect(
    page.getByRole("heading", { name: /SQL Wire Protocols|Protocols/i }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await next.click();
  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/** Selects the passphrase barrier and enters a confirmed passphrase. */
async function choosePassphrase(page: Page): Promise<void> {
  await page.locator("button").filter({ hasText: "Passphrase" }).first().click();
  await page.locator("#db-barrier-passphrase").fill(PASSPHRASE);
  await page.locator("#db-barrier-passphrase-confirm").fill(PASSPHRASE);
}

test.describe("Database Wizard - barrier passphrase", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("the barrier defaults to none, and Next needs no passphrase", async ({ page }) => {
    await toBarrierStep(page, "barrier-default-db");
    await expect(page.locator("input[name='db-seal-mode'][value='none']")).toBeChecked();
    await expect(page.locator("#db-barrier-passphrase")).toHaveCount(0);
    await expect(page.locator("main").getByRole("button", { name: "Next" })).toBeEnabled();
  });

  test("the passphrase barrier needs a passphrase and a matching confirmation", async ({
    page,
  }) => {
    await toBarrierStep(page, "barrier-validate-db");
    const next = page.locator("main").getByRole("button", { name: "Next" });
    await page.locator("button").filter({ hasText: "Passphrase" }).first().click();

    await expect(page.locator("#db-barrier-passphrase")).toHaveAttribute("type", "password");
    await expect(page.locator("#db-barrier-passphrase-confirm")).toHaveAttribute("type", "password");
    await expect(next).toBeDisabled();

    await page.locator("#db-barrier-passphrase").fill(PASSPHRASE);
    await page.locator("#db-barrier-passphrase-confirm").fill(PASSPHRASE + "-typo");
    await expect(page.getByTestId("db-barrier-passphrase-error")).toHaveText(
      "Passphrases do not match.",
    );
    await expect(next).toBeDisabled();

    await page.locator("#db-barrier-passphrase-confirm").fill(PASSPHRASE);
    await expect(page.getByTestId("db-barrier-passphrase-error")).toHaveText("");
    await expect(next).toBeEnabled();
  });

  test("create sends enabled: true with the passphrase, and the passphrase is never shown", async ({
    page,
  }) => {
    await captureCreates(page, false);
    const logged = captureConsole(page);
    await toBarrierStep(page, "barrier-create-db");
    await choosePassphrase(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();

    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
    await expect(page.getByTestId("db-summary-passphrase")).toHaveText("Set");
    await expect(page.locator("body")).not.toContainText(PASSPHRASE);

    await page.locator("main").getByRole("button", { name: "Create Database" }).click();
    const captured = await capturedCreates(page);
    expect(captured).toHaveLength(1);
    expect(captured[0].barrier).toEqual({
      enabled: true,
      mode: "passphrase",
      secret: PASSPHRASE,
    });
    expect(captured[0].dataDir).toBe("/tmp/qrdb-wizard-barrier");

    // The wizard hands off to the new database once created.
    await expect(page.getByRole("heading", { name: "Creating Database" })).toHaveCount(0, {
      timeout: STEP_TIMEOUT,
    });
    await expect(page.locator("body")).not.toContainText(PASSPHRASE);
    expectNoPassphraseLogged(logged);
  });

  test("a failed create does not show the passphrase", async ({ page }) => {
    await captureCreates(page, true);
    const logged = captureConsole(page);
    await toBarrierStep(page, "barrier-fail-db");
    await choosePassphrase(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await page.locator("main").getByRole("button", { name: "Create Database" }).click();

    await expect(page.getByText(/barrier refused for this database/)).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
    const captured = await capturedCreates(page);
    expect((captured[0].barrier as Record<string, unknown>).secret).toBe(PASSPHRASE);
    await expect(page.locator("body")).not.toContainText(PASSPHRASE);
    expectNoPassphraseLogged(logged);
  });

  test("going back to the barrier step asks for the passphrase again", async ({ page }) => {
    await toBarrierStep(page, "barrier-back-db");
    await choosePassphrase(page);
    await page.locator("main").getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("heading", { name: "Review Configuration" })).toBeVisible({
      timeout: STEP_TIMEOUT,
    });
    await page.locator("main").getByRole("button", { name: "Back" }).click();

    await expect(page.locator("#db-barrier-passphrase")).toHaveValue("");
    await expect(page.locator("#db-barrier-passphrase-confirm")).toHaveValue("");
    await expect(page.locator("main").getByRole("button", { name: "Next" })).toBeDisabled();
  });
});
