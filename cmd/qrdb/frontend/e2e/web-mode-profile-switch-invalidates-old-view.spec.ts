// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Regression test: retargeting the shared REST client invalidates the old
 * profile's view first.
 *
 * RestClient.connection.connect marks the client connected after the new
 * server's /health and then awaits its /status. When A was active and the
 * user connected B from the sidebar, A stayed "connected" (so its admin
 * view, breadcrumb and destructive actions stayed usable) while the client
 * already sent every request to B: deleting a credential from A's detail
 * panel could delete B's credential of the same name.
 *
 * The test drives connectProfile with a client double whose connect to B
 * marks the client connected and then holds (B's /status in flight), and
 * asserts that A's view is already disabled when the client is retargeted
 * and while B's connect is held: the app's admin views render only while
 * the active profile's state is "connected".
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Web mode - profile switch invalidates the old view", () => {
  test("A's view is disabled before the client retargets to B and while B's status is held", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const result = await page.evaluate(async () => {
      const storePath = "/src/stores/connection-manager.svelte.ts";
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const mod: any = await import(/* @vite-ignore */ storePath);

      // The view the app renders for the active profile is usable only while
      // that profile's state is "connected" (MainApp's connected gate).
      const activeViewUsable = (): boolean =>
        mod.connManagerStore.states.get(mod.connManagerStore.activeProfileId)?.status === "connected";

      const calls: string[] = [];
      let connected = true;
      let releaseStatus: () => void = () => undefined;
      const statusHeld = new Promise<void>((resolve) => {
        releaseStatus = resolve;
      });
      let markStatusPending: () => void = () => undefined;
      const statusPending = new Promise<void>((resolve) => {
        markStatusPending = resolve;
      });

      const anyNamespace = new Proxy(
        {},
        { get: () => async () => ({}) },
      );
      const connection = {
        isConnected: (): boolean => connected,
        disconnect: async (): Promise<void> => {
          calls.push(`disconnect:viewUsable=${String(activeViewUsable())}`);
          connected = false;
        },
        connect: async (url: string): Promise<void> => {
          calls.push(`connect:${url}:viewUsable=${String(activeViewUsable())}`);
          // B answered /health: the client now sends requests to B ...
          connected = true;
          markStatusPending();
          // ... while B's /status is still in flight.
          await statusHeld;
        },
      };
      const client = new Proxy(
        { connection },
        {
          get: (target, prop) =>
            prop in target ? (target as Record<string | symbol, unknown>)[prop] : anyNamespace,
        },
      );

      mod.connManagerStore.profiles = [
        { id: "profile-a", name: "A", type: "external", external: { address: "https://a.invalid:63012" } },
        { id: "profile-b", name: "B", type: "external", external: { address: "https://b.invalid:63012" } },
      ];
      mod.connManagerStore.activeProfileId = "profile-a";
      mod.connManagerStore.states = new Map([
        ["profile-a", { profileId: "profile-a", status: "connected", isEmbedded: false }],
      ]);
      const usableBefore = activeViewUsable();

      const switching = mod.connectProfile(client, "profile-b").catch(() => undefined);
      await statusPending;
      const usableWhileHeld = activeViewUsable();
      const aWhileHeld: string | undefined = mod.connManagerStore.states.get("profile-a")?.status;

      releaseStatus();
      await switching;
      return {
        usableBefore,
        usableWhileHeld,
        aWhileHeld,
        calls,
        bAfter: mod.connManagerStore.states.get("profile-b")?.status as string | undefined,
      };
    });

    expect(result.usableBefore).toBe(true);
    // A's view was disabled before the client left A ...
    expect(result.calls).toEqual([
      "disconnect:viewUsable=false",
      "connect:https://b.invalid:63012:viewUsable=false",
    ]);
    // ... and stays disabled while B's /status is held.
    expect(result.usableWhileHeld).toBe(false);
    expect(result.aWhileHeld).not.toBe("connected");
    expect(result.bAfter).toBe("connected");
  });
});
