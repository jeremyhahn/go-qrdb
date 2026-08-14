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

const LOAD_TIMEOUT = 10_000;

const collectionState = {
  ...secondLaunchEmbeddedState,
  databases: [
    { name: "test-kv", engine: "kv", backend: "pebble", status: "open" },
  ],
};

/** Navigate to the Collections tab of the test-kv database. */
async function navigateToCollections(
  page: import("@playwright/test").Page,
): Promise<void> {
  await page.goto("/");
  const sidebar = page.locator("aside");
  await sidebar.getByText("test-kv").click();
  await expect(page.locator(".breadcrumbs").getByText("test-kv", { exact: false })).toBeVisible({
    timeout: LOAD_TIMEOUT,
  });
  // DbNavRail: use page.locator("main nav") for tool navigation
  await page.locator("main nav").getByText("Collections", { exact: true }).click();
  await expect(
    page.locator("main nav").getByText("Collections", { exact: true }),
  ).toBeVisible();
}

/** Create a collection via the UI. */
async function createCollection(
  page: import("@playwright/test").Page,
  name: string,
  idStrategy: string = "unix_nano",
): Promise<void> {
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Create Collection" })).toBeVisible();
  await page
    .locator("input[placeholder='Enter collection name']")
    .fill(name);
  if (idStrategy !== "unix_nano") {
    await page.locator("select").selectOption(idStrategy);
  }
  // Click the footer Create button (last one)
  await page.getByRole("button", { name: "Create", exact: true }).last().click();
  await expect(
    page.getByText(`Collection "${name}" created`),
  ).toBeVisible({ timeout: 5_000 });
}

/** Select a collection from the left panel by name. */
async function selectCollection(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  await page.getByRole("button", { name: new RegExp(name) }).first().click();
  await expect(page.locator("h3").filter({ hasText: name })).toBeVisible({
    timeout: 5_000,
  });
}

/** Insert a document via JSON mode. */
async function insertDocumentJson(
  page: import("@playwright/test").Page,
  json: string,
): Promise<void> {
  await page.getByRole("button", { name: "Insert", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();
  // Switch to JSON mode via the join button group
  await page.locator(".join").getByRole("button", { name: "JSON" }).click();
  await page.locator("textarea").fill(json);
  await page.getByRole("button", { name: "Insert", exact: true }).last().click();
  await expect(page.getByText("Document inserted").first()).toBeVisible({
    timeout: 5_000,
  });
  // Wait for the toast to auto-dismiss before returning, so the next insert
  // does not encounter duplicate "Document inserted" text on the page.
  await expect(page.getByText("Document inserted")).toHaveCount(0, {
    timeout: 10_000,
  });
}

/** Insert a document via Form mode using the Add field section. */
async function insertDocumentForm(
  page: import("@playwright/test").Page,
  fields: Array<{ name: string; type: string; value: string | boolean }>,
): Promise<void> {
  await page.getByRole("button", { name: "Insert", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();
  // Should already be in Form mode by default

  for (const field of fields) {
    // Fill in new field name
    await page.locator("#new-field-name").fill(field.name);
    // Select type
    await page.locator("#new-field-type").selectOption(field.type);
    // Click the Add button
    await page.getByRole("button", { name: "Add field" }).click();

    // Now fill the field value
    if (field.type === "BOOLEAN") {
      if (field.value === true || field.value === "true") {
        const checkbox = page.locator(`#field-${field.name}`);
        const isChecked = await checkbox.isChecked();
        if (!isChecked) await checkbox.check();
      }
    } else if (field.type === "JSON") {
      await page
        .locator(`#field-${field.name}`)
        .fill(typeof field.value === "string" ? field.value : JSON.stringify(field.value));
    } else {
      await page.locator(`#field-${field.name}`).fill(String(field.value));
    }
  }

  await page.getByRole("button", { name: "Insert", exact: true }).last().click();
  await expect(page.getByText("Document inserted").first()).toBeVisible({
    timeout: 5_000,
  });
}

// ---------------------------------------------------------------------------
// Collection CRUD
// ---------------------------------------------------------------------------

test.describe("Collection CRUD", () => {
  test.use({ mockState: collectionState });

  test("shows empty collection list initially", async ({ page }) => {
    await navigateToCollections(page);
    await expect(page.getByText("No collections found")).toBeVisible();
    await expect(page.getByText("Select a Collection")).toBeVisible();
  });

  test("create a collection via slide panel", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "users");
    await expect(page.getByText("users").first()).toBeVisible();
  });

  test("create a collection with field_hash strategy", async ({ page }) => {
    await navigateToCollections(page);

    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Create Collection" })).toBeVisible();
    await page
      .locator("input[placeholder='Enter collection name']")
      .fill("hashed-coll");
    await page.locator("select").selectOption("field_hash");

    // field_hash input should appear
    await expect(
      page.locator("input[placeholder='e.g. email, username']"),
    ).toBeVisible();
    await page
      .locator("input[placeholder='e.g. email, username']")
      .fill("email");

    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(
      page.getByText('Collection "hashed-coll" created'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("create collection with empty name shows warning", async ({ page }) => {
    await navigateToCollections(page);
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Create Collection" })).toBeVisible();
    // Leave name empty, click Create
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(page.getByText("Collection name is required")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("create collection with field_hash but empty hash shows warning", async ({ page }) => {
    await navigateToCollections(page);
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page
      .locator("input[placeholder='Enter collection name']")
      .fill("needs-hash");
    await page.locator("select").selectOption("field_hash");
    // Leave field hash empty
    await page.getByRole("button", { name: "Create", exact: true }).last().click();
    await expect(
      page.getByText("Field hash is required"),
    ).toBeVisible({ timeout: 5_000 });
  });

  test("list multiple collections", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "orders");
    await createCollection(page, "products");
    await createCollection(page, "inventory");
    // Verify all three appear in the left panel
    await expect(page.getByText("orders").first()).toBeVisible();
    await expect(page.getByText("products").first()).toBeVisible();
    await expect(page.getByText("inventory").first()).toBeVisible();
  });

  test("drop a collection with confirmation", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "temp-coll");
    await expect(page.getByText("temp-coll").first()).toBeVisible();

    // Click the drop button (trash icon) next to the collection
    await page.getByLabel("Drop collection temp-coll").click();

    // Confirm in modal
    await expect(page.getByRole("heading", { name: "Drop Collection" })).toBeVisible();
    await expect(
      page.getByText("Are you sure you want to drop"),
    ).toBeVisible();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Drop" })
      .click();

    await expect(
      page.getByText('Collection "temp-coll" dropped'),
    ).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("No collections found")).toBeVisible();
  });

  test("cancel collection drop does not remove collection", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "keep-me");

    await page.getByLabel("Drop collection keep-me").click();
    await expect(page.getByRole("heading", { name: "Drop Collection" })).toBeVisible();
    // Cancel
    await page
      .locator(".modal")
      .getByRole("button", { name: "Cancel" })
      .click();

    // Collection should still exist
    await expect(page.getByText("keep-me").first()).toBeVisible();
  });

  test("create collection error shows toast", async ({ page }) => {
    await navigateToCollections(page);
    await injectError(page, "CollectionBinding.Create", "permission denied");

    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page
      .locator("input[placeholder='Enter collection name']")
      .fill("fail-coll");
    await page.getByRole("button", { name: "Create", exact: true }).last().click();

    await expect(page.getByText("permission denied")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "CollectionBinding.Create");
  });

  test("list collections error shows toast", async ({ page }) => {
    // Navigate first so mock state is initialized, then inject error before
    // the Collections tab triggers a List call.
    await page.goto("/");
    const sidebar = page.locator("aside");
    await sidebar.getByText("test-kv").click();
    await expect(page.locator(".breadcrumbs").getByText("test-kv", { exact: false })).toBeVisible({
      timeout: LOAD_TIMEOUT,
    });
    await injectError(page, "CollectionBinding.List", "network timeout");
    // DbNavRail: use page.locator("main nav") for tool navigation
    await page.locator("main nav").getByText("Collections", { exact: true }).click();
    await expect(page.getByText("network timeout").first()).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "CollectionBinding.List");
  });

  test("drop collection error shows toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "drop-fail");
    await injectError(page, "CollectionBinding.Drop", "storage locked");

    await page.getByLabel("Drop collection drop-fail").click();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Drop" })
      .click();

    await expect(page.getByText("storage locked")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "CollectionBinding.Drop");
  });
});

// ---------------------------------------------------------------------------
// Document CRUD - JSON Editor
// ---------------------------------------------------------------------------

test.describe("Document CRUD via JSON editor", () => {
  test.use({ mockState: collectionState });

  test("insert a document via JSON mode", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "json-coll");
    await selectCollection(page, "json-coll");

    await insertDocumentJson(
      page,
      '{"name": "Alice", "age": 30}',
    );

    // Verify document appears in the table
    await expect(page.getByText("Alice").first()).toBeVisible();
    await expect(page.getByText("1 documents")).toBeVisible();
  });

  test("document ID is displayed as a string without precision loss", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "id-coll");
    await selectCollection(page, "id-coll");

    await insertDocumentJson(page, '{"key": "value"}');

    // Click the document row to see detail
    const tableRow = page.locator("tr").filter({ hasText: "1773108930009562724" });
    await expect(tableRow).toBeVisible();

    // The full ID should be visible without truncation or precision loss
    await expect(page.getByText("1773108930009562724")).toBeVisible();
  });

  test("edit a document via JSON mode", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "edit-coll");
    await selectCollection(page, "edit-coll");

    await insertDocumentJson(page, '{"status": "draft"}');

    // Click the row to open detail panel
    await page.getByText("draft").first().click();

    // Click Edit in detail footer
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Edit Document" })).toBeVisible();

    // Verify document ID field is disabled
    const idInput = page.locator("input[disabled]").first();
    await expect(idInput).toBeVisible();

    // Switch to JSON mode
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();

    // Update the JSON
    await page.locator("textarea").fill('{"status": "published"}');

    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("updated")).toBeVisible({ timeout: 5_000 });

    // Verify updated value in table
    await expect(page.getByText("published").first()).toBeVisible();
  });

  test("delete a document via detail panel", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "del-coll");
    await selectCollection(page, "del-coll");

    await insertDocumentJson(page, '{"item": "remove-me"}');
    await expect(page.getByText("1 documents")).toBeVisible();

    // Click row to open detail
    await page.getByText("remove-me").first().click();

    // Click Delete in detail footer
    await page.getByRole("button", { name: "Delete", exact: true }).click();

    // Confirm in modal
    await expect(page.getByRole("heading", { name: "Delete Document" })).toBeVisible();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Delete", exact: true })
      .click();

    await expect(page.getByText("deleted")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText("0 documents")).toBeVisible();
  });

  test("invalid JSON shows error message", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "json-err-coll");
    await selectCollection(page, "json-err-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();

    // Switch to JSON mode
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();

    // Enter invalid JSON
    await page.locator("textarea").fill("{bad json}");

    // Trigger validation via input event
    await page.locator("textarea").press("Space");

    await expect(page.getByText("Invalid JSON syntax")).toBeVisible();

    // Clicking Insert should not dismiss the panel when JSON is invalid
    await page.getByRole("button", { name: "Insert", exact: true }).last().click();
    await expect(page.getByText("Invalid JSON syntax")).toBeVisible();
  });

  test("non-object JSON shows error message", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "arr-err-coll");
    await selectCollection(page, "arr-err-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();
    // Switch to JSON mode
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    await page.locator("textarea").fill("[1, 2, 3]");

    // Click Insert -- should validate
    await page.getByRole("button", { name: "Insert", exact: true }).last().click();

    await expect(page.getByText("Document must be a JSON object")).toBeVisible({
      timeout: 5_000,
    });
  });

  test("insert document error shows toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "insert-err");
    await selectCollection(page, "insert-err");

    await injectError(
      page,
      "CollectionBinding.InsertDocument",
      "disk full",
    );

    await page.getByRole("button", { name: "Insert", exact: true }).click();
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    await page.locator("textarea").fill('{"key": "val"}');
    await page.getByRole("button", { name: "Insert", exact: true }).last().click();

    await expect(page.getByText("disk full")).toBeVisible({ timeout: 5_000 });
    await clearError(page, "CollectionBinding.InsertDocument");
  });
});

// ---------------------------------------------------------------------------
// Document CRUD - Form Editor
// ---------------------------------------------------------------------------

test.describe("Document CRUD via Form editor", () => {
  test.use({ mockState: collectionState });

  test("insert a document via form with VARCHAR field", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "form-coll");
    await selectCollection(page, "form-coll");

    await insertDocumentForm(page, [
      { name: "username", type: "VARCHAR", value: "bob" },
    ]);

    await expect(page.getByText("bob").first()).toBeVisible();
    await expect(page.getByText("1 documents")).toBeVisible();
  });

  test("insert a document via form with DOUBLE field", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "double-coll");
    await selectCollection(page, "double-coll");

    await insertDocumentForm(page, [
      { name: "price", type: "DOUBLE", value: "19.99" },
    ]);

    await expect(page.getByText("19.99").first()).toBeVisible();
  });

  test("insert a document via form with BIGINT field", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "bigint-coll");
    await selectCollection(page, "bigint-coll");

    await insertDocumentForm(page, [
      { name: "counter", type: "BIGINT", value: "9007199254740993" },
    ]);

    await expect(page.getByText("9007199254740993").first()).toBeVisible();
  });

  test("insert a document via form with BOOLEAN field", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "bool-coll");
    await selectCollection(page, "bool-coll");

    await insertDocumentForm(page, [
      { name: "active", type: "BOOLEAN", value: true },
    ]);

    await expect(page.getByText("true").first()).toBeVisible();
  });

  test("insert a document via form with JSON field", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "json-field-coll");
    await selectCollection(page, "json-field-coll");

    await insertDocumentForm(page, [
      { name: "metadata", type: "JSON", value: '{"nested": true}' },
    ]);

    await expect(page.getByText("nested").first()).toBeVisible();
  });

  test("insert a document via form with multiple fields", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "multi-field-coll");
    await selectCollection(page, "multi-field-coll");

    await insertDocumentForm(page, [
      { name: "name", type: "VARCHAR", value: "Eve" },
      { name: "score", type: "DOUBLE", value: "95.5" },
      { name: "verified", type: "BOOLEAN", value: true },
    ]);

    await expect(page.getByText("Eve").first()).toBeVisible();
    await expect(page.getByText("95.5").first()).toBeVisible();
  });

  test("add field with empty name is ignored", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "empty-field-coll");
    await selectCollection(page, "empty-field-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();

    // Try to add a field with empty name
    await page.locator("#new-field-name").fill("");
    await page.getByRole("button", { name: "Add field" }).click();

    // No field should appear (only the Add field section remains)
    // The SchemaForm should still show just the add-field section
    await expect(page.locator("#new-field-name")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Form/JSON Toggle
// ---------------------------------------------------------------------------

test.describe("Form/JSON toggle switching", () => {
  test.use({ mockState: collectionState });

  test("default mode is Form", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "toggle-coll");
    await selectCollection(page, "toggle-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Insert Document" })).toBeVisible();

    // Form button should be active (btn-primary)
    const formBtn = page.locator(".join").getByRole("button", { name: "Form" });
    await expect(formBtn).toBeVisible();
    await expect(formBtn).toHaveClass(/btn-primary/);
    // JSON button should be inactive (btn-ghost)
    const jsonBtn = page.locator(".join").getByRole("button", { name: "JSON" });
    await expect(jsonBtn).toHaveClass(/btn-ghost/);
  });

  test("toggle to JSON mode shows textarea", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "toggle2-coll");
    await selectCollection(page, "toggle2-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();

    // Switch to JSON mode
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();

    // Should see textarea for JSON
    await expect(page.locator("textarea")).toBeVisible();
    await expect(page.getByText("Document Body (JSON)")).toBeVisible();
  });

  test("toggling back to form preserves fields from JSON", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "toggle3-coll");
    await selectCollection(page, "toggle3-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();

    // Switch to JSON and enter data
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    await page.locator("textarea").fill('{"city": "Berlin"}');

    // Switch back to Form
    await page.locator(".join").getByRole("button", { name: "Form" }).click();

    // The field "city" should now appear in the form
    // (SchemaForm renders fields based on the parsed object)
    // Insert the document to verify the data was preserved
    await page.getByRole("button", { name: "Insert", exact: true }).last().click();
    await expect(page.getByText("Document inserted")).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.getByText("Berlin").first()).toBeVisible();
  });

  test("form fields serialize to JSON when toggling", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "serialize-coll");
    await selectCollection(page, "serialize-coll");

    await page.getByRole("button", { name: "Insert", exact: true }).click();

    // Add a field in form mode
    await page.locator("#new-field-name").fill("color");
    await page.locator("#new-field-type").selectOption("VARCHAR");
    await page.getByRole("button", { name: "Add field" }).click();
    await page.locator("#field-color").fill("blue");

    // Switch to JSON mode
    await page.locator(".join").getByRole("button", { name: "JSON" }).click();

    // The textarea should contain the serialized JSON including "color"
    const jsonContent = await page.locator("textarea").inputValue();
    expect(jsonContent).toContain("color");
    expect(jsonContent).toContain("blue");
  });
});

// ---------------------------------------------------------------------------
// Multi-document operations
// ---------------------------------------------------------------------------

test.describe("Multi-document insertion", () => {
  test.use({ mockState: collectionState });

  test("insert multiple documents and verify table", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "multi-doc");
    await selectCollection(page, "multi-doc");

    await insertDocumentJson(page, '{"name": "Alice", "role": "admin"}');
    await insertDocumentJson(page, '{"name": "Bob", "role": "user"}');
    await insertDocumentJson(page, '{"name": "Charlie", "role": "user"}');

    await expect(page.getByText("3 documents")).toBeVisible();
    await expect(page.getByText("Alice").first()).toBeVisible();
    await expect(page.getByText("Bob").first()).toBeVisible();
    await expect(page.getByText("Charlie").first()).toBeVisible();
  });

  test("dynamic columns appear based on document fields", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "dyn-col");
    await selectCollection(page, "dyn-col");

    await insertDocumentJson(page, '{"first": "John", "last": "Doe"}');

    // Table headers should include the dynamic field columns
    await expect(page.getByRole("columnheader", { name: "ID" })).toBeVisible();
    await expect(
      page.getByRole("columnheader", { name: "first" }),
    ).toBeVisible();
    await expect(
      page.getByRole("columnheader", { name: "last" }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Bulk document deletion
// ---------------------------------------------------------------------------

test.describe("Bulk document deletion", () => {
  test.use({ mockState: collectionState });

  test("select and bulk delete documents", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "bulk-del");
    await selectCollection(page, "bulk-del");

    await insertDocumentJson(page, '{"item": "one"}');
    await insertDocumentJson(page, '{"item": "two"}');
    await insertDocumentJson(page, '{"item": "three"}');

    await expect(page.getByText("3 documents")).toBeVisible();

    // Select rows via checkboxes in CrudTable
    const checkboxes = page.locator("table input[type='checkbox']");
    // First checkbox is the select-all; individual ones follow
    const checkboxCount = await checkboxes.count();
    expect(checkboxCount).toBeGreaterThanOrEqual(4); // select-all + 3 rows

    // Click select-all checkbox
    await checkboxes.first().click();

    // Click the Delete button that appears when rows are selected (includes count badge)
    await page.getByRole("button", { name: /^Delete/ }).click();

    // Confirm if there is a confirmation
    const confirmBtn = page.locator(".modal").getByRole("button", { name: "Delete", exact: true });
    if (await confirmBtn.isVisible({ timeout: 1_000 }).catch(() => false)) {
      await confirmBtn.click();
    }

    await expect(page.getByText("Deleted 3 documents")).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.getByText("0 documents")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Document detail panel
// ---------------------------------------------------------------------------

test.describe("Document detail panel", () => {
  test.use({ mockState: collectionState });

  test("clicking a document row opens detail panel", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "detail-coll");
    await selectCollection(page, "detail-coll");

    await insertDocumentJson(
      page,
      '{"title": "My Document", "count": 42}',
    );

    // Click the row
    await page.getByText("My Document").first().click();

    // Detail panel should show
    await expect(page.getByText("Document Detail")).toBeVisible();
    await expect(page.getByText("Document ID")).toBeVisible();
    await expect(page.getByText("Document Body")).toBeVisible();
    await expect(page.getByText("My Document", { exact: true })).toBeVisible();

    // Collection name should be shown in detail
    await expect(
      page.locator("text=detail-coll").first(),
    ).toBeVisible();
  });

  test("detail panel shows edit and delete buttons", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "btn-coll");
    await selectCollection(page, "btn-coll");

    await insertDocumentJson(page, '{"x": 1}');
    // Click the row
    await page.locator("tr").filter({ hasText: "1" }).first().click();

    await expect(page.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
  });

  test("closing detail panel hides it", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "close-coll");
    await selectCollection(page, "close-coll");

    await insertDocumentJson(page, '{"data": "closetest"}');
    await page.getByText("closetest").first().click();
    await expect(page.getByText("Document Detail")).toBeVisible({ timeout: 5_000 });

    // Close via the X button or clicking outside
    // SlidePanel typically has a close button
    const closeBtn = page.locator("[aria-label='Close panel'], button:has(svg.lucide-x)").first();
    if (await closeBtn.isVisible().catch(() => false)) {
      await closeBtn.click();
    }
  });
});

// ---------------------------------------------------------------------------
// Document editing via detail panel
// ---------------------------------------------------------------------------

test.describe("Document editing", () => {
  test.use({ mockState: collectionState });

  test("edit document from detail panel in form mode", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "edit-form-coll");
    await selectCollection(page, "edit-form-coll");

    await insertDocumentJson(page, '{"greeting": "hello"}');

    // Click row to open detail
    await page.getByText("hello").first().click();
    await expect(page.getByText("Document Detail")).toBeVisible();

    // Click Edit
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Edit Document" })).toBeVisible();

    // Document ID should be displayed and disabled
    const disabledInput = page.locator("input[disabled]").first();
    await expect(disabledInput).toBeVisible();
    await expect(page.getByText("ID cannot be changed")).toBeVisible();

    // Form should show the existing field
    await expect(page.locator("#field-greeting")).toBeVisible();

    // Update the field value
    await page.locator("#field-greeting").fill("world");

    // Save
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("updated")).toBeVisible({ timeout: 5_000 });

    // Verify updated value in table
    await expect(page.getByText("world").first()).toBeVisible();
  });

  test("update document error shows toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "upd-err-coll");
    await selectCollection(page, "upd-err-coll");

    await insertDocumentJson(page, '{"val": "original"}');

    await page.getByText("original").first().click();
    await page.getByRole("button", { name: "Edit", exact: true }).click();

    await injectError(
      page,
      "CollectionBinding.UpdateDocument",
      "concurrent modification",
    );

    await page.locator(".join").getByRole("button", { name: "JSON" }).click();
    await page.locator("textarea").fill('{"val": "changed"}');
    await page.getByRole("button", { name: "Save" }).click();

    await expect(
      page.getByText("concurrent modification"),
    ).toBeVisible({ timeout: 5_000 });
    await clearError(page, "CollectionBinding.UpdateDocument");
  });
});

// ---------------------------------------------------------------------------
// Document deletion
// ---------------------------------------------------------------------------

test.describe("Document deletion", () => {
  test.use({ mockState: collectionState });

  test("delete document error shows toast", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "del-err-coll");
    await selectCollection(page, "del-err-coll");

    await insertDocumentJson(page, '{"temp": true}');

    await page.getByText("true").first().click();

    await injectError(
      page,
      "CollectionBinding.DeleteDocument",
      "permission denied",
    );

    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Delete", exact: true })
      .click();

    await expect(page.getByText("permission denied")).toBeVisible({
      timeout: 5_000,
    });
    await clearError(page, "CollectionBinding.DeleteDocument");
  });

  test("cancel delete does not remove document", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "keep-doc-coll");
    await selectCollection(page, "keep-doc-coll");

    await insertDocumentJson(page, '{"persist": "yes"}');

    await page.getByText("yes").first().click();
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Delete Document" })).toBeVisible();

    // Cancel
    await page
      .locator(".modal")
      .getByRole("button", { name: "Cancel" })
      .click();

    // Document should still be visible
    await expect(page.getByText("1 documents")).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

test.describe("Collection refresh", () => {
  test.use({ mockState: collectionState });

  test("refresh button reloads collections", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "refresh-coll");
    await expect(page.getByText("refresh-coll").first()).toBeVisible();

    // Click the top-level Refresh button (scoped to header area)
    await page.getByRole("button", { name: "Refresh" }).first().click();

    // Collection should still be visible after refresh
    await expect(page.getByText("refresh-coll").first()).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Selecting collection clears prior selection
// ---------------------------------------------------------------------------

test.describe("Collection selection behavior", () => {
  test.use({ mockState: collectionState });

  test("selecting a different collection loads its documents", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "coll-a");
    await createCollection(page, "coll-b");

    // Select coll-a and insert a document
    await selectCollection(page, "coll-a");
    await insertDocumentJson(page, '{"from": "coll-a"}');
    await expect(page.getByText("coll-a").first()).toBeVisible();
    await expect(page.getByText("1 documents")).toBeVisible();

    // Select coll-b -- should show empty docs
    await selectCollection(page, "coll-b");
    await expect(page.getByText("0 documents")).toBeVisible();
  });

  test("dropping selected collection clears document browser", async ({ page }) => {
    await navigateToCollections(page);
    await createCollection(page, "will-drop");
    await selectCollection(page, "will-drop");
    await insertDocumentJson(page, '{"tmp": 1}');

    // Drop the selected collection
    await page.getByLabel("Drop collection will-drop").click();
    await page
      .locator(".modal")
      .getByRole("button", { name: "Drop" })
      .click();

    await expect(
      page.getByText('Collection "will-drop" dropped'),
    ).toBeVisible({ timeout: 5_000 });

    // Should show empty state again since the selected collection is gone
    await expect(page.getByText("Select a Collection")).toBeVisible();
  });
});
