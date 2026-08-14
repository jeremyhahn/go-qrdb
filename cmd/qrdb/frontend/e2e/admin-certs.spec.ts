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
  secondLaunchEmbeddedState,
  injectError,
  clearError,
} from "./fixtures/wails-mock.ts";
import type { Page, Locator } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to admin mode and clicks the Certificates sidebar link. */
async function navigateToCerts(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Certificates").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Certificates"),
  ).toBeVisible();
}

/**
 * Returns a locator for the collapsible action panel body (card-body).
 * This distinguishes inner form submit buttons from the tab bar buttons.
 */
function panelBody(page: Page): Locator {
  return page.locator("main .card-body");
}

/**
 * Opens the Generate tab and fills in the username, then clicks the Generate
 * submit button (inside the panel body) and waits for the success toast.
 */
async function generateCert(
  page: Page,
  username: string,
): Promise<void> {
  const main = page.locator("main");

  // Click the Generate tab button in the action panel tab bar.
  await main.getByRole("button", { name: "Generate", exact: true }).first().click();

  // Fill username inside the panel body.
  const body = panelBody(page);
  await body.getByPlaceholder("e.g. alice").fill(username);

  // Click the Generate submit button (scoped to the panel body).
  await body.getByRole("button", { name: "Generate", exact: true }).click();

  const alert = page.locator('[role="alert"]');
  await expect(alert).toBeVisible({ timeout: 5_000 });
  await expect(alert.locator("span")).toContainText(`Certificate generated for "${username}"`);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Certificates", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("shows empty certificate list on load", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // The cert table shows an empty message when no certs have been issued.
    await expect(main.getByText("No certificates issued.")).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
  });

  test("generate certificate with valid inputs shows success toast and displays PEM", async ({
    page,
  }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Generate tab button in the action panel tab bar.
    const generateTabBtn = main.getByRole("button", { name: "Generate", exact: true }).first();
    await expect(generateTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await generateTabBtn.click();

    // Fill in only the username inside the panel body.
    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("alice");

    // Click the Generate submit button scoped to the panel body.
    await body.getByRole("button", { name: "Generate", exact: true }).click();

    // Toast should confirm success.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      'Certificate generated for "alice"',
    );

    // The generated PEM textareas should now be visible inside the panel body.
    await expect(body.getByText("Certificate PEM")).toBeVisible();
    await expect(body.getByText("Private Key PEM")).toBeVisible();

    // The certificate table should now list the new cert.
    await expect(main.getByText("alice")).toBeVisible();
  });

  test("generate certificate with empty username shows warning", async ({
    page,
  }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Generate tab button.
    await main.getByRole("button", { name: "Generate", exact: true }).first().click();

    // The submit button inside the panel body should be disabled when username is empty.
    const body = panelBody(page);
    await expect(body.getByRole("button", { name: "Generate", exact: true })).toBeDisabled();
  });

  test("generate button disabled when username is empty", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Generate tab button.
    await main.getByRole("button", { name: "Generate", exact: true }).first().click();

    const body = panelBody(page);
    const generateSubmitBtn = body.getByRole("button", { name: "Generate", exact: true });

    // Button should be disabled with empty username (default state).
    await expect(generateSubmitBtn).toBeDisabled();

    // Fill username → button becomes enabled.
    await body.getByPlaceholder("e.g. alice").fill("bob");
    await expect(generateSubmitBtn).toBeEnabled();

    // Clear username → button disabled again.
    await body.getByPlaceholder("e.g. alice").fill("");
    await expect(generateSubmitBtn).toBeDisabled();
  });

  test("revoke certificate flow: enter serial → click Revoke → confirm → success", async ({
    page,
  }) => {
    await navigateToCerts(page);

    // First generate a cert so there is something to revoke.
    await generateCert(page, "carol");

    const main = page.locator("main");

    // Click the Revoke tab button in the action panel tab bar.
    await main.getByRole("button", { name: "Revoke", exact: true }).first().click();

    const body = panelBody(page);

    // Enter the serial number (the mock assigns serials starting at 1001).
    await body
      .getByPlaceholder("e.g. 1a2b3c4d...")
      .fill("1001");

    // Click the Revoke submit button inside the panel body to show confirmation.
    await body.getByRole("button", { name: "Revoke", exact: true }).click();

    // Confirmation warning should appear.
    await expect(body.getByText("Are you sure?")).toBeVisible();
    await expect(
      body.getByRole("button", { name: "Confirm Revoke" }),
    ).toBeVisible();

    // Confirm revocation.
    await body.getByRole("button", { name: "Confirm Revoke" }).click();

    // Success toast should appear (use last() because the generate toast may still be visible).
    const alert = page.locator('[role="alert"]').last();
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("1001 revoked");

    // The serial input should be cleared after successful revocation.
    await expect(
      body.getByPlaceholder("e.g. 1a2b3c4d..."),
    ).toHaveValue("");
  });

  test("revoke cancel: enter serial → click Revoke → cancel → serial still present", async ({
    page,
  }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Revoke tab button.
    await main.getByRole("button", { name: "Revoke", exact: true }).first().click();

    const body = panelBody(page);

    await body
      .getByPlaceholder("e.g. 1a2b3c4d...")
      .fill("9999");

    // Click the Revoke submit button inside the panel body.
    await body.getByRole("button", { name: "Revoke", exact: true }).click();

    // Confirmation shows.
    await expect(
      body.getByRole("button", { name: "Confirm Revoke" }),
    ).toBeVisible();

    // Cancel.
    await body.getByRole("button", { name: "Cancel" }).click();

    // Confirmation should be gone, Revoke button should be back.
    await expect(
      body.getByRole("button", { name: "Confirm Revoke" }),
    ).not.toBeVisible();
    await expect(
      body.getByRole("button", { name: "Revoke", exact: true }),
    ).toBeVisible();

    // The serial input should still contain the value the user typed.
    await expect(
      body.getByPlaceholder("e.g. 1a2b3c4d..."),
    ).toHaveValue("9999");
  });

  test("revoke button disabled when serial is empty", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Revoke tab button.
    await main.getByRole("button", { name: "Revoke", exact: true }).first().click();

    const body = panelBody(page);

    // With empty serial the Revoke submit button should be disabled.
    await expect(
      body.getByRole("button", { name: "Revoke", exact: true }),
    ).toBeDisabled();
  });

  test("export PKCS#12 with valid inputs shows success", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button in the action panel tab bar.
    const exportTabBtn = main.getByRole("button", { name: "Export PKCS#12" }).first();
    await expect(exportTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await exportTabBtn.click();

    const body = panelBody(page);

    await body
      .getByPlaceholder("Paste certificate PEM here")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");
    await body
      .getByPlaceholder("Paste private key PEM here")
      .fill("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----");
    await body.getByPlaceholder("PKCS#12 passphrase").fill("s3cr3t");

    // Click the Export PKCS#12 submit button inside the panel body.
    await body.getByRole("button", { name: "Export PKCS#12" }).click();

    // Success toast.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "Certificate exported successfully",
    );

    // PKCS#12 output section should appear.
    await expect(
      body.getByText("PKCS#12 Data (Base64)"),
    ).toBeVisible();
  });

  test("export with missing cert PEM shows warning", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button.
    await main.getByRole("button", { name: "Export PKCS#12" }).first().click();

    const body = panelBody(page);

    // Leave cert PEM empty → export submit button should be disabled.
    await body
      .getByPlaceholder("Paste private key PEM here")
      .fill("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----");
    await body.getByPlaceholder("PKCS#12 passphrase").fill("s3cr3t");

    await expect(
      body.getByRole("button", { name: "Export PKCS#12" }),
    ).toBeDisabled();
  });

  test("export with missing key PEM shows warning", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button.
    await main.getByRole("button", { name: "Export PKCS#12" }).first().click();

    const body = panelBody(page);

    // Fill cert PEM but leave key PEM empty.
    await body
      .getByPlaceholder("Paste certificate PEM here")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");
    await body.getByPlaceholder("PKCS#12 passphrase").fill("s3cr3t");

    await expect(
      body.getByRole("button", { name: "Export PKCS#12" }),
    ).toBeDisabled();
  });

  test("export with missing passphrase shows warning", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button.
    await main.getByRole("button", { name: "Export PKCS#12" }).first().click();

    const body = panelBody(page);

    // Fill PEMs but leave passphrase empty.
    await body
      .getByPlaceholder("Paste certificate PEM here")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");
    await body
      .getByPlaceholder("Paste private key PEM here")
      .fill("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----");

    await expect(
      body.getByRole("button", { name: "Export PKCS#12" }),
    ).toBeDisabled();
  });

  test("export button disabled when any field is empty", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button.
    await main.getByRole("button", { name: "Export PKCS#12" }).first().click();

    const body = panelBody(page);
    const exportSubmitBtn = body.getByRole("button", { name: "Export PKCS#12" });

    // All fields empty initially.
    await expect(exportSubmitBtn).toBeDisabled();

    // Fill all three → button enabled.
    await body
      .getByPlaceholder("Paste certificate PEM here")
      .fill("cert");
    await body
      .getByPlaceholder("Paste private key PEM here")
      .fill("key");
    await body.getByPlaceholder("PKCS#12 passphrase").fill("pass");

    await expect(exportSubmitBtn).toBeEnabled();
  });

  test("generate error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    await injectError(page, "CertBinding.Generate", "certificate generation failed");

    const main = page.locator("main");

    // Click the Generate tab button.
    const generateTabBtn = main.getByRole("button", { name: "Generate", exact: true }).first();
    await expect(generateTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await generateTabBtn.click();

    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("alice");
    await body.getByRole("button", { name: "Generate", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "certificate generation failed",
    );

    await clearError(page, "CertBinding.Generate");
  });

  test("revoke error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    // Generate a cert first so the Revoke tab becomes available with data.
    await generateCert(page, "dave");

    await injectError(page, "CertBinding.Revoke", "revocation failed");

    const main = page.locator("main");

    // Click the Revoke tab button.
    await main.getByRole("button", { name: "Revoke", exact: true }).first().click();

    const body = panelBody(page);

    await body
      .getByPlaceholder("e.g. 1a2b3c4d...")
      .fill("1001");

    // Click Revoke submit button → then confirm.
    await body.getByRole("button", { name: "Revoke", exact: true }).click();
    await body.getByRole("button", { name: "Confirm Revoke" }).click();

    // Use last() because the generate toast may still be visible alongside the error toast.
    const alert = page.locator('[role="alert"]').last();
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("revocation failed");

    await clearError(page, "CertBinding.Revoke");
  });

  test("export error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    await injectError(page, "CertBinding.Export", "export failed");

    const main = page.locator("main");

    // Click the Export PKCS#12 tab button.
    const exportTabBtn = main.getByRole("button", { name: "Export PKCS#12" }).first();
    await expect(exportTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await exportTabBtn.click();

    const body = panelBody(page);

    await body
      .getByPlaceholder("Paste certificate PEM here")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");
    await body
      .getByPlaceholder("Paste private key PEM here")
      .fill("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----");
    await body.getByPlaceholder("PKCS#12 passphrase").fill("s3cr3t");

    await body.getByRole("button", { name: "Export PKCS#12" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("export failed");

    await clearError(page, "CertBinding.Export");
  });
});
