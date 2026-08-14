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
  import { showToast } from "../stores/toast.svelte.ts";
  import { Copy } from "lucide-svelte";

  let {
    value,
    label = "",
  }: {
    value: string;
    label?: string;
  } = $props();

  /** Attempt to pretty-print JSON, falling back to raw text. */
  let formatted = $derived.by(() => {
    try {
      const parsed = JSON.parse(value);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return value;
    }
  });

  let isJson = $derived.by(() => {
    try {
      JSON.parse(value);
      return true;
    } catch {
      return false;
    }
  });

  async function copyToClipboard(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      showToast("success", "Copied to clipboard");
    } catch {
      showToast("error", "Failed to copy to clipboard");
    }
  }
</script>

<div class="relative">
  {#if label}
    <div class="mb-1 flex items-center justify-between">
      <span class="text-base-content/70 text-xs font-semibold">{label}</span>
      <div class="flex items-center gap-1">
        {#if isJson}
          <span class="badge badge-xs badge-info">JSON</span>
        {/if}
        <button
          class="btn btn-ghost btn-xs"
          onclick={copyToClipboard}
          aria-label="Copy"
        >
          <Copy class="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  {/if}
  <pre class="bg-base-300 max-h-64 overflow-auto whitespace-pre-wrap rounded-lg p-3 font-mono text-xs">{formatted}</pre>
</div>
