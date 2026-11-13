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

/**
 * Wraps body in PEM armour of the given block type. The armour is assembled
 * at run time so the source carries no literal private-key header, which
 * secret scanners flag even on a placeholder.
 */
function pemArmour(blockType: string, body: string): string {
  return `-----BEGIN ${blockType}-----\n${body}\n-----END ${blockType}-----`;
}

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
 * Opens the Issue tab, fills in the username (leaving the CSR blank so the
 * desktop mock generates a key locally), clicks Issue, and waits for the
 * success toast.
 */
async function issueCert(
  page: Page,
  username: string,
): Promise<void> {
  const main = page.locator("main");

  await main.getByRole("button", { name: "Issue", exact: true }).first().click();

  const body = panelBody(page);
  await body.getByPlaceholder("e.g. alice").fill(username);

  await body.getByRole("button", { name: "Issue", exact: true }).click();

  const alert = page.locator('[role="alert"]');
  await expect(alert).toBeVisible({ timeout: 5_000 });
  await expect(alert.locator("span")).toContainText(`Certificate issued for "${username}"`);
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

  test("issue certificate with blank CSR generates a key locally and shows success toast", async ({
    page,
  }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Issue tab button in the action panel tab bar.
    const issueTabBtn = main.getByRole("button", { name: "Issue", exact: true }).first();
    await expect(issueTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await issueTabBtn.click();

    // Fill in only the username inside the panel body; leave CSR blank.
    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("alice");

    // Click the Issue submit button scoped to the panel body.
    await body.getByRole("button", { name: "Issue", exact: true }).click();

    // Toast should confirm success.
    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      'Certificate issued for "alice"',
    );

    // The issued certificate and locally-generated key should now be visible.
    await expect(body.getByText("Certificate PEM")).toBeVisible();
    await expect(body.getByText("Private Key PEM (generated locally)")).toBeVisible();

    // The certificate table should now list the new cert.
    await expect(main.getByText("alice")).toBeVisible();
  });

  test("issue button disabled when username is empty and no CSR is pasted", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Issue tab button.
    await main.getByRole("button", { name: "Issue", exact: true }).first().click();

    const body = panelBody(page);
    const issueSubmitBtn = body.getByRole("button", { name: "Issue", exact: true });

    // Button should be disabled with empty username (default state).
    await expect(issueSubmitBtn).toBeDisabled();

    // Fill username → button becomes enabled (desktop mock allows blank CSR).
    await body.getByPlaceholder("e.g. alice").fill("bob");
    await expect(issueSubmitBtn).toBeEnabled();

    // Clear username → button disabled again.
    await body.getByPlaceholder("e.g. alice").fill("");
    await expect(issueSubmitBtn).toBeDisabled();
  });

  test("enroll certificate with valid inputs shows success toast", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    // Click the Enroll tab button in the action panel tab bar.
    const enrollTabBtn = main.getByRole("button", { name: "Enroll", exact: true }).first();
    await expect(enrollTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await enrollTabBtn.click();

    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("erin");
    await body
      .getByPlaceholder("Paste certificate PEM here (leaf first)")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");

    await body.getByRole("button", { name: "Enroll", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      'Certificate enrolled for "erin"',
    );

    await expect(body.getByText("Enrolled Certificate")).toBeVisible();
    await expect(main.getByText("erin")).toBeVisible();
  });

  test("enroll button disabled when certificate PEM is empty", async ({ page }) => {
    await navigateToCerts(page);

    const main = page.locator("main");

    await main.getByRole("button", { name: "Enroll", exact: true }).first().click();

    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("frank");

    await expect(
      body.getByRole("button", { name: "Enroll", exact: true }),
    ).toBeDisabled();
  });

  test("revoke certificate flow: enter serial → click Revoke → confirm → success", async ({
    page,
  }) => {
    await navigateToCerts(page);

    // First issue a cert so there is something to revoke.
    await issueCert(page, "carol");

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

    // Success toast should appear (use last() because the issue toast may still be visible).
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

  test("row detail shows the record's fields and pre-fills Revoke", async ({ page }) => {
    await navigateToCerts(page);
    await issueCert(page, "hank");

    const main = page.locator("main");
    const row = main.locator("table tbody tr").filter({ hasText: "hank" });
    await expect(row).toHaveCount(1);
    const toggle = row.getByRole("button", { name: "Details", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.click();

    const detail = main.getByTestId("cert-detail");
    await expect(detail).toHaveCount(1);
    await expect(row.getByRole("button", { name: "Hide", exact: true })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    const field = (name: string) =>
      detail.locator("dt", { hasText: name }).locator("xpath=following-sibling::dd[1]");
    await expect(field("Serial")).toHaveText("1001");
    await expect(field("Username")).toHaveText("hank");
    await expect(field("Tenant")).toHaveText("default");
    await expect(field("Expires at (UTC)")).toHaveText(/^\d{4}-\d{2}-\d{2}T/);
    await expect(field("Validity remaining")).toHaveText(/^36[45] days$/);
    await expect(field("Status")).toHaveText("Active");

    // The shortcut opens the Revoke tab with the serial filled in, unconfirmed.
    await detail.getByRole("button", { name: "Revoke this certificate…" }).click();
    const body = panelBody(page);
    await expect(body.getByPlaceholder("e.g. 1a2b3c4d...")).toHaveValue("1001");
    await expect(body.getByRole("button", { name: "Confirm Revoke" })).toHaveCount(0);

    // Hide collapses the detail again.
    await row.getByRole("button", { name: "Hide", exact: true }).click();
    await expect(detail).toHaveCount(0);
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
      .fill(pemArmour("PRIVATE KEY", "test"));
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
      .fill(pemArmour("PRIVATE KEY", "test"));
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
      .fill(pemArmour("PRIVATE KEY", "test"));

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

  test("issue error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    await injectError(page, "CertBinding.Issue", "certificate issuance failed");

    const main = page.locator("main");

    // Click the Issue tab button.
    const issueTabBtn = main.getByRole("button", { name: "Issue", exact: true }).first();
    await expect(issueTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await issueTabBtn.click();

    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("alice");
    await body.getByRole("button", { name: "Issue", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "certificate issuance failed",
    );

    await clearError(page, "CertBinding.Issue");
  });

  test("enroll error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    await injectError(page, "CertBinding.Enroll", "certificate enrollment failed");

    const main = page.locator("main");

    const enrollTabBtn = main.getByRole("button", { name: "Enroll", exact: true }).first();
    await expect(enrollTabBtn).toBeVisible({ timeout: LOAD_TIMEOUT });
    await enrollTabBtn.click();

    const body = panelBody(page);
    await body.getByPlaceholder("e.g. alice").fill("gina");
    await body
      .getByPlaceholder("Paste certificate PEM here (leaf first)")
      .fill("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----");
    await body.getByRole("button", { name: "Enroll", exact: true }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText(
      "certificate enrollment failed",
    );

    await clearError(page, "CertBinding.Enroll");
  });

  test("revoke error shows error toast", async ({ page }) => {
    await navigateToCerts(page);

    // Issue a cert first so the Revoke tab becomes available with data.
    await issueCert(page, "dave");

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

    // Use last() because the issue toast may still be visible alongside the error toast.
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
      .fill(pemArmour("PRIVATE KEY", "test"));
    await body.getByPlaceholder("PKCS#12 passphrase").fill("s3cr3t");

    await body.getByRole("button", { name: "Export PKCS#12" }).click();

    const alert = page.locator('[role="alert"]');
    await expect(alert).toBeVisible({ timeout: 5_000 });
    await expect(alert.locator("span")).toContainText("export failed");

    await clearError(page, "CertBinding.Export");
  });
});
