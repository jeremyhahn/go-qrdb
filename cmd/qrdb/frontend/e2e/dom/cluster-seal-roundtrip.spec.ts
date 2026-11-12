// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * cluster-seal-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Seal Manager's cluster seal card (coverage-matrix group 10)
 * against a live passphrase-sealed qrdbd and checks it against GET
 * /v1/barrier/status. The fixture runs one node whose raft host has no
 * transport identity, so the server builds no feature gate and cluster-seal
 * is never activated: the record is {active: false} and every seal is
 * node-local. The card, the cluster unseal action and the migration panel
 * stay hidden through a seal and unseal done in the UI, and the sealed node
 * shows the passphrase unseal panel (it withholds its strategy while sealed).
 *
 * The active card (sealed record, principal, node, at, local and
 * clusterUnseal) needs a multi-node fixture with a raft transport.
 */

import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const PASSPHRASE = "dom-cluster-seal-secret-1";
const SO_PIN = "dom-cluster-seal-so-pin";
const USER_PIN = "dom-cluster-seal-user-pin";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "passphrase" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function sealStatus(
  request: APIRequestContext,
): Promise<Record<string, unknown>> {
  const res = await request.get(`${daemon!.adminUrl}/v1/barrier/status`, {
    ignoreHTTPSErrors: true,
  });
  expect(res.status(), `seal status: ${await res.text()}`).toBe(200);
  return (await res.json()) as Record<string, unknown>;
}

// The init wizard is a multi-step ceremony; this spec covers the cluster seal
// card, so the server is initialized through the ceremony API.
async function initAndOpenSealManager(
  page: Page,
  request: APIRequestContext,
): Promise<void> {
  const init = await request.post(
    `${daemon!.adminUrl}/v1/ceremony/initialize`,
    {
      data: {
        secret: Buffer.from(PASSPHRASE).toString("base64"),
        so_pin: SO_PIN,
        user_pin: USER_PIN,
        admin_generate: true,
      },
      ignoreHTTPSErrors: true,
    },
  );
  expect(
    init.ok(),
    `ceremony init: ${init.status()} ${await init.text()}`,
  ).toBeTruthy();
  await waitForHealthy(daemon!);
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByText("Seal Manager").first().click();
  await expect(
    page.getByRole("heading", { name: "Seal Manager" }),
  ).toBeVisible();
}

test("one node: the cluster seal card follows the server's inactive record through a node-local seal and unseal", async ({
  page,
  request,
}) => {
  await initAndOpenSealManager(page, request);
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  // The server's view: a node whose raft host has no transport identity runs
  // no feature gate, so cluster-seal is never activated and every seal is
  // node-local.
  const before = await sealStatus(request);
  expect(before.sealed).toBe(false);
  expect(before.strategy).toBe("passphrase");
  expect(before.cluster_seal).toEqual({
    active: false,
    sealed: false,
    epoch: 0,
  });

  // The UI's view agrees: no cluster seal card, no cluster unseal action,
  // no cluster migration panel.
  await expect(page.getByTestId("cluster-seal-status")).toHaveCount(0);
  await expect(page.getByTestId("cluster-unseal-btn")).toHaveCount(0);
  await expect(page.getByTestId("cluster-migration-status")).toHaveCount(0);

  // Seal the node through the UI (the SO PIN is provisioned, so it is asked for).
  await page.getByRole("button", { name: "Seal", exact: true }).click();
  const soPin = page.getByTestId("seal-so-pin");
  if (await soPin.isVisible()) await soPin.fill(SO_PIN);
  await page.getByRole("button", { name: "Confirm Seal" }).click();
  await expect(
    page.getByText(
      "Enter the barrier passphrase to unseal the encryption barrier.",
    ),
  ).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Sealed").first()).toBeVisible();

  const sealed = await sealStatus(request);
  expect(sealed.sealed).toBe(true);
  // A node-local seal writes no cluster record.
  expect(
    (sealed.cluster_seal as { active?: boolean } | undefined)?.active ?? false,
  ).toBe(false);
  await expect(page.getByTestId("cluster-seal-status")).toHaveCount(0);
  await expect(page.getByTestId("cluster-unseal-btn")).toHaveCount(0);

  // Unseal through the UI with the passphrase.
  await page.getByPlaceholder("Enter passphrase").fill(PASSPHRASE);
  await page.getByRole("button", { name: "Unseal", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Seal", exact: true }),
  ).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("Unsealed").first()).toBeVisible();

  const after = await sealStatus(request);
  expect(after.sealed).toBe(false);
  expect(after.cluster_seal).toEqual({
    active: false,
    sealed: false,
    epoch: 0,
  });
  await expect(page.getByTestId("cluster-seal-status")).toHaveCount(0);
});
