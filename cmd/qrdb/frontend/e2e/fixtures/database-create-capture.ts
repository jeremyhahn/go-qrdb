// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Records the requests the app sends to DatabaseBinding.Create, so a spec can
 * assert what a wizard actually asked the server for rather than only what it
 * showed.
 */

import type { Page } from "@playwright/test";

const DEFAULT_TIMEOUT = 10_000;

/**
 * Wraps the mocked DatabaseBinding.Create so every request it is called with
 * is recorded (as a JSON copy). With `fail`, the call throws after recording,
 * as a server refusal would. Call it before page.goto: it runs as an init
 * script after the Wails mock is injected and before the app boots.
 */
export async function captureDatabaseCreates(
  page: Page,
  options: { fail?: boolean } = {},
): Promise<void> {
  await page.addInitScript((failCreate: boolean) => {
    const w = window as unknown as Record<string, unknown>;
    w.__capturedDbCreateRequests = [];
    const db = (
      w.go as Record<string, Record<string, Record<string, unknown>>>
    )?.main?.DatabaseBinding;
    if (db && typeof db.Create === "function") {
      const orig = db.Create as (...args: unknown[]) => Promise<unknown>;
      db.Create = async (...args: unknown[]): Promise<unknown> => {
        (w.__capturedDbCreateRequests as unknown[]).push(
          JSON.parse(JSON.stringify(args[0])),
        );
        if (failCreate) {
          throw new Error("database: barrier refused for this database");
        }
        return orig(...args);
      };
    }
  }, options.fail ?? false);
}

/** Returns the create requests recorded so far, without waiting. */
export async function databaseCreatesSoFar(
  page: Page,
): Promise<Record<string, unknown>[]> {
  return page.evaluate(
    () =>
      (window as unknown as Record<string, unknown>)
        .__capturedDbCreateRequests as Record<string, unknown>[],
  );
}

/** Waits until at least one create request is recorded, then returns them all. */
export async function capturedDatabaseCreates(
  page: Page,
  timeout: number = DEFAULT_TIMEOUT,
): Promise<Record<string, unknown>[]> {
  await page.waitForFunction(
    () =>
      ((window as unknown as Record<string, unknown>)
        .__capturedDbCreateRequests as unknown[]).length > 0,
    { timeout },
  );
  return databaseCreatesSoFar(page);
}
