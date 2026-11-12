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
import type { Page } from "@playwright/test";

/** Timeout for async operations. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Navigates to the Seal Manager admin section. */
async function navigateToSealManager(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });


  await page.locator("aside").getByText("Seal Manager").click();
  await expect(
    page.locator(".breadcrumbs").getByText("Seal Manager"),
  ).toBeVisible();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Admin - Seal Manager", () => {
  test.describe("unsealed initialized state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 3,
          sharesProvided: 0,
          sharesTotal: 5,
        },
      },
    });

    test("shows current seal status as Unsealed", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible();
    });

    test("shows total shares count", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Total Shares" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // sharesTotal is 5.
      await expect(main.getByText("5", { exact: true })).toBeVisible();
    });

    test("hides single-admin Seal button and points to the holder quorum", async ({ page }) => {
      // sharesRequired > 0 marks this barrier as Shamir-protected. A single
      // admin cannot seal it (the server refuses BarrierSeal), and the page
      // takes no share: its holders seal it through a quorum from the CLI or
      // the desktop app.
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);
      const note = page.getByTestId("seal-quorum-note");
      await expect(note).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(note).toContainText("quorum of its share holders");
      await expect(note).toContainText("qrdb barrier quorum submit --op seal");
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
    });

    test("status error shows error toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await injectError(page, "SealBinding.Status", "seal status unavailable");

      await page.locator("aside").getByText("Seal Manager").click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(alert.locator("span")).toContainText(
        "seal status unavailable",
      );

      await clearError(page, "SealBinding.Status");
    });
  });

  test.describe("unsealed single-admin (non-shamir) state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
          strategy: "passphrase",
        },
      },
    });

    test("shows single-admin Seal button when unsealed", async ({ page }) => {
      // sharesRequired === 0 and a non-shamir strategy mark this barrier as
      // single-admin sealable, so the Seal button renders instead of the
      // holder-quorum note.
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
    });

    test("seal button shows confirmation dialog", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();

      // String matcher: the copy wraps across source lines.
      await expect(
        main.getByText("Are you sure? The server will reject all operations"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible();
    });

    test("seal sends the SO PIN entered in the confirmation", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await page.getByTestId("seal-so-pin").fill("SO123");
      await main.getByRole("button", { name: "Confirm Seal" }).click();

      await expect.poll(() => page.evaluate(() => {
        const w = window as unknown as { __QRDB_MOCK_STATE__: { sealSoPins?: string[] } };
        return w.__QRDB_MOCK_STATE__.sealSoPins ?? [];
      }), { timeout: LOAD_TIMEOUT }).toEqual(["SO123"]);
    });

    test("seal cancel does not seal", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await main.getByRole("button", { name: "Cancel", exact: true }).click();

      // The confirmation dialog dismisses and the Seal button returns;
      // the status badge still reads Unsealed.
      await expect(
        main.getByRole("button", { name: "Confirm Seal" }),
      ).toHaveCount(0);
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toBeVisible();
      await expect(
        main.locator(".badge-success").getByText("Unsealed"),
      ).toBeVisible();
    });

    test("seal error shows error toast", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await injectError(page, "SealBinding.Seal", "seal failed: barrier busy");

      await main.getByRole("button", { name: "Seal", exact: true }).click();
      await main.getByRole("button", { name: "Confirm Seal" }).click();

      const alert = page.locator('[role="alert"]').filter({
        hasText: "seal failed: barrier busy",
      });
      await expect(alert).toBeVisible({ timeout: 5_000 });

      await clearError(page, "SealBinding.Seal");
    });
  });

  test.describe("sealed initialized Shamir state", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: true,
          initialized: true,
          sharesRequired: 2,
          sharesProvided: 0,
          sharesTotal: 3,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 2,
        },
      },
    });

    test("shows seal status as Sealed in Seal Manager when sealed", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // In desktop mode, navigate to Seal Manager to see seal status.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      await expect(
        main.locator(".stat-title", { hasText: "Seal Status" }),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Status badge should show Sealed.
      await expect(
        main.locator(".badge-warning").getByText("Sealed"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });

    test("a sealed Shamir barrier points to the holder quorum and takes no share", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");
      const note = page.getByTestId("unseal-quorum-note");
      await expect(note).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(note).toContainText("2 of 3 must submit");
      await expect(note).toContainText("qrdb barrier quorum submit --op unseal");
      await expect(main.getByRole("button", { name: "Submit Share" })).toHaveCount(0);
    });
  });

  test.describe("unseal error handling", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: true,
          initialized: true,
          strategy: "passphrase",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
        unsealProgress: {
          sealed: true,
          sharesProvided: 0,
          sharesRequired: 2,
        },
      },
    });

    test("unseal error shows error toast", async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // Navigate to Seal Manager.
      await page.locator("aside").getByText("Seal Manager").click();
      await expect(
        page.locator(".breadcrumbs").getByText("Seal Manager"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const main = page.locator("main");

      await injectError(page, "SealBinding.Unseal", "unseal operation failed");

      await main.getByPlaceholder("Enter passphrase").fill("bad-passphrase");
      await main.getByRole("button", { name: "Unseal", exact: true }).click();

      const alert = page.locator('[role="alert"]');
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(alert.locator("span")).toContainText("unseal operation failed");

      await clearError(page, "SealBinding.Unseal");
    });
  });

  // -------------------------------------------------------------------------
  // Encryption not configured ("none" strategy)
  //
  // A "none"-strategy barrier performs no encryption at rest, so there is
  // nothing to seal. The Seal Manager must hide the Seal/Unseal/re-init
  // controls and present an "encryption not configured" panel instead.
  // Regression guard for the bug where a none/unconfigured barrier offered a
  // single-admin Seal button that flipped the server into a meaningless
  // sealed state.
  // -------------------------------------------------------------------------

  test.describe("not configured — committed 'none' barrier", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: true,
          strategy: "none",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
      },
    });

    test("shows 'Not configured' status and hides seal controls", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      // Seal-state stat reads "Not configured" rather than Sealed/Unsealed.
      await expect(page.getByTestId("seal-status-not-configured")).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      // The not-configured panel is shown.
      await expect(
        page.getByTestId("encryption-not-configured-card"),
      ).toBeVisible();

      // No Seal / Unseal controls (there is nothing to seal).
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);
      await expect(
        main.getByRole("button", { name: "Unseal", exact: true }),
      ).toHaveCount(0);
      await expect(page.getByTestId("seal-vote-input")).toHaveCount(0);
      // The re-initialize card is hidden for an unconfigured barrier.
      await expect(
        main.getByRole("button", { name: "Re-initialize" }),
      ).toHaveCount(0);
    });

    test("an initialized 'none' barrier shows the enable-encryption wizard, not an in-place form", async ({ page }) => {
      await navigateToSealManager(page);

      // A committed "none" barrier supports an online enable-encryption
      // migration, so the wizard is offered (strategy select + submit) rather
      // than the one-time in-place Configure Encryption button.
      await expect(
        page.getByTestId("enable-encryption-wizard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("enable-encryption-strategy")).toBeVisible();
      await expect(page.getByTestId("enable-encryption-submit")).toBeVisible();
      await expect(page.getByTestId("configure-encryption-btn")).toHaveCount(0);
    });
  });

  test.describe("not configured — uninitialized barrier", () => {
    test.use({
      mockState: {
        ...secondLaunchEmbeddedState,
        sealStatus: {
          sealed: false,
          initialized: false,
          strategy: "none",
          sharesRequired: 0,
          sharesProvided: 0,
          sharesTotal: 0,
        },
      },
    });

    test("offers in-place Configure Encryption with passphrase/shamir options", async ({ page }) => {
      await navigateToSealManager(page);

      const main = page.locator("main");

      await expect(
        page.getByTestId("encryption-not-configured-card"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      // No seal controls.
      await expect(
        main.getByRole("button", { name: "Seal", exact: true }),
      ).toHaveCount(0);

      // The Configure Encryption affordance is present; clicking it reveals the
      // strategy-selection form (passphrase by default, with a Shamir option).
      const configureBtn = page.getByTestId("configure-encryption-btn");
      await expect(configureBtn).toBeVisible();
      await configureBtn.click();

      await expect(page.getByTestId("configure-encryption-form")).toBeVisible();
      await expect(main.getByText("New Passphrase")).toBeVisible();
      await expect(main.getByText("Shamir Secret Sharing")).toBeVisible();
      await expect(
        page.getByTestId("configure-encryption-submit"),
      ).toBeVisible();
    });
  });
});

test.describe("Admin - Seal Manager: cluster seal, passphrase and ceremony", () => {
  const passphraseState = {
    ...secondLaunchEmbeddedState,
    sealStatus: {
      sealed: false,
      initialized: true,
      sharesRequired: 0,
      sharesProvided: 0,
      sharesTotal: 0,
      strategy: "passphrase",
    },
  };

  test.describe("cluster seal record", () => {
    test.use({
      mockState: {
        ...passphraseState,
        sealStatus: {
          ...passphraseState.sealStatus,
          clusterSeal: { active: true, sealed: true, epoch: 4, local: "held" },
        },
      },
    });

    test("clearing the cluster seal shows every record field", async ({ page }) => {
      await navigateToSealManager(page);
      const status = page.getByTestId("cluster-seal-status");
      await expect(status).toContainText("record sealed", { timeout: LOAD_TIMEOUT });
      await expect(status).toContainText("this node is held");

      await page.getByTestId("cluster-unseal-btn").click();

      await expect(status).toContainText("record clear (epoch 5)", { timeout: LOAD_TIMEOUT });
      await expect(status).toContainText("this node is serving");
      await expect(page.getByTestId("cluster-seal-principal")).toHaveText("admin (tenant system)");
      await expect(page.getByTestId("cluster-seal-node")).toHaveText("127.0.0.1:7000");
      await expect(page.getByTestId("cluster-seal-at")).toHaveText("2026-11-12T10:00:00Z");
      await expect(page.getByTestId("cluster-unseal-btn")).toHaveCount(0);
    });

    test("cluster unseal error shows error toast", async ({ page }) => {
      await navigateToSealManager(page);
      await expect(page.getByTestId("cluster-unseal-btn")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await injectError(page, "SealBinding.ClusterUnseal", "cluster seal clear refused");

      await page.getByTestId("cluster-unseal-btn").click();

      const alert = page.locator('[role="alert"]').filter({ hasText: "cluster seal clear refused" });
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(page.getByTestId("cluster-seal-status")).toContainText("record sealed");
    });

    test("no cluster migration in progress hides the migration section", async ({ page }) => {
      await navigateToSealManager(page);
      await expect(page.getByTestId("cluster-seal-status")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(page.getByTestId("cluster-migration-status")).toHaveCount(0);
    });
  });

  test.describe("cluster migration", () => {
    test.use({
      mockState: {
        ...passphraseState,
        sealStatus: {
          ...passphraseState.sealStatus,
          clusterSeal: { active: true, sealed: false, epoch: 2, local: "serving" },
          clusterMigration: {
            unconfirmed: true,
            target: "shamir",
            generation: 7,
            acked: 1,
            total: 3,
            nodes: [
              { nodeId: "10.0.0.1:7000", step: "serving", phase: "complete", attempts: 0 },
              { nodeId: "10.0.0.2:7000", step: "recycling", phase: "copying", attempts: 2, detail: "copy retry: peer busy" },
              { nodeId: "10.0.0.3:7000", step: "pending" },
            ],
          },
        },
      },
    });

    test("shows every cluster migration field", async ({ page }) => {
      await navigateToSealManager(page);
      const mig = page.getByTestId("cluster-migration-status");
      await expect(mig).toBeVisible({ timeout: LOAD_TIMEOUT });
      await expect(mig).toContainText("Cluster migration to shamir");
      await expect(page.getByTestId("cluster-migration-progress")).toHaveText("1 of 3");
      await expect(page.getByTestId("cluster-migration-target")).toHaveText("shamir");
      await expect(page.getByTestId("cluster-migration-generation")).toHaveText("7");
      await expect(page.getByTestId("cluster-migration-acked")).toHaveText("1");
      await expect(page.getByTestId("cluster-migration-total")).toHaveText("3");
      await expect(page.getByTestId("cluster-migration-unconfirmed")).toBeVisible();

      const rows = page.getByTestId("cluster-migration-node");
      await expect(rows).toHaveCount(3);
      await expect(rows.nth(0)).toContainText("10.0.0.1:7000");
      await expect(rows.nth(0)).toContainText("serving");
      await expect(rows.nth(0)).toContainText("complete");
      await expect(rows.nth(1)).toContainText("recycling");
      await expect(rows.nth(1)).toContainText("copying");
      await expect(rows.nth(1)).toContainText("2");
      await expect(rows.nth(1)).toContainText("copy retry: peer busy");
      await expect(rows.nth(2)).toContainText("pending");
    });
  });

  test.describe("change passphrase", () => {
    test.use({ mockState: passphraseState });

    test("changes the passphrase and clears the form", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("change-passphrase-card");
      await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });

      await card.getByTestId("cp-current").fill("old-passphrase");
      await card.getByTestId("cp-new").fill("new-passphrase-1");
      await card.getByTestId("cp-confirm").fill("new-passphrase-1");
      await card.getByTestId("cp-so-pin").fill("123456");
      await card.getByTestId("cp-submit").click();

      const alert = page.locator('[role="alert"]').filter({ hasText: "Passphrase changed" });
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(card.getByTestId("cp-current")).toHaveValue("");
      await expect(card.getByTestId("cp-new")).toHaveValue("");
      await expect(card.getByTestId("cp-so-pin")).toHaveValue("");
    });

    test("mismatched confirmation is refused before the call", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("change-passphrase-card");
      await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });

      await card.getByTestId("cp-current").fill("old-passphrase");
      await card.getByTestId("cp-new").fill("new-passphrase-1");
      await card.getByTestId("cp-confirm").fill("new-passphrase-2");
      await card.getByTestId("cp-submit").click();

      const alert = page.locator('[role="alert"]').filter({ hasText: "Passphrases do not match" });
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(card.getByTestId("cp-current")).toHaveValue("old-passphrase");
    });

    test("change passphrase error shows error toast", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("change-passphrase-card");
      await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });
      await injectError(page, "SealBinding.ChangePassphrase", "decryption failed: wrong passphrase");

      await card.getByTestId("cp-current").fill("wrong-passphrase");
      await card.getByTestId("cp-new").fill("new-passphrase-1");
      await card.getByTestId("cp-confirm").fill("new-passphrase-1");
      await card.getByTestId("cp-submit").click();

      const alert = page.locator('[role="alert"]').filter({ hasText: "decryption failed: wrong passphrase" });
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(card.getByTestId("cp-current")).toHaveValue("wrong-passphrase");
    });
  });

  test.describe("ceremony", () => {
    test.use({ mockState: passphraseState });

    test("shows the ceremony state and SPKI pin", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("ceremony-card");
      await expect(card.getByTestId("ceremony-state")).toHaveText("operational", { timeout: LOAD_TIMEOUT });
      await expect(card.getByTestId("ceremony-spki-pin")).toHaveText("mockpin");
    });

    test("ceremony status error is shown in the card", async ({ page }) => {
      await page.goto("/");
      await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await injectError(page, "CeremonyBinding.Status", "ceremony status unavailable");
      await page.locator("aside").getByText("Seal Manager").click();

      const card = page.getByTestId("ceremony-card");
      await expect(card.getByTestId("ceremony-error")).toContainText("ceremony status unavailable", {
        timeout: LOAD_TIMEOUT,
      });
      await expect(card.getByTestId("ceremony-state")).toHaveCount(0);

      await clearError(page, "CeremonyBinding.Status");
      await card.getByTestId("ceremony-refresh").click();
      await expect(card.getByTestId("ceremony-state")).toHaveText("operational", { timeout: LOAD_TIMEOUT });
    });

    test("signs a CSR and shows the certificate", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("ceremony-card");
      await expect(card.getByTestId("ceremony-state")).toBeVisible({ timeout: LOAD_TIMEOUT });

      await card.getByTestId("csr-username").fill("carol");
      await card.getByTestId("csr-so-pin").fill("123456");
      await card.getByTestId("csr-role").selectOption("custodian");
      await card
        .getByTestId("csr-pem")
        .fill("-----BEGIN CERTIFICATE REQUEST-----\nMIIB\n-----END CERTIFICATE REQUEST-----");
      await card.getByTestId("csr-sign-btn").click();

      const pem = card.getByTestId("csr-cert-pem");
      await expect(pem).toContainText("mock-signed-carol", { timeout: 5_000 });
      await expect(pem).toContainText("-----BEGIN CERTIFICATE-----");
      await expect(card.getByTestId("csr-cert-copy")).toBeVisible();
      await expect(card.getByTestId("csr-so-pin")).toHaveValue("");
    });

    test("sign CSR error shows error toast", async ({ page }) => {
      await navigateToSealManager(page);
      const card = page.getByTestId("ceremony-card");
      await expect(card.getByTestId("ceremony-state")).toBeVisible({ timeout: LOAD_TIMEOUT });
      await injectError(page, "CeremonyBinding.SignCSR", "invalid SO PIN");

      await card.getByTestId("csr-username").fill("carol");
      await card.getByTestId("csr-so-pin").fill("000000");
      await card.getByTestId("csr-pem").fill("-----BEGIN CERTIFICATE REQUEST-----\nMIIB\n-----END CERTIFICATE REQUEST-----");
      await card.getByTestId("csr-sign-btn").click();

      const alert = page.locator('[role="alert"]').filter({ hasText: "invalid SO PIN" });
      await expect(alert).toBeVisible({ timeout: 5_000 });
      await expect(card.getByTestId("csr-cert")).toHaveCount(0);
    });
  });
});
