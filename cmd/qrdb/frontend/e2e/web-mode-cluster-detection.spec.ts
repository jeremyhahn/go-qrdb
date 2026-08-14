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
 * Web-mode cluster-topology detection.
 *
 * The built-in "This Server" web profile carries no recorded topology, so the
 * web UI must derive its server mode from the live /admin/topology endpoint
 * (connection-manager.detectWebServerMode): a server reporting more than one
 * distinct Raft node is "raft" and exposes the Cluster / Topology admin nav
 * entries; a single-node server stays standalone and hides them.
 *
 * This is the mocked counterpart to the real-backend multi-node DOM spec
 * (e2e/dom/cluster-topology.spec.ts) and guards the WebSidebar → AdminNav
 * serverMode wiring against regression.
 */

import {
  webTest as test,
  expect,
  defaultWebMockState,
  threeNodeTopology,
  singleNodeTopology,
} from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Web mode - cluster topology detection", () => {
  test.describe("multi-node server", () => {
    test.use({
      webMockState: { ...defaultWebMockState, topology: threeNodeTopology },
    });

    test("exposes Cluster + Topology nav when topology reports 3 nodes", async ({ page }) => {
      await page.goto("/");
      const aside = page.locator("aside");

      await expect(aside.getByText("Topology", { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
      await expect(aside.getByText("Cluster", { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });
    });
  });

  test.describe("single-node server", () => {
    test.use({
      webMockState: { ...defaultWebMockState, topology: singleNodeTopology },
    });

    test("hides Cluster + Topology nav when topology reports 1 node", async ({ page }) => {
      await page.goto("/");

      // The app shell must be loaded before asserting absence — wait for a
      // stable always-present admin nav item first.
      const aside = page.locator("aside");
      await expect(aside.getByText("Seal Manager", { exact: true })).toBeVisible({
        timeout: LOAD_TIMEOUT,
      });

      await expect(aside.getByText("Topology", { exact: true })).toHaveCount(0);
      await expect(aside.getByText("Cluster", { exact: true })).toHaveCount(0);
    });
  });
});
