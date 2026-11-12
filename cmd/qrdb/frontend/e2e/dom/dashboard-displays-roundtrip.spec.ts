// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * dashboard-displays-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the admin Dashboard's Shards and Capabilities cards against a live
 * qrdbd (seal "none", auth off):
 *
 *   after a database is created through the API, the Shards card renders
 *   one row per entry of GET /v1/status's shards with the same cells, and
 *   the Capabilities card marks each capability enabled exactly when
 *   GET /v1/capabilities reports it.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Loads the app shell fresh from the daemon and opens the admin Dashboard. */
async function openDashboard(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText("Dashboard", { exact: true }).click();
  await expect(page.getByRole("heading", { name: "Capabilities" })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** The SDK's snake_case to camelCase key transform, which the Dashboard's columns show. */
function camel(key: string): string {
  return key.replace(/_([a-z0-9])/g, (_m, c: string) => c.toUpperCase());
}

/** The Dashboard's cell text for a wire value (nested objects keep their keys, which are not snake_case here). */
function cell(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** Capability badge label on the Dashboard → its key in GET /capabilities. */
const CAPABILITY_KEYS: Record<string, string> = {
  KV: "kv",
  MVCC: "mvcc",
  Transactions: "transaction",
  Batch: "batch",
  Watch: "watch",
  Leases: "lease",
  "Time Series": "timeSeries",
  Vectors: "vector",
  Index: "index",
};

const RAFT_DB = "dashraft";

test("dashboard shards and capabilities round-trip (UI ↔ live daemon)", async ({ page, request }) => {
  expect(daemon).not.toBeNull();
  const base = daemon!.adminUrl;

  // --- A database on the node; the node's raft host carries its shard(s). -----------
  const created = await request.post(`${base}/v1/databases`, {
    data: { name: RAFT_DB, backend: "raft" },
    ignoreHTTPSErrors: true,
  });
  expect(created.status(), `create ${RAFT_DB}: ${await created.text()}`).toBe(201);

  let status: { shard_count: number; shards: Record<string, unknown>[] } = { shard_count: 0, shards: [] };
  await expect
    .poll(async () => {
      const r = await request.get(`${base}/status`, { ignoreHTTPSErrors: true });
      status = await r.json();
      return status.shards.length;
    }, { timeout: LOAD_TIMEOUT })
    .toBeGreaterThan(0);

  const capsResp = await request.get(`${base}/capabilities`, { ignoreHTTPSErrors: true });
  expect(capsResp.status()).toBe(200);
  const caps = (await capsResp.json()) as Record<string, unknown>;

  // --- The Shards card shows the status answer. -------------------------------------
  await openDashboard(page);
  const card = page.getByTestId("dashboard-shards");
  const table = card.getByTestId("dashboard-shards-table");
  const rows = table.locator("tbody tr");
  await expect(rows).toHaveCount(status.shards.length, { timeout: LOAD_TIMEOUT });

  const wireColumns: string[] = [];
  for (const s of status.shards) for (const k of Object.keys(s)) if (!wireColumns.includes(k)) wireColumns.push(k);
  await expect(table.locator("thead th")).toHaveText(wireColumns.map(camel));

  // Shard ids, leadership and health are stable between the two reads; counters
  // (term, last_applied, pending) may move, so they are checked for presence only.
  const STABLE = ["shard_id", "replica_id", "is_leader", "state_machine_type_name", "health", "nodes"];
  const shown = await rows.evaluateAll((trs) =>
    trs.map((tr) => Array.from(tr.querySelectorAll("td")).map((td) => (td.textContent ?? "").trim())),
  );
  const shownById = new Map(shown.map((cells) => [cells[wireColumns.indexOf("shard_id")], cells]));
  for (const s of status.shards) {
    const cells = shownById.get(cell(s["shard_id"]));
    expect(cells, `row for shard ${cell(s["shard_id"])}`).toBeDefined();
    for (const key of STABLE) {
      if (!(key in s)) continue;
      expect(cells![wireColumns.indexOf(key)], `shard ${cell(s["shard_id"])} ${key}`).toBe(cell(s[key]));
    }
  }

  // The stat agrees with the status shard count.
  await expect(page.locator(".stat").filter({ hasText: "Loaded on this node" }).locator(".stat-value"))
    .toHaveText(String(status.shard_count));

  // --- The Capabilities card shows the capabilities answer. -------------------------
  const capCard = page.locator(".card").filter({ has: page.getByRole("heading", { name: "Capabilities" }) });
  for (const [label, key] of Object.entries(CAPABILITY_KEYS)) {
    expect(typeof caps[key], `capabilities.${key} is a boolean`).toBe("boolean");
    const badge = capCard.locator(".badge").filter({ hasText: new RegExp(`^\\s*${label}\\s*$`) });
    await expect(badge, `badge ${label}`).toHaveCount(1);
    if (caps[key] === true) {
      await expect(badge, `${label} enabled`).toHaveClass(/badge-success/);
    } else {
      await expect(badge, `${label} disabled`).toHaveClass(/badge-ghost/);
    }
  }
});
