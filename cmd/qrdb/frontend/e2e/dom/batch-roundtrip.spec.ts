// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * batch-roundtrip.spec.ts (real-backend DOM tier)
 *
 * Drives the Batch view against a live seal:"none" daemon: a batch of puts
 * and a delete executes through the real DOM, the server is then read back
 * over REST to prove each put landed and the delete removed its key, and a
 * batch the server refuses surfaces the server's own error in the UI.
 *
 * The Import tab, in a browser, takes a picked file and streams it over the
 * server's chunked import session: a file in each format the server lists
 * (GET /batch/import/formats) lands its keys on the server, and a malformed
 * file shows the server's parse error.
 */

import { test, expect, type Page } from "@playwright/test";
import { startDaemon, stopDaemon, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

const LOAD_TIMEOUT = 15_000;
const DATABASE = "system";

let daemon: DomDaemon | null = null;

test.beforeEach(async () => {
  daemon = await startDaemon({ seal: "none" });
});

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

/** Opens the default database's Batch tool. */
async function openBatch(page: Page): Promise<void> {
  await page.goto(UI_BASE_URL);
  await expect(page.getByText("Seal Manager")).toBeVisible({ timeout: LOAD_TIMEOUT });
  await page.locator("aside").getByText(DATABASE, { exact: true }).first().click();
  await page.getByRole("tab", { name: "Batch", exact: true }).or(
    page.getByRole("button", { name: "Batch", exact: true }),
  ).first().click();
  await expect(page.getByRole("heading", { name: "Batch", exact: true })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.locator(".toast .alert").filter({ hasText: text }).first()).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
}

type OpType = "put" | "get" | "delete" | "delete_range";

/** Appends one operation row to the builder and fills it in. */
async function addOp(page: Page, type: OpType, key: string, extra = ""): Promise<void> {
  await page.getByRole("button", { name: "Add Operation" }).click();
  const row = page.locator("select.select").filter({ has: page.locator("option[value]") }).last();
  await row.selectOption({ label: type });
  await page.getByPlaceholder("Key", { exact: true }).last().fill(key);
  if (type === "put") {
    await page.getByPlaceholder("Value", { exact: true }).last().fill(extra);
  } else if (type === "delete_range") {
    await page.getByPlaceholder("End key (exclusive)").last().fill(extra);
  }
}

/** Reads a key straight from the server; null when it does not exist. */
async function serverGet(page: Page, key: string): Promise<string | null> {
  const resp = await page.request.get(`${daemon!.adminUrl}/kv/${encodeURIComponent(key)}`, {
    headers: { "X-QRDB-Database": DATABASE },
    ignoreHTTPSErrors: true,
  });
  if (resp.status() === 404) return null;
  expect(resp.ok(), `GET ${key}: HTTP ${String(resp.status())}`).toBe(true);
  return resp.text();
}

/** Writes a key straight to the server. */
async function serverPut(page: Page, key: string, value: string): Promise<void> {
  const resp = await page.request.put(`${daemon!.adminUrl}/kv/${encodeURIComponent(key)}`, {
    headers: { "X-QRDB-Database": DATABASE },
    data: value,
    ignoreHTTPSErrors: true,
  });
  expect(resp.ok(), `PUT ${key}: HTTP ${String(resp.status())}`).toBe(true);
}

test("a batch of puts and a delete lands on the server", async ({ page }) => {
  await serverPut(page, "batch/doomed", "old");

  await openBatch(page);
  await addOp(page, "put", "batch/a", "alpha");
  await addOp(page, "put", "batch/b", "bravo");
  await addOp(page, "put", "batch/c", "charlie");
  await addOp(page, "delete", "batch/doomed");
  await page.getByRole("button", { name: "Execute Batch" }).click();
  await expectToast(page, "Batch executed successfully.");
  await expect(page.getByText("Batch Results")).toBeVisible({ timeout: LOAD_TIMEOUT });

  expect(await serverGet(page, "batch/a")).toBe("alpha");
  expect(await serverGet(page, "batch/b")).toBe("bravo");
  expect(await serverGet(page, "batch/c")).toBe("charlie");
  expect(await serverGet(page, "batch/doomed")).toBeNull();
});

test("a batch the server refuses shows the server's error", async ({ page }) => {
  await openBatch(page);
  // A range whose end sorts before its start is refused by the server.
  await addOp(page, "delete_range", "batch/z", "batch/a");
  const refused = page.waitForResponse(
    (r) => new URL(r.url()).pathname.endsWith("/batch") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Execute Batch" }).click();
  const resp = await refused;
  expect(resp.ok(), "the server must refuse this batch").toBe(false);
  const body = (await resp.json()) as { error?: string; message?: string };
  const serverMessage = body.error ?? body.message ?? "";
  expect(serverMessage).not.toBe("");
  await expect(
    page.locator(".toast .alert-error").filter({ hasText: serverMessage }).first(),
  ).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(page.getByText("Batch executed successfully.")).toHaveCount(0);
  expect(await serverGet(page, "batch/z")).toBeNull();
});


// ---------------------------------------------------------------------------
// Import from the browser: a picked file streams over the chunked import
// session (begin / append / commit) and lands on the server.
// ---------------------------------------------------------------------------

/** Two records, keyed by id, for the format under test. */
function importRows(format: string): Array<{ id: string; name: string }> {
  return [
    { id: `imp-${format}/1`, name: `${format}-alpha` },
    { id: `imp-${format}/2`, name: `${format}-bravo` },
  ];
}

/** A plain (ustar) tar holding one file, the archive format's simplest case. */
function tarOf(name: string, content: Buffer): Buffer {
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, "utf8");
  header.write("0000644\0", 100, 8, "ascii");
  header.write("0000000\0", 108, 8, "ascii");
  header.write("0000000\0", 116, 8, "ascii");
  header.write(content.length.toString(8).padStart(11, "0") + "\0", 124, 12, "ascii");
  header.write("00000000000\0", 136, 12, "ascii");
  header.write("        ", 148, 8, "ascii");
  header.write("0", 156, 1, "ascii");
  header.write("ustar\0", 257, 6, "ascii");
  header.write("00", 263, 2, "ascii");
  let sum = 0;
  for (const b of header) sum += b;
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  const padded = Buffer.alloc(Math.ceil(content.length / 512) * 512);
  content.copy(padded);
  return Buffer.concat([header, padded, Buffer.alloc(1024)]);
}

/** The file to pick for one import format: its name and its bytes. */
function importFileFor(format: string): { name: string; mimeType: string; buffer: Buffer } {
  const rows = importRows(format);
  const json = Buffer.from(JSON.stringify(rows));
  switch (format) {
    case "csv":
      return {
        name: "rows.csv",
        mimeType: "text/csv",
        buffer: Buffer.from(["id,name", ...rows.map((r) => `${r.id},${r.name}`)].join("\n") + "\n"),
      };
    case "json":
      return { name: "rows.json", mimeType: "application/json", buffer: json };
    case "auto":
      // auto infers the format from the filename the browser reports.
      return { name: "rows-auto.json", mimeType: "application/json", buffer: json };
    case "ndjson":
      return {
        name: "rows.ndjson",
        mimeType: "application/x-ndjson",
        buffer: Buffer.from(rows.map((r) => JSON.stringify(r)).join("\n") + "\n"),
      };
    case "yaml":
      return {
        name: "rows.yaml",
        mimeType: "application/yaml",
        buffer: Buffer.from(rows.map((r) => `- id: "${r.id}"\n  name: "${r.name}"`).join("\n") + "\n"),
      };
    case "archive":
      return { name: "rows.tar", mimeType: "application/x-tar", buffer: tarOf("rows.json", json) };
    default:
      throw new Error(`no import fixture for format "${format}" the server lists`);
  }
}

/** Opens the Import tab of the Batch view. */
async function openImport(page: Page): Promise<void> {
  await openBatch(page);
  await page.getByRole("button", { name: "Import", exact: true }).or(
    page.getByRole("tab", { name: "Import", exact: true }),
  ).first().click();
  await expect(page.getByTestId("import-file-input")).toBeVisible({ timeout: LOAD_TIMEOUT });
}

/** Picks a file, sets the format and key field, and runs the import. */
async function importThroughUI(
  page: Page,
  format: string,
  file: { name: string; mimeType: string; buffer: Buffer },
): Promise<void> {
  await page.getByTestId("import-file-input").setInputFiles(file);
  await page.locator("#import-format").selectOption(format);
  await page.locator("#import-key-field").fill("id");
  await page.locator("button.btn-primary", { hasText: /^\s*Import\s*$/ }).click();
}

test("a file in each listed format imports through the file input", async ({ page }) => {
  const resp = await page.request.get(`${daemon!.adminUrl}/batch/import/formats`, { ignoreHTTPSErrors: true });
  expect(resp.ok(), `GET /batch/import/formats: HTTP ${String(resp.status())}`).toBe(true);
  const { formats } = (await resp.json()) as { formats: string[] };
  expect(formats.length).toBeGreaterThan(0);

  test.setTimeout(30_000 + formats.length * LOAD_TIMEOUT);

  await openImport(page);
  for (const format of formats) {
    // The previous format's toast must be gone, so this one's is fresh.
    await expect(page.locator(".toast .alert")).toHaveCount(0, { timeout: LOAD_TIMEOUT });
    await importThroughUI(page, format, importFileFor(format));
    await expectToast(page, "Imported 2 of 2 records.");
    for (const row of importRows(format)) {
      const value = await serverGet(page, row.id);
      expect(value, `${format}: ${row.id}`).not.toBeNull();
      expect(value, `${format}: ${row.id}`).toContain(row.name);
    }
    await page.getByRole("button", { name: "Reset", exact: true }).click();
  }
});

test("a malformed import file shows the server's error", async ({ page }) => {
  await openImport(page);
  await importThroughUI(page, "json", {
    name: "broken.json",
    mimeType: "application/json",
    buffer: Buffer.from('[{"id": "imp-bad/1", "name": '),
  });
  // The server's parse refusal, carried through the commit's error body.
  const toast = page.locator(".toast .alert-error").first();
  await expect(toast).toBeVisible({ timeout: LOAD_TIMEOUT });
  await expect(toast).toContainText(/json/i);
  await expect(toast).not.toContainText("{\"error\"");
  expect(await serverGet(page, "imp-bad/1")).toBeNull();
});
