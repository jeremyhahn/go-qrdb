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
  import type { Client, ListPermissionsResponse } from "@qrdb/sdk";
  import { showToast } from "$stores/toast.svelte.ts";

  let {
    client,
    selectedPermissions,
    onchange,
  }: {
    client: Client;
    selectedPermissions: string[];
    onchange: (permissions: string[]) => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface PermissionCategory {
    readonly name: string;
    readonly group: string;
    readonly permissions: readonly string[];
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let categories: PermissionCategory[] = $state([]);
  let loading: boolean = $state(true);
  let searchQuery: string = $state("");

  let groupMap = $derived<Map<string, PermissionCategory[]>>(
    (() => {
      const map = new Map<string, PermissionCategory[]>();
      for (const cat of categories) {
        const existing = map.get(cat.group);
        if (existing !== undefined) {
          existing.push(cat);
        } else {
          map.set(cat.group, [cat]);
        }
      }
      return map;
    })(),
  );

  let filteredGroupMap = $derived<Map<string, PermissionCategory[]>>(
    (() => {
      if (searchQuery.trim() === "") return groupMap;
      const query = searchQuery.toLowerCase();
      const filtered = new Map<string, PermissionCategory[]>();
      for (const [group, cats] of groupMap) {
        const matchingCats = cats
          .map((cat) => {
            const matchingPerms = cat.permissions.filter((p) =>
              p.toLowerCase().includes(query),
            );
            if (
              cat.name.toLowerCase().includes(query) ||
              cat.group.toLowerCase().includes(query) ||
              matchingPerms.length > 0
            ) {
              return matchingPerms.length > 0
                ? { ...cat, permissions: matchingPerms }
                : cat;
            }
            return null;
          })
          .filter((c): c is PermissionCategory => c !== null);
        if (matchingCats.length > 0) {
          filtered.set(group, matchingCats);
        }
      }
      return filtered;
    })(),
  );

  let filteredGroupNames = $derived<string[]>(
    Array.from(filteredGroupMap.keys()).sort(),
  );


  // ---------------------------------------------------------------------------
  // Selection helpers
  // ---------------------------------------------------------------------------

  let selectionSet = $derived<Set<string>>(new Set(selectedPermissions));

  function isSelected(perm: string): boolean {
    return selectionSet.has(perm);
  }

  function isCategoryAllSelected(cat: PermissionCategory): boolean {
    return cat.permissions.length > 0 && cat.permissions.every((p) => selectionSet.has(p));
  }

  function isCategoryPartiallySelected(cat: PermissionCategory): boolean {
    const count = cat.permissions.filter((p) => selectionSet.has(p)).length;
    return count > 0 && count < cat.permissions.length;
  }

  function isGroupAllSelected(groupCats: PermissionCategory[]): boolean {
    const all = groupCats.flatMap((c) => c.permissions);
    return all.length > 0 && all.every((p) => selectionSet.has(p));
  }

  function isGroupPartiallySelected(groupCats: PermissionCategory[]): boolean {
    const allPerms = groupCats.flatMap((c) => c.permissions);
    const count = allPerms.filter((p) => selectionSet.has(p)).length;
    return count > 0 && count < allPerms.length;
  }

  // ---------------------------------------------------------------------------
  // Mutation helpers
  // ---------------------------------------------------------------------------

  function emitChange(next: string[]): void {
    onchange(next);
  }

  function togglePermission(perm: string): void {
    if (selectionSet.has(perm)) {
      emitChange(selectedPermissions.filter((p) => p !== perm));
    } else {
      emitChange([...selectedPermissions, perm]);
    }
  }

  function toggleCategory(cat: PermissionCategory): void {
    if (isCategoryAllSelected(cat)) {
      const toRemove = new Set(cat.permissions);
      emitChange(selectedPermissions.filter((p) => !toRemove.has(p)));
    } else {
      const toAdd = cat.permissions.filter((p) => !selectionSet.has(p));
      emitChange([...selectedPermissions, ...toAdd]);
    }
  }

  function toggleGroup(groupCats: PermissionCategory[]): void {
    const allPerms = groupCats.flatMap((c) => c.permissions);
    if (isGroupAllSelected(groupCats)) {
      const toRemove = new Set(allPerms);
      emitChange(selectedPermissions.filter((p) => !toRemove.has(p)));
    } else {
      const toAdd = allPerms.filter((p) => !selectionSet.has(p));
      emitChange([...selectedPermissions, ...toAdd]);
    }
  }

  function selectAll(): void {
    const all = categories.flatMap((c) => c.permissions);
    emitChange(Array.from(new Set([...selectedPermissions, ...all])));
  }

  function clearAll(): void {
    emitChange([]);
  }

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  async function fetchPermissions(): Promise<void> {
    try {
      loading = true;
      const response: ListPermissionsResponse =
        await client.roles.listPermissions();
      categories = [...response.categories] as PermissionCategory[];
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to fetch permissions";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void fetchPermissions();
  });
</script>

<div class="flex flex-col gap-3">
  <!-- Toolbar: search + select-all / clear-all -->
  <div class="flex flex-wrap items-center gap-2">
    <input
      type="text"
      class="input input-bordered input-sm flex-1 min-w-[200px]"
      placeholder="Filter permissions…"
      bind:value={searchQuery}
    />
    <button
      type="button"
      class="btn btn-ghost btn-sm"
      onclick={selectAll}
      disabled={loading || categories.length === 0}
      title="Select every permission"
    >
      Select all
    </button>
    <button
      type="button"
      class="btn btn-ghost btn-sm"
      onclick={clearAll}
      disabled={loading || selectedPermissions.length === 0}
      title="Clear all selections"
    >
      Clear
    </button>
    <span class="badge badge-neutral badge-sm">
      {selectedPermissions.length} selected
    </span>
  </div>

  {#if loading}
    <div class="flex items-center justify-center py-8">
      <span class="loading loading-spinner loading-md text-primary"></span>
    </div>
  {:else if filteredGroupNames.length === 0}
    <div class="bg-base-200 rounded-lg py-8 text-center">
      <p class="text-base-content/60 text-sm">
        {categories.length === 0
          ? "No permissions available."
          : "No permissions match the filter."}
      </p>
    </div>
  {:else}
    <div class="space-y-3">
      {#each filteredGroupNames as groupName (groupName)}
        {@const groupCats = filteredGroupMap.get(groupName) ?? []}
        {@const groupSelected = isGroupAllSelected(groupCats)}
        {@const groupIndeterminate = isGroupPartiallySelected(groupCats)}
        {@const groupPerms = groupCats.flatMap((c) => c.permissions)}
        {@const groupHit = groupPerms.filter((p) => isSelected(p)).length}

        <details class="group border-base-300 overflow-hidden rounded-lg border bg-base-200" open>
          <summary
            class="hover:bg-base-200/70 flex cursor-pointer select-none list-none items-center gap-3 px-4 py-2.5"
          >
            <!-- Chevron indicator -->
            <svg
              class="h-4 w-4 shrink-0 text-base-content/50 transition-transform group-open:rotate-90"
              viewBox="0 0 20 20"
              fill="currentColor"
              aria-hidden="true"
            >
              <path
                fill-rule="evenodd"
                d="M7.21 14.77a.75.75 0 0 1 .02-1.06L11.168 10 7.23 6.29a.75.75 0 1 1 1.04-1.08l4.5 4.25a.75.75 0 0 1 0 1.08l-4.5 4.25a.75.75 0 0 1-1.06-.02Z"
                clip-rule="evenodd"
              />
            </svg>
            <!-- Group-level checkbox. Click stopPropagation prevents the
                 parent <summary> from toggling the details open/closed; the
                 native change event drives the selection state so Playwright
                 and screen readers see a proper checkbox toggle. -->
            <input
              type="checkbox"
              class="checkbox checkbox-sm checkbox-primary"
              checked={groupSelected}
              indeterminate={groupIndeterminate}
              onclick={(e: MouseEvent) => e.stopPropagation()}
              onchange={() => toggleGroup(groupCats)}
              aria-label="Toggle all permissions in {groupName}"
            />
            <span class="flex-1 text-sm font-semibold">{groupName}</span>
            <span class="badge badge-sm badge-ghost">
              {groupHit}/{groupPerms.length}
            </span>
          </summary>

          <div class="border-base-300 border-t">
            {#each groupCats as cat (cat.name)}
              {@const catSelected = isCategoryAllSelected(cat)}
              {@const catIndeterminate = isCategoryPartiallySelected(cat)}
              {@const catHit = cat.permissions.filter((p) => isSelected(p)).length}

              <details class="group/cat border-base-300 border-b last:border-b-0">
                      <summary
                  class="hover:bg-base-300/40 flex cursor-pointer select-none list-none items-center gap-3 px-4 py-2"
                >
                  <svg
                    class="h-3.5 w-3.5 shrink-0 text-base-content/40 transition-transform group-open/cat:rotate-90"
                    viewBox="0 0 20 20"
                    fill="currentColor"
                    aria-hidden="true"
                  >
                    <path
                      fill-rule="evenodd"
                      d="M7.21 14.77a.75.75 0 0 1 .02-1.06L11.168 10 7.23 6.29a.75.75 0 1 1 1.04-1.08l4.5 4.25a.75.75 0 0 1 0 1.08l-4.5 4.25a.75.75 0 0 1-1.06-.02Z"
                      clip-rule="evenodd"
                    />
                  </svg>
                  <input
                    type="checkbox"
                    class="checkbox checkbox-xs checkbox-secondary"
                    checked={catSelected}
                    indeterminate={catIndeterminate}
                    onclick={(e: MouseEvent) => {
                      e.stopPropagation();
                      e.preventDefault();
                      toggleCategory(cat);
                    }}
                    aria-label="Toggle all permissions in {cat.name}"
                  />
                  <span class="flex-1 text-sm">{cat.name}</span>
                  <span class="badge badge-xs badge-ghost">
                    {catHit}/{cat.permissions.length}
                  </span>
                </summary>

                <ul class="bg-base-100/50 divide-y divide-base-300/40">
                  {#each cat.permissions as perm (perm)}
                    <li>
                      <label
                        class="hover:bg-base-200/60 flex cursor-pointer items-center gap-3 px-4 py-1.5 pl-14"
                      >
                        <input
                          type="checkbox"
                          class="checkbox checkbox-xs"
                          checked={isSelected(perm)}
                          onchange={() => togglePermission(perm)}
                        />
                        <span class="font-mono text-xs break-all">{perm}</span>
                      </label>
                    </li>
                  {/each}
                </ul>
              </details>
            {/each}
          </div>
        </details>
      {/each}
    </div>
  {/if}
</div>

<style>
  /* Hide the default disclosure marker in all browsers. */
  summary::-webkit-details-marker {
    display: none;
  }
  summary {
    list-style: none;
  }
</style>
