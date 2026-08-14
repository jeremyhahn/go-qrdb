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

<!--
  DatabaseList renders the database nodes for a single server inside the
  sidebar tree. It handles database selection, loading state, empty state, and
  the "New Database" affordance.

  Navigation is driven through navStore. The parent (ServerNode or WebSidebar)
  is responsible for triggering data loads; this component is purely
  presentational.
-->
<script lang="ts">
  import type { DatabaseInfo } from "@qrdb/sdk";
  import {
    Database,
    TrendingUp,
    Hexagon,
    Plus,
  } from "lucide-svelte";
  // lucide-svelte icons are legacy Svelte 4 class components.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconComponent = any;
  import { navStore, navigateToDatabase, navigateToCreateDatabase } from "../../stores/navigation.svelte.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  interface Props {
    /** The profile ID that owns these databases. */
    profileId: string;
    /** Databases to display. */
    databases: readonly DatabaseInfo[];
    /** Whether the database list is currently loading. */
    loading?: boolean;
    /** Whether this is the currently active (connected) server. */
    isActive?: boolean;
    /** When true renders icon-only (sidebar collapsed). */
    collapsed?: boolean;
  }

  let {
    profileId,
    databases,
    loading = false,
    isActive = false,
    collapsed = false,
  }: Props = $props();

  // ---------------------------------------------------------------------------
  // Constants
  // ---------------------------------------------------------------------------

  const ENGINE_ICONS: Readonly<Record<string, IconComponent>> = {
    kv: Database,
    timeseries: TrendingUp,
    vector: Hexagon,
  };

  const ENGINE_BADGE_CLASSES: Readonly<Record<string, string>> = {
    kv: 'badge-secondary',
    timeseries: 'badge-warning',
    vector: 'badge-accent',
  };

  const ENGINE_BADGE_LABELS: Readonly<Record<string, string>> = {
    kv: 'kv',
    timeseries: 'ts',
    vector: 'vec',
  };

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** The name of the currently selected database for this server, or null. */
  const selectedDbName = $derived.by((): string | null => {
    const ctx = navStore.context;
    if (ctx.type !== 'database') return null;
    if ((ctx as { profileId: string }).profileId !== profileId) return null;
    return (ctx as { dbName: string }).dbName;
  });

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  function handleSelectDatabase(db: DatabaseInfo): void {
    navigateToDatabase(profileId, db.name);
  }

  function handleCreateDatabase(): void {
    navigateToCreateDatabase(profileId);
  }
</script>

{#if loading}
  <div class="flex justify-center py-3">
    <span class="loading loading-spinner loading-xs text-base-content/30"></span>
  </div>
{:else if databases.length === 0}
  {#if !collapsed}
    <p class="text-base-content/30 px-2 py-1.5 text-xs">No databases.</p>
  {/if}
{:else}
  {#each databases as db (db.name)}
    {@const EngineIcon = ENGINE_ICONS[db.engine] ?? Database}
    {@const isSelected = selectedDbName === db.name}
    <button
      class="group flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-xs transition-colors duration-150
        {isSelected
          ? 'bg-primary/10 text-base-content font-medium'
          : 'text-base-content/60 hover:bg-base-content/5 hover:text-base-content/90'}"
      class:justify-center={collapsed}
      onclick={() => handleSelectDatabase(db)}
      title={collapsed ? `${db.name} (${db.engine})` : db.name}
    >
      <EngineIcon class="h-3.5 w-3.5 shrink-0 {isSelected ? 'text-primary' : ''}" />
      {#if !collapsed}
        <span class="flex-1 truncate text-left">{db.name}</span>
        <span class="badge badge-xs text-[9px] {ENGINE_BADGE_CLASSES[db.engine] ?? 'badge-ghost'}">
          {ENGINE_BADGE_LABELS[db.engine] ?? db.engine}
        </span>
      {/if}
    </button>
  {/each}
{/if}

{#if isActive && !collapsed}
  <button
    class="mt-0.5 flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-base-content/40 transition-colors duration-150 hover:bg-base-content/5 hover:text-base-content"
    onclick={handleCreateDatabase}
  >
    <Plus class="h-3.5 w-3.5 shrink-0" />
    <span>New Database</span>
  </button>
{:else if isActive && collapsed}
  <button
    class="flex w-full items-center justify-center rounded-lg px-2 py-1.5 text-xs text-base-content/40 transition-colors duration-150 hover:bg-base-content/5 hover:text-base-content"
    onclick={handleCreateDatabase}
    title="New Database"
  >
    <Plus class="h-3.5 w-3.5 shrink-0" />
  </button>
{/if}
