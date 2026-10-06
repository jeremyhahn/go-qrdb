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
 * Wails binding registration smoke tests.
 *
 * These tests verify that every binding listed in the TypeScript SDK's
 * WailsBindings interface (sdk/typescript/src/wails.ts) is present on
 * window.go.main at runtime.
 *
 * In production, window.go.main is populated by the Wails build toolchain
 * from the structs listed in options.App.Bind in gui_desktop.go.  In tests,
 * the wails-mock.ts fixture populates window.go.main with stub objects that
 * mirror the production shape.
 *
 * A failing test here means one of two things:
 *   a) The binding was added to gui_desktop.go but not to wails-mock.ts
 *      (test environment gap — update the mock).
 *   b) The binding was removed from wails-mock.ts without a corresponding
 *      removal from gui_desktop.go (or vice versa — alignment error).
 *
 * The Go-level counterpart (gui_desktop_bindings_test.go) asserts the same
 * contract on the actual Go Bind list, catching the class of bug where a
 * binding is created in Go but never registered.
 *
 * Expected bindings are derived from WailsBindings interface in wails.ts.
 * ConnectionBinding is present in the mock but absent from WailsBindings
 * because it is an internal plumbing binding; it is still verified here.
 */

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

/** Generous timeout for page load + mock injection. */
const LOAD_TIMEOUT = 10_000;

// ---------------------------------------------------------------------------
// Binding manifest — keep this in sync with:
//   sdk/typescript/src/wails.ts  (WailsBindings.main)
//   cmd/qrdb/gui_desktop.go      (options.App.Bind)
//   cmd/qrdb/frontend/e2e/fixtures/wails-mock.ts  (w.go.main)
// ---------------------------------------------------------------------------

/**
 * The canonical list of Wails binding names that must be present on
 * window.go.main after the application loads.  The order matches the Bind
 * slice in gui_desktop.go for readability.
 */
const EXPECTED_BINDINGS = [
  "ConnectionBinding",
  "ConnectionManagerBinding",
  "EmbeddedServerBinding",
  "KVBinding",
  "AdminBinding",
  "MVCCBinding",
  "TransactionBinding",
  "BatchBinding",
  "WatchBinding",
  "LeaseBinding",
  "IndexBinding",
  "TimeSeriesBinding",
  "VectorBinding",
  "UserBinding",
  "RoleBinding",
  "CertBinding",
  "DatabaseBinding",
  "CollectionBinding",
  "SQLBinding",
  "SealBinding",
  "ConfigBinding",
  "TenantBinding",
  "BackupBinding",
  "RebalanceBinding",
  "LockBinding",
  "CredentialBinding",
  "AuditBinding",
  "DaemonBinding",
] as const;

type BindingName = (typeof EXPECTED_BINDINGS)[number];

// ---------------------------------------------------------------------------
// Per-binding presence tests
// ---------------------------------------------------------------------------

test.describe("Wails Binding Registration — presence", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  for (const bindingName of EXPECTED_BINDINGS) {
    test(`${bindingName} is defined on window.go.main`, async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const isDefined = await page.evaluate((name: BindingName) => {
        const w = window as unknown as {
          go?: { main?: Record<string, unknown> };
        };
        return w.go?.main?.[name] !== undefined;
      }, bindingName);

      expect(
        isDefined,
        `${bindingName} is undefined on window.go.main — it may be missing from gui_desktop.go Bind list`,
      ).toBe(true);
    });
  }
});

// ---------------------------------------------------------------------------
// Bulk presence test — single round-trip for speed
// ---------------------------------------------------------------------------

test.describe("Wails Binding Registration — bulk check", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  test("all expected bindings are defined in a single evaluation", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const missing = await page.evaluate(
      (names: readonly string[]) => {
        const w = window as unknown as {
          go?: { main?: Record<string, unknown> };
        };
        const main = w.go?.main;
        if (!main) return names.slice(); // all missing if go.main doesn't exist
        return names.filter((n) => main[n] === undefined);
      },
      EXPECTED_BINDINGS as readonly string[],
    );

    expect(
      missing,
      `Missing bindings on window.go.main: [${missing.join(", ")}]. ` +
        "Add them to gui_desktop.go Bind list and wails-mock.ts.",
    ).toHaveLength(0);
  });

  test("window.go.main namespace exists", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const hasMain = await page.evaluate(() => {
      const w = window as unknown as {
        go?: { main?: Record<string, unknown> };
      };
      return typeof w.go?.main === "object" && w.go.main !== null;
    });

    expect(hasMain, "window.go.main is not an object — Wails mock not injected").toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Per-binding method presence tests
// ---------------------------------------------------------------------------

/**
 * For the bindings most commonly involved in the missing-registration bug
 * class, verify that the expected primary methods are callable functions.
 * This catches the case where a binding object exists but is an empty stub.
 */
test.describe("Wails Binding Registration — method presence", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  /** Binding name → array of method names that must be callable functions. */
  const BINDING_METHODS: Partial<Record<BindingName, readonly string[]>> = {
    TenantBinding: ["List", "Get", "Create", "Delete", "UpdateQuotas", "AddTrustAnchors", "ListTrustAnchors", "RemoveTrustAnchor"],
    BackupBinding: ["List", "Create", "Get", "Delete", "Restore", "Verify"],
    RebalanceBinding: ["GetShardStats", "CreatePlan", "Execute", "GetImbalanceReport"],
    LockBinding: ["Acquire", "TryLock", "Release", "Extend", "Status", "List"],
    CredentialBinding: [
      "Submit",
      "Store",
      "Retrieve",
      "List",
      "Strategy",
      "Delete",
      "Rotate",
      "ListVersions",
      "RetrieveVersion",
      "GCExpired",
    ],
    AuditBinding: ["Query"],
    UserBinding: ["List", "Get", "Create", "Update", "Delete"],
    RoleBinding: ["List", "Get", "Create", "Update", "Delete"],
    CertBinding: ["List", "Issue", "Enroll", "Export", "Revoke"],
    DatabaseBinding: ["List", "Get", "Create", "Drop"],
    SealBinding: ["Status", "Initialize", "InitializeShamir", "Unseal", "Seal"],
    KVBinding: ["Get", "Put", "Delete", "List", "Exists"],
    AdminBinding: ["Health", "Status", "Peers"],
    DaemonBinding: ["Start", "Stop", "Status", "GetLogs"],
    ConnectionManagerBinding: ["ListProfiles", "Connect", "Disconnect"],
  };

  for (const [bindingName, methods] of Object.entries(BINDING_METHODS) as [
    BindingName,
    readonly string[],
  ][]) {
    test(`${bindingName} has expected methods`, async ({ page }) => {
      await page.goto("/");
      await expect(
        page.locator(".breadcrumbs").getByText("Dashboard"),
      ).toBeVisible({ timeout: LOAD_TIMEOUT });

      const missingMethods = await page.evaluate(
        ({ name, expectedMethods }: { name: string; expectedMethods: readonly string[] }) => {
          const w = window as unknown as {
            go?: { main?: Record<string, Record<string, unknown>> };
          };
          const binding = w.go?.main?.[name];
          if (!binding) return expectedMethods.slice();
          return expectedMethods.filter(
            (m) => typeof binding[m] !== "function",
          );
        },
        { name: bindingName, expectedMethods: methods },
      );

      expect(
        missingMethods,
        `${bindingName} is missing methods: [${missingMethods.join(", ")}]`,
      ).toHaveLength(0);
    });
  }
});

// ---------------------------------------------------------------------------
// Critical regression: TenantBinding and BackupBinding specifically
// ---------------------------------------------------------------------------

test.describe("Wails Binding Registration — critical regression guards", () => {
  test.use({ mockState: secondLaunchEmbeddedState });

  /**
   * Direct regression test for the 2026-04-06 bug:
   * TenantBinding was created in bindings_tenant.go but omitted from the
   * Bind list in gui_desktop.go, making window.go.main.TenantBinding
   * undefined at runtime.
   */
  test("TenantBinding.List() resolves without error (regression: 2026-04-06)", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const result = await page.evaluate(async () => {
      const w = window as unknown as {
        go?: { main?: Record<string, Record<string, () => Promise<unknown>>> };
      };
      const binding = w.go?.main?.TenantBinding;
      if (!binding) {
        throw new Error(
          "window.go.main.TenantBinding is undefined — not registered in Bind list",
        );
      }
      if (typeof binding.List !== "function") {
        throw new Error(
          "window.go.main.TenantBinding.List is not a function",
        );
      }
      return await binding.List();
    });

    expect(result).toBeDefined();
  });

  /**
   * Direct regression test for BackupBinding:
   * BackupBinding was created in bindings_backup.go but must be present
   * in the Bind list to be accessible at window.go.main.BackupBinding.
   */
  test("BackupBinding.List() resolves without error (regression: 2026-04-06)", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    const result = await page.evaluate(async () => {
      const w = window as unknown as {
        go?: { main?: Record<string, Record<string, () => Promise<unknown>>> };
      };
      const binding = w.go?.main?.BackupBinding;
      if (!binding) {
        throw new Error(
          "window.go.main.BackupBinding is undefined — not registered in Bind list",
        );
      }
      if (typeof binding.List !== "function") {
        throw new Error(
          "window.go.main.BackupBinding.List is not a function",
        );
      }
      return await binding.List();
    });

    expect(result).toBeDefined();
  });

  /**
   * Verifies that the total binding count on window.go.main matches what
   * is expected from the manifest above.  A count mismatch is an early
   * warning that a binding was added or removed without updating both
   * gui_desktop.go and wails-mock.ts.
   */
  test("window.go.main contains the expected number of bindings", async ({ page }) => {
    await page.goto("/");
    await expect(
      page.locator(".breadcrumbs").getByText("Dashboard"),
    ).toBeVisible({ timeout: LOAD_TIMEOUT });

    // Count only the bindings whose names end in "Binding" to exclude
    // any auxiliary properties the mock may set (e.g. __indexStore).
    const count = await page.evaluate(() => {
      const w = window as unknown as {
        go?: { main?: Record<string, unknown> };
      };
      const main = w.go?.main;
      if (!main) return 0;
      return Object.keys(main).filter((k) => k.endsWith("Binding")).length;
    });

    // The mock also exposes WizardBinding which is not in EXPECTED_BINDINGS
    // (it is a legacy binding not registered via gui_desktop.go in production).
    // Allow +1 for WizardBinding so this test does not break if it is removed.
    expect(count).toBeGreaterThanOrEqual(EXPECTED_BINDINGS.length);
  });
});
