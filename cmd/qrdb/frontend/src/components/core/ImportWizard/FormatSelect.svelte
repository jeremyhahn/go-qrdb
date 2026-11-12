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
  import type { ImportFormat, JsonFormat } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    selectedFormat,
    selectedJsonFormat,
    onSelect,
  }: {
    selectedFormat: ImportFormat | null;
    selectedJsonFormat: JsonFormat;
    onSelect: (format: ImportFormat, jsonFormat?: JsonFormat) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  // Follows the prop; local edits hold until the prop changes.
  let localJsonFormat = $derived(selectedJsonFormat);

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  // ---------------------------------------------------------------------------
  // Format Definitions
  // ---------------------------------------------------------------------------

  interface FormatOption {
    readonly id: ImportFormat;
    readonly label: string;
    readonly description: string;
    readonly icon: string;
  }

  const formats: readonly FormatOption[] = [
    {
      id: "csv",
      label: "CSV",
      description: "Auto-detects delimiter (comma, semicolon, tab, pipe)",
      icon: "M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z",
    },
    {
      id: "json",
      label: "JSON",
      description: "Single object, array of objects, or newline-delimited",
      icon: "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4",
    },
    {
      id: "archive",
      label: "Archive",
      description: "ZIP or .tar.gz containing multiple CSV/JSON files",
      icon: "M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4",
    },
  ] as const;

  interface JsonFormatOption {
    readonly id: JsonFormat;
    readonly label: string;
    readonly example: string;
  }

  const jsonFormats: readonly JsonFormatOption[] = [
    {
      id: "object",
      label: "Single Object",
      example: '{ "key1": "value1", "key2": "value2" }',
    },
    {
      id: "array",
      label: "Array of Objects",
      example: '[{ "id": "1", "name": "Alice" }, ...]',
    },
    {
      id: "ndjson",
      label: "NDJSON",
      example: '{"id": "1", ...}\\n{"id": "2", ...}',
    },
  ] as const;

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleFormatSelect(format: ImportFormat): void {
    if (format === "json") {
      onSelect(format, localJsonFormat);
    } else {
      onSelect(format);
    }
  }

  function handleJsonFormatSelect(format: JsonFormat): void {
    localJsonFormat = format;
    if (selectedFormat === "json") {
      onSelect("json", format);
    }
  }
</script>

<div class="space-y-6">
  <div>
    <h3 class="mb-4 text-lg font-semibold">Select Import Format</h3>
    <p class="text-base-content/70 mb-6 text-sm">
      Choose the format of the file you want to import.
    </p>
  </div>

  <!-- Format Cards -->
  <div class="grid gap-4 md:grid-cols-3">
    {#each formats as format (format.id)}
      <button
        type="button"
        class="card cursor-pointer border-2 transition-all duration-200 hover:shadow-md
          {selectedFormat === format.id
          ? 'border-primary bg-primary/5 shadow-md'
          : 'border-base-300 hover:border-primary/50'}"
        onclick={() => handleFormatSelect(format.id)}
      >
        <div class="card-body items-center p-6 text-center">
          <div
            class="mb-3 flex h-14 w-14 items-center justify-center rounded-full
              {selectedFormat === format.id ? 'bg-primary/20' : 'bg-base-200'}"
          >
            <svg
              xmlns="http://www.w3.org/2000/svg"
              class="h-7 w-7 {selectedFormat === format.id
                ? 'text-primary'
                : 'text-base-content/60'}"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="1.5"
                d={format.icon}
              />
            </svg>
          </div>
          <h4 class="card-title text-base">{format.label}</h4>
          <p class="text-base-content/60 text-xs">{format.description}</p>

          <!-- Selection Indicator -->
          {#if selectedFormat === format.id}
            <div class="badge badge-primary badge-sm mt-2">Selected</div>
          {/if}
        </div>
      </button>
    {/each}
  </div>

  <!-- JSON Format Sub-options -->
  {#if selectedFormat === "json"}
    <div class="card bg-base-200/50 mt-4">
      <div class="card-body p-4">
        <h4 class="mb-3 text-sm font-semibold">JSON Format Type</h4>

        <div class="space-y-2">
          {#each jsonFormats as jf (jf.id)}
            <label
              class="flex cursor-pointer items-start gap-3 rounded-lg p-3 transition-colors
                {localJsonFormat === jf.id
                ? 'bg-primary/10'
                : 'hover:bg-base-300/50'}"
            >
              <input
                type="radio"
                name="json-format"
                class="radio radio-primary radio-sm mt-0.5"
                checked={localJsonFormat === jf.id}
                onchange={() => handleJsonFormatSelect(jf.id)}
              />
              <div class="flex-1">
                <span class="block font-medium">{jf.label}</span>
                <code class="text-base-content/50 mt-1 block text-xs"
                  >{jf.example}</code
                >
              </div>
            </label>
          {/each}
        </div>
      </div>
    </div>
  {/if}

  <!-- Archive Note -->
  {#if selectedFormat === "archive"}
    <div class="alert alert-info">
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
          d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <span class="text-sm">
        Archive import will process all supported files (CSV, JSON) within the
        archive. Each file will be imported using its detected format.
      </span>
    </div>
  {/if}
</div>
