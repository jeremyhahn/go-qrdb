// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Regression test: web-mode profile switches are serialized.
 *
 * RestClient.connection.connect changes the client's baseUrl before it
 * awaits /health. Two overlapping profile Connect actions used to interleave
 * on the shared REST client: when A's health response arrived after B had
 * connected, A's continuation recorded A as the connected target while the
 * client pointed at B, so the UI labelled A while reads and destructive
 * operations reached B.
 *
 * The test loads the connection-manager store from the dev server (the same
 * module instance the app uses) and drives connectProfile with a client
 * double whose first connect is held open, then asserts that the second
 * profile's switch does not touch the client until the first has settled.
 */

import { webTest as test, expect } from "./fixtures/web-mode-mock.ts";

const LOAD_TIMEOUT = 10_000;

test.describe("Web mode - profile switches are serialized", () => {
  test("a second connect waits for a delayed first connect to settle", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".breadcrumbs").getByText("Dashboard")).toBeVisible({ timeout: LOAD_TIMEOUT });

    const result = await page.evaluate(async () => {
      const storePath = "/src/stores/connection-manager.svelte.ts";
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const mod: any = await import(/* @vite-ignore */ storePath);

      const calls: string[] = [];
      let connected = false;
      let releaseFirst: () => void = () => undefined;
      const firstHeld = new Promise<void>((resolve) => {
        releaseFirst = resolve;
      });
      let connects = 0;
      let clientUrl = "";

      // Every namespace other than connection answers each call with an
      // empty result, so the probe and topology reads settle at once.
      const anyNamespace = new Proxy(
        {},
        { get: () => async () => ({}) },
      );
      const connection = {
        isConnected: (): boolean => connected,
        disconnect: async (): Promise<void> => {
          calls.push("disconnect");
          connected = false;
        },
        connect: async (url: string): Promise<void> => {
          connects += 1;
          calls.push(`connect:${String(connects)}:${url}`);
          if (connects === 1) await firstHeld;
          connected = true;
          clientUrl = url;
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

      const first = mod.connectProfile(client, "profile-a").catch(() => undefined);
      const second = mod.connectProfile(client, "profile-b").catch(() => undefined);

      // Let every ready continuation run while A's connect is still held.
      for (let i = 0; i < 50; i++) await Promise.resolve();
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      const whileHeld = [...calls];

      releaseFirst();
      await Promise.all([first, second]);
      return {
        whileHeld,
        all: calls,
        clientUrl,
        connected,
        a: mod.connManagerStore.states.get("profile-a")?.status as string | undefined,
        b: mod.connManagerStore.states.get("profile-b")?.status as string | undefined,
      };
    });

    // While A's connect is in flight, B must not disconnect or retarget the
    // shared client.
    expect(result.whileHeld).toEqual(["connect:1:https://a.invalid:63012"]);
    // A's switch finished before B's began, and B really left A's server
    // for its own: the complete disconnect/connect sequence, both URLs.
    expect(result.all).toEqual([
      "connect:1:https://a.invalid:63012",
      "disconnect",
      "connect:2:https://b.invalid:63012",
    ]);
    // After A is released the client targets B and the store says so.
    expect(result.clientUrl).toBe("https://b.invalid:63012");
    expect(result.connected).toBe(true);
    expect(result.b).toBe("connected");
    expect(result.a).toBe("disconnected");
  });
});
