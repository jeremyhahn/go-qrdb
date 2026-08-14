// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

import {
  test,
  expect,
  secondLaunchEmbeddedState,
} from "./fixtures/wails-mock.ts";

const LOAD_TIMEOUT = 10_000;

const indexState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "idx-db", engine: "kv", backend: "pebble", status: "open" },
  ],
};

// ---------------------------------------------------------------------------
// Navigation helpers
// ---------------------------------------------------------------------------

/** Navigate to the Collections tab for the idx-db database. */
async function navigateToCollections(page: import("@playwright/test").Page): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("idx-db").click();
  await expect(page.locator(".breadcrumbs").getByText("idx-db", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  await page.locator("main nav").getByText("Collections", { exact: true }).click();
}

/** Create a collection named "users" and navigate to the Indexes inner tab. */
async function navigateToIndexSection(page: import("@playwright/test").Page): Promise<void> {
  await navigateToCollections(page);

  // Create a collection named "users"
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await page.locator("input[placeholder='Enter collection name']").fill("users");
  await page.locator(".modal").getByRole("button", { name: "Create" }).click();
  await expect(page.getByText('Collection "users" created.')).toBeVisible({ timeout: 5_000 });

  // Select the collection (use exact match to avoid matching the toast)
  await page.getByRole("button", { name: /users.*docs/ }).click();

  // Navigate to the Indexes inner tab (scoped to the collection detail tablist,
  // which uses the "tabs tabs-bordered" class — distinct from the workspace TabBar).
  await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
  await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
}

/** Create a collection, select it, switch to Indexes, and create an index on "email". */
async function setupWithEmailIndex(page: import("@playwright/test").Page): Promise<void> {
  await navigateToIndexSection(page);

  await page.getByRole("button", { name: "Create Index" }).click();
  await page.locator("input[placeholder='Field name']").fill("email");
  await page.locator(".modal").getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(/Index "idx-db\.users\.email" created/)).toBeVisible({ timeout: 5_000 });
}

/**
 * Insert a document into the "users" collection via the Documents tab.
 * Switches to Documents tab, opens the Insert panel, fills in raw JSON,
 * then switches back to the caller's desired tab.
 */
async function insertDocument(
  page: import("@playwright/test").Page,
  json: string,
): Promise<void> {
  // Ensure we are on the Documents inner tab.
  await page.getByRole("tab", { name: "Documents" }).click();
  // Wait for the toolbar Insert button (scoped outside any modal).
  const insertToolbarBtn = page.locator("main").getByRole("button", { name: "Insert", exact: true });
  await expect(insertToolbarBtn).toBeVisible({ timeout: 5_000 });
  await insertToolbarBtn.click();

  // Wait for the Insert Document modal to open.
  const modal = page.locator(".modal");
  await expect(modal).toBeVisible({ timeout: 5_000 });

  // Switch to JSON mode to insert raw JSON (the FormModal defaults to Form mode).
  await modal.getByRole("button", { name: "JSON", exact: true }).click();

  // Fill in the JSON textarea.
  await modal.locator("textarea").fill(json);

  // Submit — the FormModal uses "Insert" as the submit label in create mode.
  await modal.getByRole("button", { name: "Insert", exact: true }).click();
  // Use first() since multiple "Document inserted." toasts may be visible from prior calls.
  await expect(page.getByText("Document inserted.").first()).toBeVisible({ timeout: 5_000 });
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

test.describe("Collection Indexes", () => {
  test.use({ mockState: indexState });

  // -------------------------------------------------------------------------
  // Manage sub-tab
  // -------------------------------------------------------------------------

  test("shows empty index list on manage tab", async ({ page }) => {
    await navigateToIndexSection(page);

    await expect(page.getByRole("button", { name: "Create Index" })).toBeVisible();
    await expect(page.getByText("No indexes defined.")).toBeVisible();
  });

  test("can create an index on a collection field", async ({ page }) => {
    await navigateToIndexSection(page);

    // Open the Create Index modal.
    await page.getByRole("button", { name: "Create Index" }).click();
    await expect(page.locator(".modal").getByText("Create Index")).toBeVisible();

    // Verify the auto-generated name preview is visible.
    await expect(page.locator(".modal").getByText(/idx-db\.users\./)).toBeVisible();

    // Fill in the field name.
    await page.locator("input[placeholder='Field name']").fill("email");

    // Submit.
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();

    // Success toast uses the full auto-generated name.
    await expect(page.getByText(/Index "idx-db\.users\.email" created/)).toBeVisible({ timeout: 5_000 });

    // The field name column in the manage table shows "email".
    await expect(page.getByRole("cell", { name: "email" }).first()).toBeVisible({ timeout: 5_000 });
  });

  test("create index with empty field name shows warning", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("button", { name: "Create Index" }).click();

    // Submit without filling in the field name.
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();

    await expect(page.getByText("Field name is required.")).toBeVisible({ timeout: 5_000 });
  });

  test("index name preview shown in modal", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("button", { name: "Create Index" }).click();

    // The preview text shows the auto-generated name format even before typing.
    await expect(page.locator(".modal").getByText(/idx-db\.users\./)).toBeVisible();
  });

  test("can drop an index", async ({ page }) => {
    await setupWithEmailIndex(page);

    // Click Drop on the index row (exact match to avoid collection drop buttons).
    await page.getByRole("button", { name: "Drop", exact: true }).click();

    // Confirm in the modal.
    await expect(page.locator(".modal").getByRole("heading", { name: "Drop Index" })).toBeVisible();
    await page.locator(".modal").getByRole("button", { name: "Drop" }).click();

    // Empty state reappears.
    await expect(page.getByText("No indexes defined.")).toBeVisible({ timeout: 5_000 });
  });

  test("created index appears in query and scan dropdowns", async ({ page }) => {
    await setupWithEmailIndex(page);

    // Query tab — dropdown contains an "email" option.
    await page.getByRole("tab", { name: "Query" }).click();
    await expect(page.locator("option", { hasText: "email" }).first()).toBeAttached();

    // Scan tab — dropdown also contains "email".
    await page.getByRole("tab", { name: "Scan" }).click();
    await expect(page.locator("option", { hasText: "email" }).first()).toBeAttached();
  });

  test("refresh button reloads indexes", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("button", { name: "Refresh indexes" }).click();

    // Manage tab content still visible after refresh.
    await expect(page.getByRole("button", { name: "Create Index" })).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Query sub-tab
  // -------------------------------------------------------------------------

  test("switch to query tab shows dropdown and value input", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("tab", { name: "Query" }).click();

    await expect(page.locator("select").filter({ hasText: "-- select index --" })).toBeVisible();
    await expect(page.locator("input[placeholder='Value to match']")).toBeVisible();
    await expect(page.getByRole("button", { name: "Query" })).toBeVisible();
    // Before any query is submitted the "No results" placeholder is shown.
    await expect(page.getByText("No results")).toBeVisible();
  });

  test("query with no index selected shows warning", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("tab", { name: "Query" }).click();

    // Provide a value but leave the index unselected.
    await page.locator("input[placeholder='Value to match']").fill("test@example.com");
    await page.getByRole("button", { name: "Query" }).click();

    await expect(page.getByText("Select an index and provide a value to query.")).toBeVisible({ timeout: 5_000 });
  });

  // -------------------------------------------------------------------------
  // Scan sub-tab
  // -------------------------------------------------------------------------

  test("switch to scan tab shows dropdown and range inputs", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("tab", { name: "Scan" }).click();

    await expect(page.locator("select").filter({ hasText: "-- select index --" })).toBeVisible();
    await expect(page.locator("input[placeholder='Start value']")).toBeVisible();
    await expect(page.locator("input[placeholder='End value']")).toBeVisible();
    await expect(page.getByRole("button", { name: "Scan" })).toBeVisible();
    // Before any scan is submitted the "No entries in range" placeholder is shown.
    await expect(page.getByText("No entries in range").first()).toBeVisible();
  });

  test("scan with no index selected shows warning", async ({ page }) => {
    await navigateToIndexSection(page);

    await page.getByRole("tab", { name: "Scan" }).click();

    // Fill range inputs but leave index unselected.
    await page.locator("input[placeholder='Start value']").fill("a");
    await page.locator("input[placeholder='End value']").fill("z");
    await page.getByRole("button", { name: "Scan" }).click();

    await expect(page.getByText("Select an index and provide start and end values.")).toBeVisible({ timeout: 5_000 });
  });

  // -------------------------------------------------------------------------
  // Navigation / visibility
  // -------------------------------------------------------------------------

  test("indexes tab is not visible when no collection is selected", async ({ page }) => {
    await navigateToCollections(page);

    // No collection is selected — the collection detail tablist (with "Indexes" tab)
    // should not be rendered. The workspace-level TabBar has a different structure.
    await expect(page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" })).not.toBeVisible();
  });

  test("switching to Documents tab hides index content", async ({ page }) => {
    await navigateToIndexSection(page);

    // Indexes tab is active — inner Manage/Query/Scan tabs are visible.
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible();

    // Switch back to Documents tab.
    await page.getByRole("tab", { name: "Documents" }).click();

    // Index inner tabs no longer visible.
    await expect(page.getByRole("tab", { name: "Manage" })).not.toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Realistic workflow tests (inverted-index mock behaviour)
  // -------------------------------------------------------------------------

  test("creates indexes and queries documents by field value", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create the "users" collection.
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("users");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "users" created.')).toBeVisible({ timeout: 5_000 });

    // 2. Select the collection so the right panel appears.
    await page.getByRole("button", { name: /users.*docs/ }).click();

    // 3. Insert 3 documents via the Documents tab.
    await insertDocument(page, '{"f1":"test","f2":123,"f3":true}');
    await insertDocument(page, '{"f1":"test2","f2":123,"f3":true}');
    await insertDocument(page, '{"f1":"test3","f2":1234,"f3":true}');

    // 4. Switch to Indexes tab and create index on field "f2".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("f2");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.users\.f2" created/)).toBeVisible({ timeout: 5_000 });

    // 5. Verify index appears in the manage table.
    await expect(page.getByRole("cell", { name: "f2" }).first()).toBeVisible({ timeout: 5_000 });

    // 6. Switch to Query sub-tab, select f2 index, query for value "123".
    await page.getByRole("tab", { name: "Query" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "f2" });
    await page.locator("input[placeholder='Value to match']").fill("123");
    await page.getByRole("button", { name: "Query" }).click();

    // 7. Verify 2 results are returned (both docs with f2=123).
    const resultRows = page.locator("table tbody tr");
    await expect(resultRows).toHaveCount(2, { timeout: 5_000 });
  });

  test("scans index range and returns matching entries", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create collection and insert 3 documents.
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("users");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "users" created.')).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: /users.*docs/ }).click();

    await insertDocument(page, '{"f1":"test","f2":123,"f3":true}');
    await insertDocument(page, '{"f1":"test2","f2":123,"f3":true}');
    await insertDocument(page, '{"f1":"test3","f2":1234,"f3":true}');

    // 2. Create index on "f2".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("f2");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.users\.f2" created/)).toBeVisible({ timeout: 5_000 });

    // 3. Switch to Scan sub-tab, select f2 index, scan range "1" to "200".
    await page.getByRole("tab", { name: "Scan" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "f2" });
    await page.locator("input[placeholder='Start value']").fill("1");
    await page.locator("input[placeholder='End value']").fill("200");
    await page.getByRole("button", { name: "Scan" }).click();

    // 4. Verify the scan results show Value and Document IDs columns.
    const scanTable = page.locator("table").filter({ has: page.getByRole("columnheader", { name: "Value" }) });
    await expect(scanTable.getByRole("columnheader", { name: "Value" })).toBeVisible({ timeout: 5_000 });
    await expect(scanTable.getByRole("columnheader", { name: "Document IDs" })).toBeVisible({ timeout: 5_000 });

    // 5. The value "123" should appear (2 matching documents).
    await expect(scanTable.getByRole("cell", { name: "123", exact: true })).toBeVisible({ timeout: 5_000 });
  });

  test("unique index prevents duplicate values", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create collection.
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("users");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "users" created.')).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: /users.*docs/ }).click();

    // 2. Insert first document with email field.
    await insertDocument(page, '{"email":"alice@test.com"}');

    // 3. Create unique index on "email".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("email");
    // Tick the unique checkbox.
    const uniqueCheckbox = page.locator("input[type='checkbox']").filter({ hasText: /unique/i }).first();
    if (await uniqueCheckbox.isVisible({ timeout: 1_000 }).catch(() => false)) {
      await uniqueCheckbox.check();
    } else {
      // Checkbox may be labelled differently; find by proximity to "Unique" label.
      await page.locator("label").filter({ hasText: /unique/i }).locator("input[type='checkbox']").check();
    }
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.users\.email" created/)).toBeVisible({ timeout: 5_000 });

    // 4. Try to insert a second document with the same email — expect error toast.
    await insertDocument(page, '{"email":"alice@test.com"}');
    await expect(page.getByText(/unique constraint violation/i)).toBeVisible({ timeout: 5_000 });
  });

  test("sparse index skips documents without the indexed field", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create collection.
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("users");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "users" created.')).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: /users.*docs/ }).click();

    // 2. Insert alice (has nickname) then bob (no nickname).
    await insertDocument(page, '{"name":"alice","nickname":"ally"}');
    await insertDocument(page, '{"name":"bob"}');

    // 3. Create sparse index on "nickname".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("nickname");
    // Tick the sparse checkbox.
    const sparseCheckbox = page.locator("label").filter({ hasText: /sparse/i }).locator("input[type='checkbox']");
    if (await sparseCheckbox.isVisible({ timeout: 1_000 }).catch(() => false)) {
      await sparseCheckbox.check();
    }
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.users\.nickname" created/)).toBeVisible({ timeout: 5_000 });

    // 4. Query for "ally" — should return exactly 1 result.
    await page.getByRole("tab", { name: "Query" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "nickname" });
    await page.locator("input[placeholder='Value to match']").fill("ally");
    await page.getByRole("button", { name: "Query" }).click();

    const queryRows = page.locator("table tbody tr");
    await expect(queryRows).toHaveCount(1, { timeout: 5_000 });

    // 5. Scan entire range — should only have 1 entry (bob is skipped).
    await page.getByRole("tab", { name: "Scan" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "nickname" });
    await page.locator("input[placeholder='Start value']").fill("a");
    await page.locator("input[placeholder='End value']").fill("\uFFFF");
    await page.getByRole("button", { name: "Scan" }).click();

    // Only 1 row in the scan results table (the "ally" entry).
    const scanTable = page.locator("table").filter({ has: page.getByRole("columnheader", { name: "Value" }) });
    const scanRows = scanTable.locator("tbody tr");
    await expect(scanRows).toHaveCount(1, { timeout: 5_000 });
  });

  test("numeric index scan excludes values outside numeric range", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create collection "testkv".
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("testkv");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "testkv" created.')).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: /testkv.*docs/ }).click();

    // 2. Insert documents: f2=5, f2=123, f2=1234.
    await insertDocument(page, '{"f2":5}');
    await insertDocument(page, '{"f2":123}');
    await insertDocument(page, '{"f2":1234}');

    // 3. Create a NUMERIC index on "f2".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("f2");
    // Select the numeric value type.
    await page.locator(".modal select").selectOption({ label: "Numeric (integer ordering)" });
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.testkv\.f2" created/)).toBeVisible({ timeout: 5_000 });

    // 4. Scan [1, 222) — numerically: 5 and 123 are in range, 1234 is NOT.
    await page.getByRole("tab", { name: "Scan" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "f2" });
    await page.locator("input[placeholder='Start value']").fill("1");
    await page.locator("input[placeholder='End value']").fill("222");
    await page.getByRole("button", { name: "Scan" }).click();

    // 5. Results must contain 5 and 123 but not 1234.
    const scanTable = page.locator("table").filter({ has: page.getByRole("columnheader", { name: "Value" }) });
    await expect(scanTable.getByRole("columnheader", { name: "Value" })).toBeVisible({ timeout: 5_000 });
    const scanRows = scanTable.locator("tbody tr");
    await expect(scanRows).toHaveCount(2, { timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "5", exact: true })).toBeVisible({ timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "123", exact: true })).toBeVisible({ timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "1234", exact: true })).not.toBeVisible();
  });

  test("string index scan uses lexicographic ordering", async ({ page }) => {
    await navigateToCollections(page);

    // 1. Create collection "testkv".
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page.locator("input[placeholder='Enter collection name']").fill("testkv");
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText('Collection "testkv" created.')).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: /testkv.*docs/ }).click();

    // 2. Insert documents: f2=5, f2=123, f2=1234.
    await insertDocument(page, '{"f2":5}');
    await insertDocument(page, '{"f2":123}');
    await insertDocument(page, '{"f2":1234}');

    // 3. Create a STRING (default) index on "f2".
    await page.locator(".tabs.tabs-bordered").getByRole("tab", { name: "Indexes" }).click();
    await expect(page.getByRole("tab", { name: "Manage" })).toBeVisible({ timeout: 5_000 });
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator("input[placeholder='Field name']").fill("f2");
    // Leave the default "String (lexicographic)" selection.
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(/Index "idx-db\.testkv\.f2" created/)).toBeVisible({ timeout: 5_000 });

    // 4. Scan [1, 222) — lexicographically: "1234" < "222" so 123 and 1234 are both in range;
    //    "5" >= "222" lexicographically so f2=5 is NOT included.
    await page.getByRole("tab", { name: "Scan" }).click();
    await page.locator("select").filter({ hasText: "-- select index --" }).selectOption({ label: "f2" });
    await page.locator("input[placeholder='Start value']").fill("1");
    await page.locator("input[placeholder='End value']").fill("222");
    await page.getByRole("button", { name: "Scan" }).click();

    // 5. Lexicographic results: 123 and 1234 are in range, 5 is NOT.
    const scanTable = page.locator("table").filter({ has: page.getByRole("columnheader", { name: "Value" }) });
    await expect(scanTable.getByRole("columnheader", { name: "Value" })).toBeVisible({ timeout: 5_000 });
    const scanRows = scanTable.locator("tbody tr");
    await expect(scanRows).toHaveCount(2, { timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "123", exact: true })).toBeVisible({ timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "1234", exact: true })).toBeVisible({ timeout: 5_000 });
    await expect(scanTable.getByRole("cell", { name: "5", exact: true })).not.toBeVisible();
  });

  test("shows validation warnings for empty fields", async ({ page }) => {
    await navigateToIndexSection(page);

    // 1. Try creating an index with an empty field name.
    await page.getByRole("button", { name: "Create Index" }).click();
    await page.locator(".modal").getByRole("button", { name: "Create" }).click();
    await expect(page.getByText("Field name is required.")).toBeVisible({ timeout: 5_000 });
    // Close the modal by clicking Cancel.
    await page.locator(".modal").getByRole("button", { name: "Cancel" }).click();
    await expect(page.locator(".modal")).not.toBeVisible({ timeout: 5_000 });

    // 2. Try querying without selecting an index or entering a value.
    await page.getByRole("tab", { name: "Query" }).click();
    await page.getByRole("button", { name: "Query" }).click();
    await expect(page.getByText("Select an index and provide a value to query.")).toBeVisible({ timeout: 5_000 });

    // 3. Try scanning without selecting an index.
    await page.getByRole("tab", { name: "Scan" }).click();
    await page.getByRole("button", { name: "Scan" }).click();
    await expect(page.getByText("Select an index and provide start and end values.")).toBeVisible({ timeout: 5_000 });
  });
});
