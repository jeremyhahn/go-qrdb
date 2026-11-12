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

  let {
    title,
    description = "",
    accentColor = "",
    actions,
  }: {
    title: string;
    description?: string;
    accentColor?: string;
    actions?: Snippet;
  } = $props();
</script>

<div class="mb-8 flex items-center justify-between">
  <div
    class={accentColor ? "border-l-[3px] pl-3" : ""}
    style={accentColor ? `border-color: ${accentColor}` : ""}
  >
    <h1 class="text-2xl font-semibold tracking-tight">{title}</h1>
    {#if description}
      <p class="text-base-content/60 mt-1 text-sm leading-relaxed">
        {description}
      </p>
    {/if}
  </div>
  {#if actions}
    <div class="flex items-center gap-2">
      {@render actions()}
    </div>
  {/if}
</div>
