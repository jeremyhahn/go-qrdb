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
 * E2E tests for the invitation operations: invitations.invite from the user
 * manager (InviteUserModal) and invitations.redeem from the connection
 * manager (RedeemInvitationModal), against the mocked InvitationBinding.
 */

import {
  test,
  expect,
  defaultMockState,
  secondLaunchEmbeddedState,
  injectError,
} from "./fixtures/wails-mock.ts";
import { waitForConnectionManager } from "./fixtures/embedded-wizard-steps.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const CSR_PEM =
  "-----BEGIN CERTIFICATE REQUEST-----\nMOCKCSR\n-----END CERTIFICATE REQUEST-----";

/** Navigates to admin mode and opens the Users view. */
async function navigateToUsers(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Users").click();
  await expect(page.locator(".breadcrumbs").getByText("Users")).toBeVisible();
}

/** Issues an invitation through the mock binding and returns its token. */
async function issueInvitation(page: Page, username: string): Promise<string> {
  return page.evaluate((u: string) => {
    const w = window as unknown as {
      go: {
        main: {
          InvitationBinding: {
            Invite: (req: { username: string }) => Promise<{ token: string }>;
          };
        };
      };
    };
    return w.go.main.InvitationBinding.Invite({ username: u }).then((r) => r.token);
  }, username);
}

/** Opens the redeem modal from the empty connection manager and fills it. */
async function openAndFillRedeem(page: Page, token: string): Promise<void> {
  await page.getByRole("button", { name: "Redeem Invitation" }).click();
  await expect(
    page.getByRole("heading", { name: "Redeem Invitation" }),
  ).toBeVisible();
  await page.locator("#redeem-server").fill("qrdb.example.com:8443");
  await page.locator("#redeem-transport").selectOption("grpc");
  await page.locator("#redeem-ca-file").fill("/tmp/ca.pem");
  await page.locator("#redeem-tenant").fill("default");
  await page.locator("#redeem-token").fill(token);
  await page.locator("#redeem-csr").fill(CSR_PEM);
}

test.describe("Admin - Invitations", () => {
  test.describe("Invite", () => {
    test.use({ mockState: secondLaunchEmbeddedState });

    test("invite from the user manager shows the one-time token", async ({ page }) => {
      await navigateToUsers(page);
      await page.getByRole("button", { name: "Invite User" }).click();
      await page.locator("#invite-username").fill("carol");
      await page.locator("#invite-email").fill("carol@acme.com");
      await page.getByRole("button", { name: "Invite", exact: true }).click();

      await expect(
        page.getByRole("heading", { name: "Invitation created" }),
      ).toBeVisible();
      // Svelte sets the value as a property, not an attribute.
      const tokenInput = page.locator("input[readonly].font-mono");
      await expect(tokenInput).toHaveValue(/inv-token-\d+-carol/);
    });

    test("invite error shows a toast and keeps the form", async ({ page }) => {
      await navigateToUsers(page);
      await injectError(page, "InvitationBinding.Invite", "username already exists");
      await page.getByRole("button", { name: "Invite User" }).click();
      await page.locator("#invite-username").fill("dave");
      await page.getByRole("button", { name: "Invite", exact: true }).click();

      await expect(page.locator('[role="alert"]').last()).toContainText(
        "username already exists",
      );
      await expect(page.locator("#invite-username")).toBeVisible();
    });
  });

  test.describe("Redeem", () => {
    test.use({ mockState: defaultMockState });

    test("redeem with an issued token returns the certificate", async ({ page }) => {
      await page.goto("/");
      await waitForConnectionManager(page);
      const token = await issueInvitation(page, "erin");

      await openAndFillRedeem(page, token);
      await page.getByRole("button", { name: "Redeem", exact: true }).click();

      await expect(
        page.getByRole("heading", { name: "Invitation redeemed" }),
      ).toBeVisible();
      await expect(page.getByTestId("redeem-username")).toHaveText("erin");
      await expect(page.getByTestId("redeem-user-id")).toHaveText(/user-inv-\d+/);
      await expect(page.getByTestId("redeem-cert-pem")).toHaveValue(
        /BEGIN CERTIFICATE[\s\S]*MOCK-erin/,
      );
      await expect(
        page.getByRole("button", { name: "Copy certificate" }),
      ).toBeVisible();
    });

    test("redeem of an unknown token shows the error in the modal", async ({ page }) => {
      await page.goto("/");
      await waitForConnectionManager(page);

      await openAndFillRedeem(page, "not-an-issued-token");
      await page.getByRole("button", { name: "Redeem", exact: true }).click();

      await expect(page.getByTestId("redeem-error")).toContainText(
        "invitation token is invalid or expired",
      );
      await expect(page.getByTestId("redeem-result")).toHaveCount(0);
    });

    test("a redeemed token cannot be redeemed twice", async ({ page }) => {
      await page.goto("/");
      await waitForConnectionManager(page);
      const token = await issueInvitation(page, "frank");

      await openAndFillRedeem(page, token);
      await page.getByRole("button", { name: "Redeem", exact: true }).click();
      await expect(page.getByTestId("redeem-username")).toHaveText("frank");
      await page.getByRole("button", { name: "Done" }).click();

      await openAndFillRedeem(page, token);
      await page.getByRole("button", { name: "Redeem", exact: true }).click();
      await expect(page.getByTestId("redeem-error")).toContainText(
        "invitation token is invalid or expired",
      );
    });

    test("redeem binding error shows in the modal", async ({ page }) => {
      await page.goto("/");
      await waitForConnectionManager(page);
      const token = await issueInvitation(page, "grace");
      await injectError(page, "InvitationBinding.Redeem", "connection refused");

      await openAndFillRedeem(page, token);
      await page.getByRole("button", { name: "Redeem", exact: true }).click();

      await expect(page.getByTestId("redeem-error")).toContainText("connection refused");
    });

    test("redeem requires a CSR PEM", async ({ page }) => {
      await page.goto("/");
      await waitForConnectionManager(page);

      await openAndFillRedeem(page, "any-token");
      await page.locator("#redeem-csr").fill("");
      await page.getByRole("button", { name: "Redeem", exact: true }).click();

      await expect(page.getByTestId("redeem-error")).toContainText("CSR PEM is required.");
    });
  });
});
