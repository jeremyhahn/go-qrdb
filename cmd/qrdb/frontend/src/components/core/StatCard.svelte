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
  import { TrendingUp, TrendingDown } from "lucide-svelte";

  type TrendDirection = "up" | "down" | "neutral";

  let {
    label,
    value,
    unit = "",
    icon = undefined,
    accentColor = "",
    trend = undefined,
    trendValue = "",
  }: {
    label: string;
    value: string | number;
    unit?: string;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    icon?: any;
    accentColor?: string;
    trend?: TrendDirection | undefined;
    trendValue?: string;
  } = $props();

  const accentStyle = $derived(
    accentColor ? `border-left: 3px solid ${accentColor}` : "",
  );
</script>

<div class="surface-panel rounded-lg p-4" style={accentStyle}>
  <div class="flex items-start gap-3">
    {#if icon}
      {@const IconComponent = icon}
      <div class="text-base-content/40 mt-0.5 shrink-0">
        <IconComponent class="h-5 w-5" />
      </div>
    {/if}

    <div class="flex flex-col gap-1">
      <span
        class="text-base-content/50 text-xs font-medium uppercase tracking-wider"
      >
        {label}
      </span>

      <div class="flex items-baseline gap-1.5">
        <span class="font-mono text-2xl font-semibold">{value}</span>
        {#if unit}
          <span class="text-base-content/50 text-sm">{unit}</span>
        {/if}
      </div>

      {#if trend && trendValue}
        {@const trendClass =
          trend === "up"
            ? "text-success"
            : trend === "down"
              ? "text-error"
              : "text-base-content/50"}
        <div
          class="mt-0.5 flex items-center gap-1 text-xs font-medium {trendClass}"
        >
          {#if trend === "up"}
            <TrendingUp class="h-3.5 w-3.5" />
          {:else if trend === "down"}
            <TrendingDown class="h-3.5 w-3.5" />
          {/if}
          <span>{trendValue}</span>
        </div>
      {/if}
    </div>
  </div>
</div>
