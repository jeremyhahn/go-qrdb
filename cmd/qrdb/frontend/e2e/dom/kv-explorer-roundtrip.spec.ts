// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * kv-explorer-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Regression for the REST SDK base64 parity bug: GET /kv/scan returns the
 * value map as base64 (Go marshals map[string][]byte as base64), and the REST
 * client previously returned those values verbatim. The KV Explorer drives its
 * list/Details/Edit surfaces from client.kv.scan(), so the UI showed base64
 * instead of plaintext and — because the Edit dialog pre-filled the base64
 * string and Save re-stored it — editing an existing entry corrupted the value
 * into base64-of-base64.
 *
 * This spec boots a real seal:"none" daemon (so the app loads straight into the
 * shell with no barrier gate), creates a KV entry through the real DOM against
 * the live REST backend, and asserts the value is shown as plaintext in every
 * surface and that an edit round trip does not corrupt it into base64.
 */

import { test, expect } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

// "hello plaintext value" base64-encoded — the form that leaked through before
// the fix. It must never appear in the UI.
const PLAINTEXT = "hello plaintext value";
const PLAINTEXT_B64 = "aGVsbG8gcGxhaW50ZXh0IHZhbHVl";
// The edited value and its base64 form (must not appear after the round trip).
const EDITED = "updated plaintext value";
const EDITED_B64 = "dXBkYXRlZCBwbGFpbnRleHQgdmFsdWU=";

const KEY = "chrometest/roundtrip";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Opens the default (kv-engine) database's KV Store tab. */
async function openDefaultKvStore(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto(UI_BASE_URL);
  // App shell loaded (no barrier gate with seal:"none").
  await expect(page.getByText("Seal Manager")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // The fresh daemon ships a single kv-engine database named "system".
  await page
    .locator("aside")
    .getByText("system", { exact: true })
    .first()
    .click();

  // The default tool tab is the KV Store data view.
  await expect(page.getByRole("heading", { name: "KV Store" })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

/** Creates a KV entry via the slide panel and waits for the success toast. */
async function createEntry(
  page: import("@playwright/test").Page,
  key: string,
  value: string,
): Promise<void> {
  await page
    .getByRole("button", { name: "Create", exact: true })
    .first()
    .click();
  await page.locator("input[placeholder='Enter key']").fill(key);
  await page
    .locator("textarea[placeholder='Enter value (supports JSON)']")
    .fill(value);
  await page
    .getByRole("button", { name: "Create", exact: true })
    .last()
    .click();
  await expect(page.getByText(`Created "${key}"`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("KV value round-trips as plaintext through list, Details, and Edit (no base64 corruption)", async ({
  page,
}) => {
  await openDefaultKvStore(page);

  // Create an entry with a known plaintext value.
  await createEntry(page, KEY, PLAINTEXT);

  // 1) The list cell shows the PLAINTEXT value, not its base64 form.
  await expect(page.getByText(PLAINTEXT).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText(PLAINTEXT_B64)).toHaveCount(0);

  // 2) Opening the row Details shows the plaintext value.
  await page.getByText(KEY).first().click();
  const details = page.getByLabel("Details");
  await expect(details.getByText(PLAINTEXT)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(details.getByText(PLAINTEXT_B64)).toHaveCount(0);

  // 3) Opening Edit pre-fills the plaintext value (not base64).
  await page.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByText("Edit Entry")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  const valueArea = page.locator(
    "textarea[placeholder='Enter value (supports JSON)']",
  );
  await expect(valueArea).toHaveValue(PLAINTEXT);

  // 4) Edit the value and save; the re-read (fresh scan) must show the new
  //    plaintext, never base64 — proving no base64 corruption on the edit
  //    round trip (the original bug stored the base64 string verbatim).
  await valueArea.fill(EDITED);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(`Updated "${KEY}"`).last()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  await expect(page.getByText(EDITED).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByText(EDITED_B64)).toHaveCount(0);
  // The stale (pre-edit) plaintext must no longer surface in the list.
  await expect(page.getByText(PLAINTEXT_B64)).toHaveCount(0);
});
