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
import { webTest, injectWebRestMock, type WebMockState } from "./fixtures/web-mode-mock.ts";
import { isApiPath } from "../api-routes.ts";
import { test as plainTest } from "@playwright/test";

const LOAD_TIMEOUT = 10_000;

const vlogState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "vlog-db", backend: "pebble", status: "open" },
  ],
};

type Page = import("@playwright/test").Page;

async function navigateToVlog(page: Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("vlog-db").click();
  await expect(page.locator(".breadcrumbs").getByText("vlog-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("tab", { name: "Verifiable Logs", exact: true }).click();
}

/** Creates a log through the Create form and selects it in the list. */
async function createAndSelect(page: Page, name: string): Promise<void> {
  await page.getByLabel("Log name").fill(name);
  await page.getByLabel("Origin").fill(`example.com/${name}`);
  await page.getByTestId("vlog-create-submit").click();
  const entry = page.getByTestId("vlog-list").getByRole("button", { name, exact: true });
  await expect(entry).toBeVisible({ timeout: LOAD_TIMEOUT });
  await entry.click();
  await expect(page.getByTestId("vlog-head")).toContainText("Size", { timeout: LOAD_TIMEOUT });
}

async function appendLines(page: Page, lines: string[]): Promise<void> {
  await page.getByLabel("Entries to append").fill(lines.join("\n"));
  await page.getByTestId("vlog-append-submit").click();
}

test.describe("Verifiable Logs tab", () => {
  test.use({ mockState: vlogState });

  test("create lists the new log and shows its key version", async ({ page }) => {
    await navigateToVlog(page);
    await page.getByLabel("Log name").fill("audit");
    await page.getByLabel("Origin").fill("example.com/audit");
    await page.getByTestId("vlog-create-submit").click();
    await expect(page.getByTestId("vlog-list").getByRole("button", { name: "audit", exact: true })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await expect(page.getByTestId("vlog-created-key")).toHaveText("Key version 1");
  });

  test("append then get shows the decoded entries and head size", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "events");
    await appendLines(page, ["first", "zweite ü"]);
    await expect(page.getByText("Appended 2 entries from index 0")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId("vlog-head").locator("tr", { hasText: /^Size/ })).toContainText("2");

    await page.getByTestId("vlog-get-submit").click();
    const list = page.getByTestId("vlog-entries");
    await expect(list.locator("li")).toHaveCount(2, { timeout: 5_000 });
    await expect(list.locator("li").nth(0)).toContainText("0: first");
    await expect(list.locator("li").nth(1)).toContainText("1: zweite ü");
  });

  test("inclusion and consistency proofs show their hash counts", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "proofs");
    await appendLines(page, ["a"]);
    await expect(page.getByText("Appended 1 entries from index 0")).toBeVisible({ timeout: 5_000 });

    await page.getByLabel("Inclusion index").fill("0");
    await page.getByLabel("Inclusion size").fill("1");
    await page.getByTestId("vlog-inclusion-submit").click();
    await expect(page.getByTestId("vlog-inclusion-proof")).toContainText("0 hashes", { timeout: 5_000 });

    await page.getByLabel("Consistency size 1").fill("1");
    await page.getByLabel("Consistency size 2").fill("1");
    await page.getByTestId("vlog-consistency-submit").click();
    await expect(page.getByTestId("vlog-consistency-proof")).toContainText("0 hashes", { timeout: 5_000 });
  });

  test("sign head shows the signed head and checkpoint", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "signed");
    await page.getByTestId("vlog-sign-head").click();
    const signed = page.getByTestId("vlog-signed-head");
    await expect(signed).toContainText("Key version 1, ed25519", { timeout: 5_000 });
    await expect(signed.locator("pre")).toBeAttached();
  });

  test("rotate key shows the new key version", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "rotating");
    await page.getByLabel("New key type").selectOption("ecdsa-p256-sha256");
    await page.getByTestId("vlog-rotate-submit").click();
    await expect(page.getByTestId("vlog-rotated-key")).toHaveText("Key version 2", { timeout: 5_000 });
  });

  test("expire bodies asks to confirm and reports the count", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "expiring");
    await page.getByLabel("Cutoff index").fill("3");
    await page.getByTestId("vlog-expire-submit").click();
    const modal = page.locator(".modal").filter({ hasText: "Expire Bodies" });
    await expect(modal).toBeVisible({ timeout: 5_000 });
    await modal.getByRole("button", { name: "Expire", exact: true }).click();
    await expect(page.getByText("Expired 3 bodies")).toBeVisible({ timeout: 5_000 });
  });

  test("a create failure shows the error", async ({ page }) => {
    await navigateToVlog(page);
    await injectError(page, "VlogBinding.Create", "log exists");
    await page.getByLabel("Log name").fill("dup");
    await page.getByLabel("Origin").fill("example.com/dup");
    await page.getByTestId("vlog-create-submit").click();
    await expect(page.getByText("log exists")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByTestId("vlog-list").getByRole("button", { name: "dup", exact: true })).toHaveCount(0);
    await clearError(page, "VlogBinding.Create");
  });

  test("a negative inclusion index is refused before the call", async ({ page }) => {
    await navigateToVlog(page);
    await createAndSelect(page, "refusing");
    await injectError(page, "VlogBinding.ProveInclusion", "proof call reached the binding");
    await page.getByLabel("Inclusion index").fill("-1");
    await page.getByTestId("vlog-inclusion-submit").click();
    await expect(page.getByText("Index must be a non-negative integer.")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("proof call reached the binding")).toHaveCount(0);
    await expect(page.getByTestId("vlog-inclusion-proof")).not.toContainText("hashes");
    await clearError(page, "VlogBinding.ProveInclusion");
  });
});

/** Opens vlog-db and waits for its tab bar, with the Vectors tab in it. */
async function openWorkspace(page: Page): Promise<void> {
  await page.goto("/");
  await page.locator("aside").getByText("vlog-db").click();
  await expect(page.getByRole("tab", { name: "Vectors", exact: true })).toBeVisible({ timeout: LOAD_TIMEOUT });
}

const vlogTab = (page: Page) => page.getByRole("tab", { name: "Verifiable Logs", exact: true });

// The desktop binding answers a call to an unwired service with the
// embedded NilServiceError, which the Wails runtime rejects as its message.
test.describe("Verifiable Logs tab on a desktop without a log service", () => {
  test.use({ mockState: { ...vlogState, errors: { "VlogBinding.List": "embedded: vlog must not be nil" } } });

  test("is not offered", async ({ page }) => {
    await openWorkspace(page);
    await expect(page.getByRole("tab", { name: "Leases", exact: true })).toBeVisible();
    await expect(vlogTab(page)).toHaveCount(0);
  });
});

// A failure that does not say the service is absent keeps the tab, so the
// view can show the error.
test.describe("Verifiable Logs tab when listing logs fails", () => {
  test.use({ mockState: { ...vlogState, errors: { "VlogBinding.List": "permission denied" } } });

  test("stays on offer", async ({ page }) => {
    await openWorkspace(page);
    await expect(vlogTab(page)).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

const webVlogState: WebMockState = {
  sealStatus: { sealed: false, initialized: false, strategy: "none", hardware_backed: false },
  databases: [{ name: "vlog-db", backend: "pebble", status: "open" }],
};

/** The KV Explorer the workspace opens on scans the keys and reads the MVCC revision. */
async function routeKv(page: Page): Promise<void> {
  await page.route("**/kv/scan**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ pairs: {} }) }),
  );
  await page.route("**/kv/revision**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ revision: 1 }) }),
  );
}

webTest.describe("Verifiable Logs tab over REST", () => {
  webTest("is not offered when the server answers 501", async ({ page }) => {
    await injectWebRestMock(page, webVlogState);
    await routeKv(page);
    let probed = false;
    await page.route("**/vlog/logs**", (route) => {
      probed = true;
      void route.fulfill({
        status: 501,
        contentType: "application/json",
        body: JSON.stringify({ error: "rest: service not implemented: vlog" }),
      });
    });
    await openWorkspace(page);
    await expect.poll(() => probed, { timeout: LOAD_TIMEOUT }).toBe(true);
    await expect(vlogTab(page)).toHaveCount(0);
  });

  webTest("is offered when the server lists the database's logs", async ({ page }) => {
    await injectWebRestMock(page, webVlogState);
    await routeKv(page);
    await page.route("**/vlog/logs**", (route) => {
      void route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ logs: ["audit"] }) });
    });
    await openWorkspace(page);
    await expect(vlogTab(page)).toBeVisible({ timeout: LOAD_TIMEOUT });
  });
});

// The dev proxy forwards, and the mocked tier's API guard covers, every
// /vlog and /groups request the UI sends; neither swallows a lookalike path.
plainTest("the API path matcher covers /vlog and /groups", () => {
  for (const path of ["/vlog", "/vlog/logs", "/vlog/proof/inclusion", "/groups", "/groups/list", "/groups/members/add"]) {
    expect(isApiPath(path), path).toBe(true);
  }
  for (const path of ["/vlogs", "/vlog-view.js", "/groupsx"]) {
    expect(isApiPath(path), path).toBe(false);
  }
});
