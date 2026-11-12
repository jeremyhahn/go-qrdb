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
  type SkeletonVariant =
    "text" | "circle" | "rect" | "card" | "table-row" | "table";

  let {
    variant = "text",
    width,
    height,
    lines = 1,
    columns = 4,
    rows = 3,
    animate = true,
    class: className = "",
  }: {
    variant?: SkeletonVariant;
    width?: string;
    height?: string;
    lines?: number;
    columns?: number;
    rows?: number;
    animate?: boolean;
    class?: string;
  } = $props();

  const defaultDimensions: Readonly<
    Record<SkeletonVariant, { width: string; height: string }>
  > = {
    text: { width: "100%", height: "1rem" },
    circle: { width: "3rem", height: "3rem" },
    rect: { width: "100%", height: "4rem" },
    card: { width: "100%", height: "auto" },
    "table-row": { width: "100%", height: "3rem" },
    table: { width: "100%", height: "auto" },
  } as const;

  const cellWidths: readonly string[] = [
    "60%",
    "80%",
    "45%",
    "70%",
    "55%",
    "90%",
    "65%",
    "75%",
  ] as const;

  function getCellWidth(index: number): string {
    const idx = index % cellWidths.length;
    return cellWidths[idx] ?? "50%";
  }

  const computedWidth = $derived(width ?? defaultDimensions[variant].width);
  const computedHeight = $derived(height ?? defaultDimensions[variant].height);
  const animationClass = $derived(
    animate ? "skeleton-shimmer" : "skeleton-static",
  );
</script>

{#if variant === "text"}
  <div
    class="flex flex-col gap-2 {className}"
    style:width={computedWidth}
    role="status"
    aria-label="Loading"
  >
    {#each Array(lines) as _, i}
      <div
        class="skeleton-base {animationClass}"
        style:height={computedHeight}
        style:width={i === lines - 1 && lines > 1 ? "80%" : "100%"}
      ></div>
    {/each}
  </div>
{:else if variant === "circle"}
  <div
    class="skeleton-base skeleton-circle {animationClass} {className}"
    style:width={computedWidth}
    style:height={computedHeight}
    role="status"
    aria-label="Loading"
  ></div>
{:else if variant === "rect"}
  <div
    class="skeleton-base {animationClass} {className}"
    style:width={computedWidth}
    style:height={computedHeight}
    role="status"
    aria-label="Loading"
  ></div>
{:else if variant === "card"}
  <div
    class="skeleton-card-inner bg-base-200/50 {className}"
    style:width={computedWidth}
    role="status"
    aria-label="Loading card"
  >
    <div class="skeleton-base {animationClass} mb-3 h-5 w-3/5"></div>
    <div class="flex flex-col gap-2">
      <div class="skeleton-base {animationClass} h-4 w-full"></div>
      <div class="skeleton-base {animationClass} h-4 w-full"></div>
      <div class="skeleton-base {animationClass} h-4 w-4/5"></div>
    </div>
  </div>
{:else if variant === "table-row"}
  <div
    class="flex items-center gap-3 py-2 {className}"
    style:width={computedWidth}
    style:height={computedHeight}
    role="status"
    aria-label="Loading row"
  >
    {#each Array(columns) as _, i}
      <div
        class="skeleton-base {animationClass} h-4 flex-1"
        style:max-width={getCellWidth(i)}
      ></div>
    {/each}
  </div>
{:else if variant === "table"}
  <div
    class="w-full {className}"
    style:width={computedWidth}
    role="status"
    aria-label="Loading table"
  >
    <div class="border-base-300 mb-2 flex items-center gap-3 border-b py-3">
      {#each Array(columns) as _, i}
        <div
          class="skeleton-base {animationClass} h-4 flex-1"
          style:max-width={getCellWidth(i)}
        ></div>
      {/each}
    </div>
    {#each Array(rows) as _, rowIndex}
      <div
        class="border-base-300/50 flex items-center gap-3 border-b py-3 last:border-b-0"
      >
        {#each Array(columns) as _, colIndex}
          <div
            class="skeleton-base {animationClass} h-4 flex-1"
            style:max-width={getCellWidth(
              (rowIndex + colIndex) % cellWidths.length,
            )}
          ></div>
        {/each}
      </div>
    {/each}
  </div>
{/if}

<style>
  .skeleton-base {
    background-color: var(--skeleton-base);
    border-radius: var(--radius-md);
  }

  .skeleton-shimmer {
    background: linear-gradient(
      90deg,
      var(--skeleton-base) 25%,
      var(--skeleton-highlight) 50%,
      var(--skeleton-base) 75%
    );
    background-size: 200% 100%;
    animation: skeleton-shimmer var(--skeleton-duration) ease-in-out infinite;
  }

  .skeleton-static {
    background-color: var(--skeleton-base);
  }

  @keyframes skeleton-shimmer {
    0% {
      background-position: -200% 0;
    }
    100% {
      background-position: 200% 0;
    }
  }

  .skeleton-circle {
    border-radius: var(--radius-full);
  }

  .skeleton-card-inner {
    border-radius: var(--radius-lg);
    border: 1px solid var(--skeleton-base);
    padding: var(--space-4);
  }
</style>
