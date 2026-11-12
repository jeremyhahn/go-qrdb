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
  import type { CellAlignment } from "./types.ts";
  import { showToast } from "$stores/toast.svelte.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    value,
    rendered = undefined,
    align = "left",
    width = undefined,
    minWidth = undefined,
    maxWidth = undefined,
    copyable = true,
  }: {
    value: unknown;
    rendered?: string | undefined;
    align?: CellAlignment | undefined;
    width?: string | undefined;
    minWidth?: string | undefined;
    maxWidth?: string | undefined;
    copyable?: boolean | undefined;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let showTooltip = $state(false);
  let isOverflowing = $state(false);
  let contentRef: HTMLSpanElement | null = $state(null);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Display value for the cell. */
  const displayValue = $derived(
    rendered !== undefined ? rendered : formatValue(value),
  );

  /** Alignment class. */
  const alignClass = $derived(
    align === "center"
      ? "text-center"
      : align === "right"
        ? "text-right"
        : "text-left",
  );

  /** Style object for cell dimensions. */
  const cellStyle = $derived.by(() => {
    const styles: string[] = [];
    if (width !== undefined) {
      styles.push(`width: ${width}`);
    }
    if (minWidth !== undefined) {
      styles.push(`min-width: ${minWidth}`);
    }
    if (maxWidth !== undefined) {
      styles.push(`max-width: ${maxWidth}`);
    }
    return styles.join("; ");
  });

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  /** Formats a value for display. */
  function formatValue(val: unknown): string {
    if (val === null || val === undefined) {
      return "";
    }
    if (val instanceof Date) {
      return val.toLocaleString();
    }
    if (typeof val === "object") {
      return JSON.stringify(val);
    }
    return String(val);
  }

  /**
   * Copies the cell value to clipboard. Only the copy button calls this,
   * and its click stops there: it never also counts as a row click. A
   * browser that refuses clipboard access (no permission, insecure
   * context, headless) gets a quiet info toast, not an error.
   */
  async function copyToClipboard(event: MouseEvent): Promise<void> {
    event.stopPropagation();
    try {
      await navigator.clipboard.writeText(displayValue);
      showToast("success", "Copied to clipboard");
    } catch {
      showToast("info", "Clipboard unavailable in this browser");
    }
  }

  /** Checks if content is overflowing. */
  function checkOverflow(): void {
    if (contentRef !== null) {
      isOverflowing = contentRef.scrollWidth > contentRef.clientWidth;
    }
  }

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  $effect(() => {
    if (contentRef === null) {
      return;
    }

    // Check overflow on mount and when value changes
    checkOverflow();

    // Use ResizeObserver for dynamic content
    const observer = new ResizeObserver(() => {
      checkOverflow();
    });
    observer.observe(contentRef);

    return () => {
      observer.disconnect();
    };
  });
</script>

<td
  class="relative px-3 py-2 {alignClass}"
  style={cellStyle}
  onmouseenter={() => {
    if (isOverflowing) showTooltip = true;
  }}
  onmouseleave={() => {
    showTooltip = false;
  }}
>
  <div class="group flex items-center gap-1">
    <span
      bind:this={contentRef}
      class="block overflow-hidden text-ellipsis whitespace-nowrap"
    >
      {#if rendered !== undefined}
        <!-- Column.render returns markup by contract; callers must escape data. -->
        <!-- eslint-disable-next-line svelte/no-at-html-tags -->
        {@html rendered}
      {:else}
        {displayValue}
      {/if}
    </span>

    {#if copyable && displayValue !== ""}
      <button
        type="button"
        class="btn btn-ghost btn-xs ml-auto shrink-0 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
        onclick={copyToClipboard}
        data-testid="cell-copy"
        title="Copy to clipboard"
      >
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-3.5 w-3.5"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
          />
        </svg>
      </button>
    {/if}
  </div>

  <!-- Tooltip for overflowing content -->
  {#if showTooltip && isOverflowing}
    <div
      class="bg-base-300 text-base-content absolute left-0 top-full z-50 mt-1 max-w-md rounded-lg p-2 text-sm shadow-lg"
      role="tooltip"
    >
      <div class="max-h-32 overflow-auto whitespace-pre-wrap break-all">
        {#if rendered !== undefined}
          <!-- Column.render returns markup by contract; callers must escape data. -->
          <!-- eslint-disable-next-line svelte/no-at-html-tags -->
          {@html rendered}
        {:else}
          {displayValue}
        {/if}
      </div>
    </div>
  {/if}
</td>
