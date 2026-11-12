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

// Real-backend roundtrip for the Credentials admin view (CredentialManager):
// store a named credential against a live qrdbd, see it listed, retrieve
// (reveal) its value, rotate it to a new version with a grace period, list
// both versions and reveal each one, then delete it through the confirm
// modal.

import { test, expect, type Page } from "@playwright/test";
import {
  startDaemon,
  stopDaemon,
  waitForHealthy,
  UI_BASE_URL,
  type DomDaemon,
} from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  // The credential service is part of the seal subsystem: it exists only
  // with seal.enabled, so boot the passthrough ("none") strategy enabled.
  daemon = await startDaemon({ seal: "none-committed" });
  await initializeCeremony(daemon);
  await waitForHealthy(daemon);
});

// Runs the first-boot ceremony (what `qrdb init` does) with the none
// strategy and a server-generated admin credential, which this throwaway
// daemon discards. The Server Initialization wizard cannot do this: it sends
// no SO PIN or user PIN, both of which the ceremony requires.
async function initializeCeremony(d: DomDaemon): Promise<void> {
  const https = await import("https");
  await new Promise<void>((resolve, reject) => {
    const body = JSON.stringify({
      so_pin: "so-pin-dom-e2e",
      user_pin: "user-pin-dom-e2e",
      admin_generate: true,
    });
    const req = https.default.request(
      `${d.adminUrl}/v1/ceremony/initialize`,
      {
        method: "POST",
        rejectUnauthorized: false,
        headers: {
          "Content-Type": "application/json",
          "Content-Length": Buffer.byteLength(body),
        },
      },
      (res) => {
        let text = "";
        res.on("data", (c: Buffer) => (text += c.toString()));
        res.on("end", () => {
          if (res.statusCode && res.statusCode < 300) resolve();
          else
            reject(
              new Error(
                `ceremony initialize returned ${res.statusCode}: ${text.slice(0, 300)}`,
              ),
            );
        });
      },
    );
    req.on("error", reject);
    req.end(body);
  });
}

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

async function openCredentialsView(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: 20_000 });
  await page.getByRole("button", { name: "Credentials", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "New Credential" }),
  ).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

test("credential store + retrieve + rotate + versions + delete round-trip (UI ↔ live daemon)", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  const name = `ui-cred-${Date.now()}`;
  const firstValue = "first-secret-value";
  const secondValue = "second-secret-value";

  await openCredentialsView(page);
  await expect(page.getByText("No credentials found.")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // Store.
  await page.getByRole("button", { name: "New Credential" }).click();
  const createModal = page
    .locator(".modal-box")
    .filter({ hasText: "New Credential" });
  await expect(createModal).toBeVisible();
  await createModal.getByPlaceholder("e.g. db-password").fill(name);
  await createModal
    .getByPlaceholder("credential value (base64 or plain text)")
    .fill(firstValue);
  await createModal.getByRole("button", { name: "Store", exact: true }).click();
  await expect(page.getByText(`Credential "${name}" stored.`)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  // List: the credential is a row; selecting it opens the detail panel.
  const row = page.getByRole("row").filter({ hasText: name });
  await expect(row).toBeVisible({ timeout: LOAD_TIMEOUT });
  await row.click();
  await expect(
    page.getByRole("heading", { name: "Credential Detail" }),
  ).toBeVisible();

  // Retrieve: Reveal shows the stored value.
  await page.getByRole("button", { name: "Reveal", exact: true }).click();
  await expect(page.getByText(firstValue, { exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.getByRole("button", { name: "Hide", exact: true }).click();
  await expect(page.getByText(firstValue, { exact: true })).toHaveCount(0);

  // Rotate with a grace period, so the previous version stays readable.
  await page.getByRole("button", { name: "Rotate", exact: true }).click();
  const rotateModal = page
    .locator(".modal-box")
    .filter({ hasText: "Rotate Credential" });
  await expect(rotateModal).toBeVisible();
  await rotateModal.getByPlaceholder("new credential value").fill(secondValue);
  await rotateModal
    .getByPlaceholder("0 = wipe previous value immediately")
    .fill("3600");
  await rotateModal
    .getByRole("button", { name: "Rotate", exact: true })
    .click();
  const rotated = page.getByText(
    new RegExp(`Credential "${name}" rotated: v(\\d+) -> v(\\d+)\\.`),
  );
  await expect(rotated).toBeVisible({ timeout: LOAD_TIMEOUT });
  const m = /rotated: v(\d+) -> v(\d+)\./.exec(
    (await rotated.textContent()) ?? "",
  );
  expect(m).not.toBeNull();
  const prev = m![1]!;
  const next = m![2]!;
  expect(Number(next)).toBeGreaterThan(Number(prev));

  // Versions: both are listed, the new one is current, each reveals its value.
  const prevRow = page.getByTestId(`credential-version-${prev}`);
  const nextRow = page.getByTestId(`credential-version-${next}`);
  await expect(prevRow).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(nextRow).toBeVisible();
  await expect(nextRow.locator("td").nth(1)).toHaveText("yes");
  await expect(prevRow.locator("td").nth(1)).toHaveText("no");
  await page.getByRole("button", { name: `Reveal version ${next}` }).click();
  await expect(page.getByTestId(`credential-version-value-${next}`)).toHaveText(
    secondValue,
    {
      timeout: LOAD_TIMEOUT,
    },
  );
  await page.getByRole("button", { name: `Reveal version ${prev}` }).click();
  await expect(page.getByTestId(`credential-version-value-${prev}`)).toHaveText(
    firstValue,
    {
      timeout: LOAD_TIMEOUT,
    },
  );

  // Retrieve after rotation returns the new value.
  await page.getByRole("button", { name: "Reveal", exact: true }).click();
  await expect(
    page.locator(".card-body .font-mono.text-xs.bg-base-300"),
  ).toHaveText(secondValue, {
    timeout: LOAD_TIMEOUT,
  });

  // Delete through the confirm modal; the list is empty again.
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  const confirm = page
    .locator(".modal-box")
    .filter({ hasText: "Delete Credential" });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByText(`Credential "${name}" deleted.`)).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await expect(page.getByRole("row").filter({ hasText: name })).toHaveCount(0);
  await expect(page.getByText("No credentials found.")).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });

  expect(errors).toEqual([]);
});
