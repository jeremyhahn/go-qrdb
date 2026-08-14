// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * rebalance-panel-web.spec.ts (mocked tier — web/REST transport)
 *
 * Fast-tier regression guard for the "hotspots" omitted-array crash class.
 *
 * Background: GET /v1/rebalance/report tags its Hotspots field
 * `json:"hotspots,omitempty"` on the Go side (fixed to no longer omit it —
 * see pkg/api/rest/handlers_rebalance.go — but RebalancePanel.svelte must
 * stay defensive regardless, since any other omitempty array field can
 * reintroduce the same crash). Go's encoding/json drops an `omitempty`
 * array field from the wire whenever it is empty, REGARDLESS of whether the
 * slice is nil or a non-nil zero-length slice — the common "balanced
 * cluster, zero hotspots" case. RebalancePanel.svelte used to evaluate
 * `report.hotspots.length` unconditionally, throwing "Cannot read
 * properties of undefined (reading 'length')" and aborting the render of
 * the entire imbalance-report section.
 *
 * The existing mocked Wails-desktop suite (rebalance-panel.spec.ts) never
 * caught this because its fixture always supplies an explicit `hotspots: []`
 * (or a populated array) rather than reproducing the real wire encoding.
 * This spec uses the REST/web-mode fixture (web-mode-mock.ts) and
 * page.route() to hand back a response body with the "hotspots" key
 * omitted entirely, exactly like the real qrdbd wire encoding, so it
 * exercises the same code path the real-backend DOM test
 * (e2e/dom/cluster-topology.spec.ts) exercises, at the fast/mocked tier.
 */

import {
  webTest as test,
  expect,
  defaultWebMockState,
  threeNodeTopology,
} from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Rebalance Panel (web/REST transport)", () => {
  // Rebalance nav is gated on serverMode === "raft", derived from the live
  // /admin/topology response reporting more than one distinct node.
  test.use({ webMockState: { ...defaultWebMockState, topology: threeNodeTopology } });

  test("renders the imbalance report when the wire response omits the \"hotspots\" key entirely", async ({
    page,
  }) => {
    const pageErrors: Error[] = [];
    page.on("pageerror", (err) => pageErrors.push(err));

    await page.route("**/v1/rebalance/report", (route) => {
      void route.fulfill({
        status: 200,
        contentType: "application/json",
        // Deliberately no "hotspots" key — this is the real wire shape Go's
        // encoding/json produces for a `json:"hotspots,omitempty"` field
        // holding a len-0 slice, NOT `{ "hotspots": [] }`.
        body: JSON.stringify({
          nodes: [
            {
              node_id: "node-1",
              address: "127.0.0.1:9001",
              shard_count: 2,
              total_keys: 100,
              total_bytes: 2048,
              leader_count: 1,
            },
            {
              node_id: "node-2",
              address: "127.0.0.1:9002",
              shard_count: 2,
              total_keys: 100,
              total_bytes: 2048,
              leader_count: 1,
            },
          ],
          imbalance_score: 0,
        }),
      });
    });

    await page.goto("/");
    await expect(page.locator("aside").getByText("Seal Manager", { exact: true })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    await page.locator("aside").getByText("Rebalance", { exact: true }).click();
    await expect(page.getByRole("heading", { name: "Rebalance Plan" })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });

    const main = page.locator("main");

    // All three stat cards render — including "Hotspots", proving
    // `(report.hotspots ?? []).length` resolved to 0 instead of throwing.
    await expect(main.getByText("Imbalance Score")).toBeVisible({ timeout: LOAD_TIMEOUT });
    const hotspotsCard = main.locator(".surface-panel", { hasText: "Hotspots" });
    await expect(hotspotsCard).toBeVisible();
    await expect(hotspotsCard.getByText("0", { exact: true })).toBeVisible();

    // The section after the stat cards (Node Load table) also renders,
    // proving the template did not abort partway through.
    await expect(main.getByText("Node Load")).toBeVisible();
    await expect(main.getByText("node-1")).toBeVisible();
    await expect(main.getByText("node-2")).toBeVisible();

    expect(pageErrors).toEqual([]);
  });
});
