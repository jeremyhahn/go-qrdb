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
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconProp = string | any | null;

  let {
    icon = null,
    title,
    description = "",
    actionLabel = "",
    onaction,
  }: {
    icon?: IconProp;
    title: string;
    description?: string;
    actionLabel?: string;
    onaction?: () => void;
  } = $props();

  let isComponent = $derived(icon !== null && typeof icon !== "string");
</script>

<div class="flex flex-col items-center justify-center py-12 text-center">
  {#if icon}
    {#if isComponent}
      {@const Icon = icon}
      <Icon class="text-base-content/30 mb-4 h-12 w-12" strokeWidth={1.5} />
    {:else}
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="text-base-content/30 mb-4 h-12 w-12"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="1.5"
          d={icon as string}
        />
      </svg>
    {/if}
  {/if}
  <h3 class="text-base-content/70 text-lg font-medium">{title}</h3>
  {#if description}
    <p class="text-base-content/50 mt-1 max-w-sm text-sm">{description}</p>
  {/if}
  {#if actionLabel && onaction}
    <button class="btn btn-primary btn-sm mt-4" onclick={onaction}>
      {actionLabel}
    </button>
  {/if}
</div>
