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
  import type { Snippet } from "svelte";
  import { fly, fade } from "svelte/transition";
  import { X } from "lucide-svelte";

  let {
    open,
    title,
    width = 384,
    onclose,
    children,
    footer = undefined,
  }: {
    open: boolean;
    title: string;
    width?: number;
    onclose: () => void;
    children: Snippet;
    footer?: Snippet | undefined;
  } = $props();

  /** Handles keyboard events on the backdrop for accessibility. */
  function handleBackdropKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      onclose();
    }
  }
</script>

{#if open}
  <!-- Backdrop overlay -->
  <div
    class="fixed inset-0 z-40 bg-black/40"
    transition:fade={{ duration: 200 }}
    onclick={onclose}
    onkeydown={handleBackdropKeydown}
    role="button"
    tabindex="-1"
    aria-label="Close panel"
  ></div>

  <!-- Slide panel -->
  <div
    class="surface-panel border-base-content/6 fixed inset-y-0 right-0 z-50 flex flex-col border-l"
    style="width: {width}px"
    transition:fly={{ x: width, duration: 250 }}
    role="dialog"
    aria-modal="true"
    aria-label={title}
  >
    <!-- Header -->
    <div
      class="border-base-content/6 flex items-center justify-between border-b px-6 py-4"
    >
      <h2 class="text-lg font-semibold">{title}</h2>
      <button
        class="btn btn-ghost btn-sm btn-square"
        onclick={onclose}
        aria-label="Close"
      >
        <X class="h-5 w-5" />
      </button>
    </div>

    <!-- Body -->
    <div class="flex-1 overflow-y-auto p-6">
      {@render children()}
    </div>

    <!-- Footer (optional) -->
    {#if footer}
      <div class="border-base-content/6 border-t px-6 py-4">
        {@render footer()}
      </div>
    {/if}
  </div>
{/if}
