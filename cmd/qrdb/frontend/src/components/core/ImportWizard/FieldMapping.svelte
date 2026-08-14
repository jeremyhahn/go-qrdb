<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->
<script lang="ts">
  import type {
    ParsedPreview,
    ImportFormat,
    ValueMode,
    FieldMappingUpdate,
  } from './types.ts';

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    preview,
    keyField,
    valueMode,
    valueField,
    skipHeader,
    createMissing,
    updateExisting,
    format,
    onChange,
  }: {
    preview: ParsedPreview;
    keyField: string;
    valueMode: ValueMode;
    valueField: string;
    skipHeader: boolean;
    createMissing: boolean;
    updateExisting: boolean;
    format: ImportFormat;
    onChange: (mapping: FieldMappingUpdate) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const showSkipHeader = $derived(format === 'csv');

  const valueFieldOptions = $derived.by(() => {
    // Filter out the key field from value field options
    return preview.headers.filter((h) => h !== keyField);
  });

  const previewData = $derived.by(() => {
    // Show what the key-value pairs will look like
    return preview.rows.map((row) => {
      const key = row[keyField] ?? '';
      let value: string;

      if (valueMode === 'field') {
        value = row[valueField] ?? '';
      } else {
        // Row mode - entire row as JSON, excluding the key field
        const rowData: Record<string, string> = {};
        for (const header of preview.headers) {
          if (header !== keyField) {
            rowData[header] = row[header] ?? '';
          }
        }
        value = JSON.stringify(rowData);
      }

      return { key, value };
    });
  });

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleKeyFieldChange(event: Event): void {
    const target = event.target as HTMLSelectElement;
    onChange({ keyField: target.value });
  }

  function handleValueModeChange(mode: ValueMode): void {
    onChange({ valueMode: mode });
  }

  function handleValueFieldChange(event: Event): void {
    const target = event.target as HTMLSelectElement;
    onChange({ valueField: target.value });
  }

  function handleSkipHeaderChange(event: Event): void {
    const target = event.target as HTMLInputElement;
    onChange({ skipHeader: target.checked });
  }

  function handleCreateMissingChange(event: Event): void {
    const target = event.target as HTMLInputElement;
    onChange({ createMissing: target.checked });
  }

  function handleUpdateExistingChange(event: Event): void {
    const target = event.target as HTMLInputElement;
    onChange({ updateExisting: target.checked });
  }
</script>

<div class="space-y-6">
  <div>
    <h3 class="mb-2 text-lg font-semibold">Configure Field Mapping</h3>
    <p class="text-base-content/70 text-sm">
      Select which fields to use as the key and value for each imported record.
    </p>
  </div>

  <!-- Preview Table -->
  <div class="card bg-base-200/50">
    <div class="card-body p-4">
      <h4 class="mb-3 text-sm font-semibold">Data Preview (First {preview.rows.length} rows)</h4>

      <div class="overflow-x-auto">
        <table class="table table-sm">
          <thead>
            <tr>
              {#each preview.headers as header (header)}
                <th
                  class="
                    {header === keyField ? 'bg-primary/20 text-primary' : ''}
                    {header === valueField && valueMode === 'field' ? 'bg-secondary/20 text-secondary' : ''}"
                >
                  {header}
                  {#if header === keyField}
                    <span class="badge badge-primary badge-xs ml-1">Key</span>
                  {/if}
                  {#if header === valueField && valueMode === 'field'}
                    <span class="badge badge-secondary badge-xs ml-1">Value</span>
                  {/if}
                </th>
              {/each}
            </tr>
          </thead>
          <tbody>
            {#each preview.rows as row, index (index)}
              <tr>
                {#each preview.headers as header (header)}
                  <td
                    class="max-w-[200px] truncate
                      {header === keyField ? 'bg-primary/10' : ''}
                      {header === valueField && valueMode === 'field' ? 'bg-secondary/10' : ''}"
                    title={row[header]}
                  >
                    {row[header]}
                  </td>
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <p class="text-base-content/50 mt-2 text-xs">
        Total rows to import: {preview.totalRows}
      </p>
    </div>
  </div>

  <!-- Mapping Configuration -->
  <div class="grid gap-6 md:grid-cols-2">
    <!-- Key Field Selection -->
    <div class="form-control">
      <label class="label" for="key-field">
        <span class="label-text font-medium">Key Field</span>
        <span class="label-text-alt text-error">Required</span>
      </label>
      <select
        id="key-field"
        class="select select-bordered w-full"
        value={keyField}
        onchange={handleKeyFieldChange}
      >
        <option value="" disabled>Select a field...</option>
        {#each preview.headers as header (header)}
          <option value={header}>{header}</option>
        {/each}
      </select>
      <p class="text-base-content/60 mt-1 text-xs">
        This field will be used as the key for each record
      </p>
    </div>

    <!-- Value Mode Selection -->
    <div class="form-control">
      <p class="label-text mb-2 font-medium">Value Mode</p>

      <div class="space-y-2">
        <label class="flex cursor-pointer items-center gap-3 rounded-lg p-2 hover:bg-base-200/50">
          <input
            type="radio"
            name="value-mode"
            class="radio radio-sm"
            checked={valueMode === 'field'}
            onchange={() => handleValueModeChange('field')}
          />
          <span>Use specific field as value</span>
        </label>

        <label class="flex cursor-pointer items-center gap-3 rounded-lg p-2 hover:bg-base-200/50">
          <input
            type="radio"
            name="value-mode"
            class="radio radio-sm"
            checked={valueMode === 'row'}
            onchange={() => handleValueModeChange('row')}
          />
          <span>Use entire row as JSON value</span>
        </label>
      </div>
    </div>
  </div>

  <!-- Value Field Selection (only when field mode) -->
  {#if valueMode === 'field'}
    <div class="form-control max-w-md">
      <label class="label" for="value-field">
        <span class="label-text font-medium">Value Field</span>
      </label>
      <select
        id="value-field"
        class="select select-bordered w-full"
        value={valueField}
        onchange={handleValueFieldChange}
        disabled={valueFieldOptions.length === 0}
      >
        <option value="" disabled>Select a field...</option>
        {#each valueFieldOptions as header (header)}
          <option value={header}>{header}</option>
        {/each}
      </select>
      <p class="text-base-content/60 mt-1 text-xs">
        This field's value will be stored
      </p>
    </div>
  {/if}

  <!-- Import Preview -->
  <div class="card bg-base-200/50">
    <div class="card-body p-4">
      <h4 class="mb-3 text-sm font-semibold">Import Preview</h4>

      <div class="overflow-x-auto">
        <table class="table table-sm">
          <thead>
            <tr>
              <th class="text-primary">Key</th>
              <th class="text-secondary">Value</th>
            </tr>
          </thead>
          <tbody>
            {#each previewData as item, index (index)}
              <tr>
                <td class="font-mono text-sm">{item.key || '(empty)'}</td>
                <td class="max-w-[400px] truncate font-mono text-sm" title={item.value}>
                  {item.value || '(empty)'}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- Import Options -->
  <div class="divider">Import Options</div>

  <div class="grid gap-4 md:grid-cols-2">
    {#if showSkipHeader}
      <label class="flex cursor-pointer items-start gap-3 rounded-lg border p-4 hover:bg-base-200/30">
        <input
          type="checkbox"
          class="checkbox checkbox-sm mt-0.5"
          checked={skipHeader}
          onchange={handleSkipHeaderChange}
        />
        <div>
          <span class="block font-medium">Skip header row</span>
          <span class="text-base-content/60 block text-sm">
            First row is treated as column headers (already applied in preview)
          </span>
        </div>
      </label>
    {/if}

    <label class="flex cursor-pointer items-start gap-3 rounded-lg border p-4 hover:bg-base-200/30">
      <input
        type="checkbox"
        class="checkbox checkbox-sm mt-0.5"
        checked={createMissing}
        onchange={handleCreateMissingChange}
      />
      <div>
        <span class="block font-medium">Create missing keys</span>
        <span class="text-base-content/60 block text-sm">
          Create new records for keys that don't exist
        </span>
      </div>
    </label>

    <label class="flex cursor-pointer items-start gap-3 rounded-lg border p-4 hover:bg-base-200/30">
      <input
        type="checkbox"
        class="checkbox checkbox-sm mt-0.5"
        checked={updateExisting}
        onchange={handleUpdateExistingChange}
      />
      <div>
        <span class="block font-medium">Update existing keys</span>
        <span class="text-base-content/60 block text-sm">
          Overwrite values for keys that already exist
        </span>
      </div>
    </label>
  </div>

  <!-- Validation Warning -->
  {#if keyField === ''}
    <div class="alert alert-warning">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5 shrink-0"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
        />
      </svg>
      <span class="text-sm">Please select a key field to continue.</span>
    </div>
  {/if}
</div>
