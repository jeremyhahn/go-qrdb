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
  // lucide-svelte icons are legacy Svelte 4 class components incompatible with
  // Svelte 5 Component<T> under exactOptionalPropertyTypes.
  import type { ComponentType } from "svelte";

  interface Tab {
    id: string;
    label: string;
    // lucide-svelte icons are class components.
    icon?: ComponentType;
    badge?: string | number;
  }

  let {
    tabs,
    activeTab,
    accentColor = "",
    onchange,
  }: {
    tabs: Tab[];
    activeTab: string;
    accentColor?: string;
    onchange: (tabId: string) => void;
  } = $props();
</script>

<div class="border-base-content/6 flex items-center gap-1 border-b" role="tablist">
  {#each tabs as tab (tab.id)}
    <button
      class="-mb-px flex items-center gap-2 border-b-2 px-3 py-2 text-sm font-medium transition-colors
             {tab.id === activeTab
               ? 'border-current text-base-content'
               : 'border-transparent text-base-content/50 hover:text-base-content/70'}"
      style={tab.id === activeTab && accentColor ? `color: ${accentColor}` : ""}
      role="tab"
      aria-selected={tab.id === activeTab}
      aria-controls="tabpanel-{tab.id}"
      onclick={() => onchange(tab.id)}
    >
      {#if tab.icon}
        {@const TabIcon = tab.icon}
        <TabIcon class="h-4 w-4" />
      {/if}
      {tab.label}
      {#if tab.badge !== undefined}
        <span class="badge badge-xs badge-primary">{tab.badge}</span>
      {/if}
    </button>
  {/each}
</div>
