// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Web-mode E2E tests for the Cluster view's join-token form.
 *
 * admin.mintJoinToken is deliberately not available over the embedded desktop
 * transport (it has no authorization), so the form only renders when the
 * client is the REST client. The REST API is mocked via page.route() and
 * window.go is absent, so isDesktop() returns false.
 */

import {
  webTest as test,
  expect,
  threeNodeTopology,
  type WebMockState,
} from "./fixtures/web-mode-mock.ts";
import type { Page } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

/** A raft cluster so the Cluster admin view is reachable. */
const webState: WebMockState = {
  sealStatus: {
    sealed: false,
    initialized: true,
    strategy: "none",
    hardware_backed: false,
    initialized_at: "2026-01-01T00:00:00Z",
  },
  databases: [{ name: "default", backend: "pebble", status: "open" }],
  topology: threeNodeTopology,
};

/**
 * Mocks the other REST calls the Cluster view makes on mount: the peer list
 * and the node compaction policy (none set).
 */
async function mockClusterView(page: Page): Promise<void> {
  await page.route("**/peers", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ shards: [] }),
    }),
  );
  await page.route("**/admin/node-compaction", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({}),
    }),
  );
}

/** Waits for the dashboard, then opens the Cluster admin view. */
async function navigateToCluster(page: Page): Promise<void> {
  await mockClusterView(page);
  await page.goto("/");
  await page.locator(".breadcrumbs").getByText("Dashboard").waitFor({
    state: "visible",
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("aside").getByText("Cluster").click();
  await expect(page.locator(".breadcrumbs").getByText("Cluster")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test.describe("Web Mode - Cluster join token", () => {
  test.use({ webMockState: webState });

  test("mints a join token over REST and shows it with a copy button", async ({ page }) => {
    let requestBody: unknown = null;
    await page.route("**/v1/cluster/join-tokens", async (route) => {
      requestBody = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          token: "qrdb-join-token-abc123",
          expiresAt: "2026-11-12T13:00:00Z",
        }),
      });
    });

    await navigateToCluster(page);

    const card = page.getByTestId("join-token-card");
    await expect(card).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(card.getByTestId("join-token-desktop-note")).toHaveCount(0);

    await card.getByLabel("Join node ID").fill("node-4");
    await card.getByLabel("Join token TTL seconds").fill("600");
    await card.getByRole("button", { name: "Mint Token" }).click();

    await expect(card.getByTestId("join-token-value")).toHaveText("qrdb-join-token-abc123", {
      timeout: 5_000,
    });
    await expect(card).toContainText("Expires 2026-11-12T13:00:00Z");
    await expect(card.getByRole("button", { name: "Copy join token" })).toBeVisible();
    expect(requestBody).toEqual({ node_id: "node-4", ttl_seconds: 600 });
  });

  test("join token mint error shows an error toast", async ({ page }) => {
    await page.route("**/v1/cluster/join-tokens", (route) =>
      route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ error: "permission denied: cluster:join-token" }),
      }),
    );

    await navigateToCluster(page);

    const card = page.getByTestId("join-token-card");
    await card.getByLabel("Join node ID").fill("node-5");
    await card.getByRole("button", { name: "Mint Token" }).click();

    await expect(page.getByText("permission denied", { exact: false })).toBeVisible({
      timeout: 5_000,
    });
    await expect(card.getByTestId("join-token-value")).toHaveCount(0);
  });

  test("mint without a node ID shows a validation warning", async ({ page }) => {
    await navigateToCluster(page);

    const card = page.getByTestId("join-token-card");
    await card.getByRole("button", { name: "Mint Token" }).click();
    await expect(page.getByText("Node ID is required")).toBeVisible({ timeout: 5_000 });
  });
});
