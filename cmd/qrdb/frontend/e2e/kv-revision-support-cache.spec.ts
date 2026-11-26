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

// The KV explorer remembers a database whose store answered 501 to the
// revision probe. The memory is keyed by connection profile, tenant and
// database name, and checked against the database's store fingerprint: a
// second connection (or tenant) with a same-named database, or a database
// recreated with a different store, still probes.

import type { Page } from "@playwright/test";
import {
  test,
  expect,
  injectError,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";
import {
  RevisionSupportCache,
  REVISION_SUPPORT_STORAGE_KEY,
  revisionSupportFingerprint,
  revisionSupportKey,
  type RevisionSupportStorage,
} from "../src/lib/revisionSupportCache.ts";

const LOAD_TIMEOUT = 10_000;
const DB = "kv-shared";
const PROFILE = "mock-profile-1";

const sharedState = {
  ...secondLaunchEmbeddedState,
  databases: [{ name: DB, backend: "pebble", status: "open" }],
};

function memoryStorage(): RevisionSupportStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => {
      data.set(key, value);
    },
  };
}

test.describe("revision-support cache module", () => {
  const pebble = revisionSupportFingerprint({ backend: "pebble", dataDir: "/data/a" });

  test("a second connection with a same-named database is not covered", () => {
    const cache = new RevisionSupportCache(memoryStorage());
    cache.rememberUnsupported({ profileId: "conn-a", tenant: "t1", database: "orders" }, pebble);
    expect(cache.isUnsupported({ profileId: "conn-a", tenant: "t1", database: "orders" }, pebble)).toBe(true);
    expect(cache.isUnsupported({ profileId: "conn-b", tenant: "t1", database: "orders" }, pebble)).toBe(false);
    expect(cache.isUnsupported({ profileId: "conn-a", tenant: "t2", database: "orders" }, pebble)).toBe(false);
  });

  test("a recreated database with a different store drops the entry", () => {
    const storage = memoryStorage();
    const cache = new RevisionSupportCache(storage);
    const scope = { profileId: "conn-a", tenant: "t1", database: "orders" };
    cache.rememberUnsupported(scope, pebble);
    const raft = revisionSupportFingerprint({ backend: "raft", shardId: 7, dataDir: "/data/a" });
    expect(cache.isUnsupported(scope, raft)).toBe(false);
    // The stale entry is gone for good, even for the old fingerprint.
    expect(cache.isUnsupported(scope, pebble)).toBe(false);
    expect(JSON.parse(storage.data.get(REVISION_SUPPORT_STORAGE_KEY) ?? "{}")).toEqual({});
  });

  test("entries persist across instances sharing a storage", () => {
    const storage = memoryStorage();
    const scope = { profileId: "conn-a", tenant: "", database: "orders" };
    new RevisionSupportCache(storage).rememberUnsupported(scope, pebble);
    expect(new RevisionSupportCache(storage).isUnsupported(scope, pebble)).toBe(true);
  });
});

async function openKV(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText(DB).click();
  await expect(page.locator(".breadcrumbs").getByText(DB, { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Returns the stored cache entries, keyed by revisionSupportKey. */
async function storedEntries(page: Page): Promise<Record<string, string>> {
  const raw = await page.evaluate((k) => sessionStorage.getItem(k), REVISION_SUPPORT_STORAGE_KEY);
  return raw ? (JSON.parse(raw) as Record<string, string>) : {};
}

async function storeEntries(page: Page, entries: Record<string, string>): Promise<void> {
  await page.evaluate(
    ({ k, v }) => sessionStorage.setItem(k, v),
    { k: REVISION_SUPPORT_STORAGE_KEY, v: JSON.stringify(entries) },
  );
}

test.describe("KV explorer revision-support cache", () => {
  test.use({ mockState: sharedState });

  test("a second connection with a same-named database still probes", async ({ page }) => {
    const line = page.getByTestId("kv-current-revision");
    const unavailable = page.getByTestId("kv-revisions-unavailable");

    // This connection's store answers 501: the explorer records it.
    await openKV(page);
    await injectError(page, "MVCCBinding.CurrentRevision", "501 Not Implemented");
    await page.getByRole("button", { name: "Refresh" }).first().click();
    await expect(unavailable).toBeVisible({ timeout: LOAD_TIMEOUT });

    const entries = await storedEntries(page);
    const ownKey = Object.keys(entries).find((k) => {
      const [profileId, , database] = JSON.parse(k) as [string, string, string];
      return profileId === PROFILE && database === DB;
    });
    expect(ownKey, `entries: ${JSON.stringify(entries)}`).toBeDefined();
    const [, tenant] = JSON.parse(ownKey!) as [string, string, string];
    const fingerprint = entries[ownKey!]!;

    // Same connection, same store after a reload: no probe, still unavailable.
    await openKV(page);
    await expect(unavailable).toBeVisible({ timeout: LOAD_TIMEOUT });
    await expect(line).toHaveText(/Current revision:\s*—/);

    // Only a second connection's same-named database is recorded: this
    // connection's store keeps revisions and the explorer must ask.
    await storeEntries(page, {
      [revisionSupportKey({ profileId: "second-connection", tenant, database: DB })]: fingerprint,
    });
    await openKV(page);
    await expect(line).toHaveText(/Current revision:\s*0/, { timeout: LOAD_TIMEOUT });
    await expect(unavailable).toHaveCount(0);

    // An entry for this connection recorded under another store (the
    // database was recreated) is stale: the explorer asks again.
    await storeEntries(page, {
      [revisionSupportKey({ profileId: PROFILE, tenant, database: DB })]:
        revisionSupportFingerprint({ backend: "raft", shardId: 99, dataDir: "/elsewhere" }),
    });
    await openKV(page);
    await expect(line).toHaveText(/Current revision:\s*0/, { timeout: LOAD_TIMEOUT });
    await expect(unavailable).toHaveCount(0);
  });
});
