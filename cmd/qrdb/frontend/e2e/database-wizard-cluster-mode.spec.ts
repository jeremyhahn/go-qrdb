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
 * Database creation wizard — Cluster Mode selector tests.
 *
 * Mirrors database-wizard-replicas.spec.ts: on a (mocked) raft-mode server
 * the Raft Configuration step exposes an explicit "Cluster Mode" selector
 * (none / local / distributed) alongside the Replication Factor field —
 * parity with the CLI's `database create --cluster-mode` flag. This test
 * drives the wizard and asserts that the assembled CreateDatabaseRequest
 * carries `raft.clusterMode` matching the user's selection, and that
 * choosing "local" surfaces and submits a Local Node Count.
 */

import { test, expect, secondLaunchEmbeddedState } from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

const STEP_TIMEOUT = 10_000;

/**
 * Overrides the Wails bindings so the active connection is a raft-mode
 * server and DatabaseBinding.Create records the submitted request on window.
 */
async function installRaftAndCapture(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as Record<string, unknown>;
    const origGo = w.go as Record<string, unknown>;
    if (!origGo) return;
    const main = origGo.main as Record<string, Record<string, unknown>>;
    if (!main) return;

    const cmb = main.ConnectionManagerBinding as Record<string, unknown>;
    if (cmb) {
      cmb.GetActiveProfileID = async () => "raft-profile";
      cmb.ListProfiles = async () => [
        {
          id: "raft-profile",
          name: "Raft Server",
          type: "embedded",
          serverMode: "raft",
          embedded: {
            raftEnabled: true,
            dataDir: "/data",
            sealConfig: { mode: "none" },
          },
          createdAt: new Date().toISOString(),
        },
      ];
      cmb.GetState = async () => ({
        profileId: "raft-profile",
        status: "connected",
        isEmbedded: true,
      });
    }

    const dbb = main.DatabaseBinding as Record<string, unknown>;
    if (dbb) {
      dbb.Create = async (req: Record<string, unknown>) => {
        w.__dbCreateReq = req;
        return {
          name: req.name,
          backend: (req.backend as string) || "pebble",
          status: "open",
        };
      };
    }
  });
}

/** Advances the wizard from the landing page to the Raft Configuration step. */
async function openWizardToRaftStep(page: Page, name: string): Promise<void> {
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.locator("aside").getByText("New Database").click();
  await expect(
    page.getByRole("heading", { name: "Database Name & Engine" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });

  await page.locator("#db-name").fill(name);
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Storage Backend" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Raft Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

/** Advances from the Raft step through SQL and Barrier to reach Summary. */
async function advanceRaftToSummary(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Protocols" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Database Encryption Barrier" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
  await page.getByRole("button", { name: "Next" }).click();

  await expect(
    page.getByRole("heading", { name: "Review Configuration" }),
  ).toBeVisible({ timeout: STEP_TIMEOUT });
}

test.describe("Database Wizard - Cluster Mode selector (raft server)", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("cluster mode selector defaults to distributed and shows Raft Groups", async ({
    page,
  }) => {
    await installRaftAndCapture(page);
    await openWizardToRaftStep(page, "cluster-mode-default");

    const main = page.locator("main");
    const select = main.getByTestId("db-cluster-mode-select");
    await expect(select).toBeVisible();
    await expect(select).toHaveValue("distributed");

    // The Raft Groups panel and the production-ready alert are visible by
    // default under the "distributed" mode.
    await expect(main.getByTestId("raft-multinode-ready")).toBeVisible();
    await expect(main.locator("#db-replication-factor")).toBeVisible();
  });

  test("selecting cluster mode 'none' hides the Raft Groups panel", async ({
    page,
  }) => {
    await installRaftAndCapture(page);
    await openWizardToRaftStep(page, "cluster-mode-none");

    const main = page.locator("main");
    await main.getByTestId("db-cluster-mode-select").selectOption("none");

    await expect(main.locator("#db-replication-factor")).not.toBeVisible();
    await expect(main.getByTestId("raft-multinode-ready")).not.toBeVisible();
  });

  test("selecting cluster mode 'local' exposes a Local Node Count input", async ({
    page,
  }) => {
    await installRaftAndCapture(page);
    await openWizardToRaftStep(page, "cluster-mode-local");

    const main = page.locator("main");
    await main.getByTestId("db-cluster-mode-select").selectOption("local");

    await expect(main.locator("#db-local-node-count")).toBeVisible();
    // The distributed-only "production-ready" alert must not show for local.
    await expect(main.getByTestId("raft-multinode-ready")).not.toBeVisible();
  });

  test("wizard sends the selected cluster mode in the create request", async ({
    page,
  }) => {
    await installRaftAndCapture(page);
    await openWizardToRaftStep(page, "cluster-mode-local-submit");

    const main = page.locator("main");
    await main.getByTestId("db-cluster-mode-select").selectOption("local");
    await expect(main.locator("#db-local-node-count")).toBeVisible();
    await main.locator("#db-local-node-count").fill("5");
    await main.locator("#db-replication-factor").fill("2");

    await advanceRaftToSummary(page);
    await page.getByRole("button", { name: "Create Database" }).click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __dbCreateReq?: {
                    raft?: { clusterMode?: string; localNodeCount?: number };
                    replicas?: number;
                  };
                }
              ).__dbCreateReq?.raft?.clusterMode ?? null,
          ),
        { timeout: STEP_TIMEOUT },
      )
      .toBe("local");

    const req = await page.evaluate(
      () =>
        (
          window as unknown as {
            __dbCreateReq?: {
              raft?: { clusterMode?: string; localNodeCount?: number };
              replicas?: number;
            };
          }
        ).__dbCreateReq,
    );
    expect(req?.raft?.localNodeCount).toBe(5);
    expect(req?.replicas).toBe(2);
  });

  test("wizard sends cluster mode 'distributed' by default in the create request", async ({
    page,
  }) => {
    await installRaftAndCapture(page);
    await openWizardToRaftStep(page, "cluster-mode-distributed-submit");

    await advanceRaftToSummary(page);
    await page.getByRole("button", { name: "Create Database" }).click();

    await expect
      .poll(
        () =>
          page.evaluate(
            () =>
              (
                window as unknown as {
                  __dbCreateReq?: { raft?: { clusterMode?: string } };
                }
              ).__dbCreateReq?.raft?.clusterMode ?? null,
          ),
        { timeout: STEP_TIMEOUT },
      )
      .toBe("distributed");
  });
});
