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
  import type { Component } from "svelte";
  import { MoreHorizontal } from "lucide-svelte";

  type MenuItemVariant = "default" | "danger";

  interface MenuItem {
    label: string;
    icon?: Component;
    variant?: MenuItemVariant;
    onclick: () => void;
  }

  type DropdownPosition = "left" | "right";

  let {
    items,
    position = "right",
  }: {
    items: MenuItem[];
    position?: DropdownPosition;
  } = $props();

  /** Handles item click: invokes the callback and closes the dropdown. */
  function handleItemClick(item: MenuItem): void {
    item.onclick();
    if (document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }
  }
</script>

<div class="dropdown" class:dropdown-end={position === "right"}>
  <button
    class="btn btn-ghost btn-xs btn-square opacity-0 transition-opacity group-hover:opacity-100"
    tabindex="0"
    aria-label="Actions menu"
  >
    <MoreHorizontal class="h-4 w-4" />
  </button>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <ul
    tabindex="0"
    class="border-base-content/6 dropdown-content menu z-50 w-48 rounded-lg border bg-base-200 p-1 shadow-lg"
  >
    {#each items as item}
      <li>
        <button
          class="flex items-center gap-2 text-sm"
          class:text-error={item.variant === "danger"}
          onclick={() => handleItemClick(item)}
        >
          {#if item.icon}
            {@const ItemIcon = item.icon}
            <ItemIcon class="h-4 w-4" />
          {/if}
          {item.label}
        </button>
      </li>
    {/each}
  </ul>
</div>
