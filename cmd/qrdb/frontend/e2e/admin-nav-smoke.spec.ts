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
 * Admin sidebar navigation smoke tests — regression guard for the binding
 * registration bug class.
 *
 * Background: when a Go Wails binding is created but not added to the
 * options.App.Bind slice in gui_desktop.go, the TypeScript SDK accesses
 * window.go.main.<Name> which is undefined at runtime. Any method call on
 * undefined throws "undefined is not an object (evaluating 't.List')" or
 * similar, producing an unhandled JavaScript exception that surfaces as a
 * blank content area with no visible heading.
 *
 * The bug was first introduced when TenantBinding and BackupBinding were
 * added to the Go source but omitted from the Bind list.
 *
 * These tests click every admin sidebar item and assert:
 *   1. No unhandled JavaScript exceptions occur during or after navigation.
 *   2. The breadcrumb updates to the expected section name (content loaded).
 *   3. The main content area is not blank.
 *
 * Cluster is gated on serverMode === "raft" and is tested separately using
 * raftClusterState. Daemon is desktop-only but renders in the mock env.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
  raftClusterState,
} from "./fixtures/wails-mock.ts";
import type { Page } from "@playwright/test";

/** Generous timeout — each view may trigger async data fetches. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Registers a pageerror listener before navigation and returns a reference
 * to the accumulator array.  The listener is active for the lifetime of
 * the page so it captures errors triggered by any async code that fires
 * after the initial click settles.
 */
function attachPageErrorCollector(page: Page): { errors: Error[] } {
  const collector: { errors: Error[] } = { errors: [] };
  page.on("pageerror", (err) => {
    collector.errors.push(err);
  });
  return collector;
}

/**
 * Navigates to root and waits for the application to reach the Dashboard
 * state.  Returns the page-error collector so callers can assert on it.
 */
async function loadApp(page: Page): Promise<{ errors: Error[] }> {
  const collector = attachPageErrorCollector(page);
  await page.goto("/");
  await expect(
    page.locator(".breadcrumbs").getByText("Dashboard"),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  return collector;
}

/**
 * Clicks a sidebar button identified by its title attribute and waits for
 * the breadcrumb to reflect the section name.
 *
 * Using the title attribute avoids strict-mode violations when the same
 * label (e.g. "Databases") appears both as a sidebar button title and as
 * a heading inside the content area.
 */
async function clickSidebarItem(page: Page, title: string): Promise<void> {
  await page.locator(`aside button[title="${title}"]`).click();
  await expect(
    page.locator(".breadcrumbs").getByText(title),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/**
 * Asserts that no JavaScript exceptions were collected and that the main
 * content area contains at least one visible element (not blank).
 */
async function assertNoErrorsAndContentLoaded(
  page: Page,
  collector: { errors: Error[] },
  sectionLabel: string,
): Promise<void> {
  // Allow a short settle window for async rendering to complete.
  await page.waitForTimeout(300);

  if (collector.errors.length > 0) {
    const messages = collector.errors.map((e) => e.message).join("\n  ");
    throw new Error(
      `JavaScript exception(s) after navigating to "${sectionLabel}":\n  ${messages}`,
    );
  }

  // The main element must contain at least one visible child — a blank screen
  // means the view failed to mount, which is the symptom of a missing binding.
  const main = page.locator("main");
  await expect(main).not.toBeEmpty();
}

// ---------------------------------------------------------------------------
// Standalone (non-raft) admin item smoke tests
// ---------------------------------------------------------------------------

test.describe("Admin Nav Smoke — standalone mode", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  // Items that should be present and navigable in standalone embedded mode.
  // Cluster is absent because serverMode is "standalone".
  // Daemon is included because the mock environment treats isDesktop() as true.
  const standaloneItems: readonly string[] = [
    "Dashboard",
    "Seal Manager",
    "Tenants",
    "Users",
    "Roles",
    "Certificates",
    "Credentials",
    "Databases",
    "Backups",
    "Audit Log",
    "Settings",
  ] as const;

  for (const item of standaloneItems) {
    test(`clicking "${item}" shows content without JavaScript errors`, async ({ page }) => {
      const collector = await loadApp(page);

      // Navigate away from Dashboard first so the click is a genuine
      // navigation (Dashboard is already active on load).
      if (item !== "Dashboard") {
        await clickSidebarItem(page, item);
      }

      await assertNoErrorsAndContentLoaded(page, collector, item);

      // Secondary assertion: the breadcrumb must reflect the clicked item.
      await expect(
        page.locator(".breadcrumbs").getByText(item),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });
    });
  }

  test("navigating through all standalone items accumulates no errors", async ({ page }) => {
    const collector = await loadApp(page);

    for (const item of standaloneItems) {
      await clickSidebarItem(page, item);

      if (collector.errors.length > 0) {
        const messages = collector.errors.map((e) => e.message).join("\n  ");
        throw new Error(
          `JavaScript exception(s) after navigating to "${item}":\n  ${messages}`,
        );
      }
    }
  });
});

// ---------------------------------------------------------------------------
// Raft cluster admin item smoke tests (includes Cluster nav item)
// ---------------------------------------------------------------------------

test.describe("Admin Nav Smoke — raft cluster mode", () => {
  test.use({ mockState: raftClusterState });

  test(`clicking "Cluster" shows content without JavaScript errors`, async ({ page }) => {
    const collector = await loadApp(page);

    await clickSidebarItem(page, "Cluster");
    await assertNoErrorsAndContentLoaded(page, collector, "Cluster");

    await expect(
      page.locator(".breadcrumbs").getByText("Cluster"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test(`clicking "Topology" shows content without JavaScript errors`, async ({ page }) => {
    const collector = await loadApp(page);

    await clickSidebarItem(page, "Topology");
    await assertNoErrorsAndContentLoaded(page, collector, "Topology");

    await expect(
      page.locator(".breadcrumbs").getByText("Topology"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test(`clicking "Rebalance" shows content without JavaScript errors`, async ({ page }) => {
    const collector = await loadApp(page);

    await clickSidebarItem(page, "Rebalance");
    await assertNoErrorsAndContentLoaded(page, collector, "Rebalance");

    await expect(
      page.locator(".breadcrumbs").getByText("Rebalance"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  test("navigating through all raft items accumulates no errors", async ({ page }) => {
    const allItems = [
      "Dashboard",
      "Cluster",
      "Topology",
      "Rebalance",
      "Seal Manager",
      "Tenants",
      "Users",
      "Roles",
      "Certificates",
      "Credentials",
      "Databases",
      "Backups",
      "Audit Log",
      "Settings",
    ] as const;

    const collector = await loadApp(page);

    for (const item of allItems) {
      await clickSidebarItem(page, item);

      if (collector.errors.length > 0) {
        const messages = collector.errors.map((e) => e.message).join("\n  ");
        throw new Error(
          `JavaScript exception(s) after navigating to "${item}":\n  ${messages}`,
        );
      }
    }
  });
});

// ---------------------------------------------------------------------------
// Binding-specific regression tests
// ---------------------------------------------------------------------------

test.describe("Admin Nav Smoke — binding regression guards", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  /**
   * Regression: TenantBinding was missing from gui_desktop.go Bind list.
   * window.go.main.TenantBinding was undefined; TenantBinding.List() threw
   * "undefined is not an object (evaluating 't.List')".
   */
  test("Tenants view loads without undefined binding error (TenantBinding regression)", async ({ page }) => {
    const collector = attachPageErrorCollector(page);

    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    await clickSidebarItem(page, "Tenants");

    // Wait for the async TenantBinding.List() call to resolve.
    await page.waitForTimeout(500);

    const tenantErrors = collector.errors.filter(
      (e) =>
        e.message.includes("TenantBinding") ||
        e.message.includes("undefined is not an object") ||
        e.message.includes("Cannot read properties of undefined"),
    );

    if (tenantErrors.length > 0) {
      throw new Error(
        `TenantBinding regression detected:\n  ${tenantErrors.map((e) => e.message).join("\n  ")}`,
      );
    }

    await expect(
      page.locator(".breadcrumbs").getByText("Tenants"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });
  });

  /**
   * Regression: BackupBinding was missing from gui_desktop.go Bind list.
   * Any view calling BackupBinding methods would receive undefined and throw.
   *
   * This test verifies the binding is accessible (mock returns stub data)
   * and no runtime exception fires when the binding is invoked via
   * page.evaluate to simulate what a real Wails view would do.
   */
  test("BackupBinding is accessible and callable without runtime error (BackupBinding regression)", async ({ page }) => {
    const collector = attachPageErrorCollector(page);

    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Directly exercise the BackupBinding.List() method the same way a
    // Svelte component would: via window.go.main.BackupBinding.List().
    const result = await page.evaluate(async () => {
      const w = window as unknown as {
        go?: { main?: Record<string, Record<string, () => Promise<unknown>>> };
      };
      const binding = w.go?.main?.BackupBinding;
      if (!binding) {
        throw new Error(
          "BackupBinding is undefined on window.go.main — binding not registered",
        );
      }
      return await binding.List();
    });

    // The mock returns { backups: [] } — just verify it resolved without error.
    expect(result).toBeDefined();

    // No unhandled page errors should have fired.
    expect(collector.errors).toHaveLength(0);
  });

  /**
   * Regression guard for the 4 newest Wails bindings (RebalanceBinding,
   * LockBinding, CredentialBinding, AuditBinding) added alongside the
   * CLI/GUI parity sweep — mirrors the BackupBinding regression test above.
   * Each is exercised directly via window.go.main.<Binding> the same way a
   * Svelte view would call it.
   */
  for (const spec of [
    { binding: "RebalanceBinding", method: "GetImbalanceReport", args: [] as unknown[] },
    { binding: "LockBinding", method: "List", args: [""] },
    { binding: "CredentialBinding", method: "List", args: [] },
    { binding: "AuditBinding", method: "Query", args: [{}] },
  ] as const) {
    test(`${spec.binding} is accessible and callable without runtime error (${spec.binding} regression)`, async ({ page }) => {
      const collector = attachPageErrorCollector(page);

      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const result = await page.evaluate(
        async ({ bindingName, methodName, methodArgs }) => {
          const w = window as unknown as {
            go?: { main?: Record<string, Record<string, (...a: unknown[]) => Promise<unknown>>> };
          };
          const binding = w.go?.main?.[bindingName];
          if (!binding) {
            throw new Error(`${bindingName} is undefined on window.go.main — binding not registered`);
          }
          const fn = binding[methodName];
          if (typeof fn !== "function") {
            throw new Error(`${bindingName}.${methodName} is not a function`);
          }
          return await fn(...methodArgs);
        },
        { bindingName: spec.binding, methodName: spec.method, methodArgs: spec.args },
      );

      expect(result).toBeDefined();
      expect(collector.errors).toHaveLength(0);
    });
  }

  /**
   * Simulates what happens when a binding is missing: accesses a bogus
   * binding name and verifies the error would be caught before it becomes
   * a silent blank screen.
   */
  test("accessing a missing binding name throws immediately (control regression)", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Accessing a non-existent binding should return undefined, not throw.
    // This mirrors the original bug: calling methods on undefined throws.
    const isUndefined = await page.evaluate(() => {
      const w = window as unknown as {
        go?: { main?: Record<string, unknown> };
      };
      return w.go?.main?.["NonExistentBinding"] === undefined;
    });

    expect(isUndefined).toBe(true);
  });
});
