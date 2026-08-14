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
  import type { ImportConfig, ParsedPreview } from './types.ts';
  import { LARGE_IMPORT_THRESHOLD } from './types.ts';

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    config,
    preview,
    transactional,
    onTransactionalChange,
    onStartImport,
  }: {
    config: ImportConfig;
    preview: ParsedPreview;
    transactional: boolean;
    onTransactionalChange: (transactional: boolean) => void;
    onStartImport: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const isLargeImport = $derived(preview.totalRows > LARGE_IMPORT_THRESHOLD);

  const formatLabel = $derived.by(() => {
    if (config.format === 'json' && config.jsonFormat) {
      return `JSON (${config.jsonFormat.toUpperCase()})`;
    }
    return config.format.toUpperCase();
  });

  const valueModeLabel = $derived(
    config.valueMode === 'field' ? 'Specific field' : 'Entire row as JSON'
  );

  const summaryItems = $derived.by(() => {
    const items: Array<{ label: string; value: string; highlight?: boolean }> = [
      { label: 'File', value: config.file?.name ?? 'Unknown' },
      { label: 'Format', value: formatLabel },
      { label: 'Total Rows', value: preview.totalRows.toLocaleString(), highlight: isLargeImport },
      { label: 'Key Field', value: config.keyField },
      { label: 'Value Mode', value: valueModeLabel },
    ];

    if (config.valueMode === 'field' && config.valueField) {
      items.push({ label: 'Value Field', value: config.valueField });
    }

    return items;
  });

  const optionItems = $derived.by(() => {
    const items: Array<{ label: string; enabled: boolean }> = [];

    if (config.format === 'csv') {
      items.push({ label: 'Skip header row', enabled: config.skipHeader });
    }

    items.push({ label: 'Create missing keys', enabled: config.createMissing });
    items.push({ label: 'Update existing keys', enabled: config.updateExisting });

    return items;
  });

</script>

<div class="space-y-6">
  <div>
    <h3 class="mb-2 text-lg font-semibold">Review Import Settings</h3>
    <p class="text-base-content/70 text-sm">
      Review your configuration before starting the import.
    </p>
  </div>

  <!-- Summary Card -->
  <div class="card border bg-base-100">
    <div class="card-body p-4">
      <h4 class="mb-4 flex items-center gap-2 text-sm font-semibold">
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="text-primary h-5 w-5"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
          />
        </svg>
        Import Summary
      </h4>

      <div class="grid gap-3 sm:grid-cols-2">
        {#each summaryItems as item (item.label)}
          <div class="flex justify-between rounded-lg bg-base-200/50 px-3 py-2">
            <span class="text-base-content/70 text-sm">{item.label}</span>
            <span
              class="text-sm font-medium
                {item.highlight ? 'text-warning' : ''}"
            >
              {item.value}
            </span>
          </div>
        {/each}
      </div>

      <!-- Options -->
      <div class="divider my-4"></div>

      <h5 class="mb-3 text-sm font-semibold">Import Options</h5>
      <div class="flex flex-wrap gap-2">
        {#each optionItems as option (option.label)}
          <span
            class="badge {option.enabled ? 'badge-success' : 'badge-ghost'}"
          >
            {#if option.enabled}
              <svg
                xmlns="http://www.w3.org/2000/svg"
                class="mr-1 h-3 w-3"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M5 13l4 4L19 7"
                />
              </svg>
            {:else}
              <svg
                xmlns="http://www.w3.org/2000/svg"
                class="mr-1 h-3 w-3"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M6 18L18 6M6 6l12 12"
                />
              </svg>
            {/if}
            {option.label}
          </span>
        {/each}
      </div>
    </div>
  </div>

  <!-- Import Mode -->
  <div class="card border bg-base-100">
    <div class="card-body p-4">
      <h4 class="mb-4 flex items-center gap-2 text-sm font-semibold">
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="text-primary h-5 w-5"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 6V4m0 2a2 2 0 100 4m0-4a2 2 0 110 4m-6 8a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4m6 6v10m6-2a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4"
          />
        </svg>
        Import Mode
      </h4>

      <div class="space-y-3">
        <label
          class="flex cursor-pointer items-start gap-3 rounded-lg border p-4 transition-colors
            {transactional ? 'border-primary bg-primary/5' : 'hover:bg-base-200/30'}"
        >
          <input
            type="radio"
            name="import-mode"
            class="radio radio-primary mt-0.5"
            checked={transactional}
            onchange={() => onTransactionalChange(true)}
          />
          <div class="flex-1">
            <span class="block font-medium">Transactional (All-or-nothing)</span>
            <span class="text-base-content/60 block text-sm">
              Either all records are imported successfully, or none are.
              Recommended for critical data.
            </span>
          </div>
        </label>

        <label
          class="flex cursor-pointer items-start gap-3 rounded-lg border p-4 transition-colors
            {!transactional ? 'border-primary bg-primary/5' : 'hover:bg-base-200/30'}"
        >
          <input
            type="radio"
            name="import-mode"
            class="radio radio-primary mt-0.5"
            checked={!transactional}
            onchange={() => onTransactionalChange(false)}
          />
          <div class="flex-1">
            <span class="block font-medium">Best-effort (Continue on error)</span>
            <span class="text-base-content/60 block text-sm">
              Import as many records as possible, logging failures.
              Useful for large imports with potential data issues.
            </span>
          </div>
        </label>
      </div>
    </div>
  </div>

  <!-- Large Import Warning -->
  {#if isLargeImport}
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
      <div>
        <h5 class="font-medium">Large Import Warning</h5>
        <p class="text-sm">
          You are importing {preview.totalRows.toLocaleString()} rows.
          Large imports may take several minutes to complete.
          {#if transactional}
            Consider using best-effort mode for better performance.
          {/if}
        </p>
      </div>
    </div>
  {/if}

  <!-- Start Import Button -->
  <div class="flex justify-end">
    <button
      type="button"
      class="btn btn-primary btn-lg gap-2"
      onclick={onStartImport}
    >
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12"
        />
      </svg>
      Start Import
    </button>
  </div>
</div>
