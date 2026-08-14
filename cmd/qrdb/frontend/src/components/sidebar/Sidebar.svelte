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
  Sidebar is the primary navigation shell. In desktop mode it renders the
  full multi-server tree. In web mode it renders WebSidebar (single implicit
  server, no connection management).

  Collapse state is owned by navStore and toggled by the header button.
  Data loading and SDK calls are delegated upward via callbacks so this
  component stays free of async logic.
-->
<script lang="ts">
  import type { DatabaseInfo } from "@qrdb/sdk";
  import { Plus } from "lucide-svelte";
  import { navStore } from "../../stores/navigation.svelte.ts";
  import {
    connManagerStore,
  } from "../../stores/connection-manager.svelte.ts";
  import type { ConnectionState } from "../../stores/connection-manager.svelte.ts";
  import { databaseStore } from "../../stores/database.svelte.ts";
  import ServerNode from "./ServerNode.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  interface Props {
    /**
     * Per-server database lists keyed by profile ID.
     * Only the active server's databases are surfaced through databaseStore;
     * other servers use this map so that expanding a second server shows its
     * databases without overwriting the active server's list.
     */
    perServerDatabases: ReadonlyMap<string, readonly DatabaseInfo[]>;
    onconnect: (profileId: string) => Promise<void>;
    ondisconnect: (profileId: string) => Promise<void>;
    onactivate: (profileId: string) => Promise<void>;
    onaddserver: () => void;
  }

  let {
    perServerDatabases,
    onconnect,
    ondisconnect,
    onactivate,
    onaddserver,
  }: Props = $props();

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  /** Returns the databases for a profile, using the active store for the active profile. */
  function databasesForProfile(profileId: string): readonly DatabaseInfo[] {
    if (profileId === connManagerStore.activeProfileId) {
      return databaseStore.databases;
    }
    return perServerDatabases.get(profileId) ?? [];
  }

  /** Returns the connection state for a profile. */
  function stateForProfile(profileId: string): ConnectionState | undefined {
    return connManagerStore.states.get(profileId);
  }
</script>

<aside
  class="surface-panel flex shrink-0 flex-col overflow-y-auto border-r border-base-content/8 transition-all duration-200 ease-smooth"
  class:w-56={!navStore.sidebarCollapsed}
  class:w-14={navStore.sidebarCollapsed}
  style="border-radius: 0;"
>
  <nav class="flex-1 space-y-0.5 p-2">

    <!-- ======================================================================
         SERVER TREE: shared between desktop and web modes.
         In web mode the "This Server" built-in profile is always present so
         the same multi-server tree renders with exactly one entry on first
         load. The [+] Add Server button is shown in both modes; the dialog
         hides the Embedded option when !isDesktop().
         ====================================================================== -->

    <!-- Servers header (expanded state) -->
    {#if !navStore.sidebarCollapsed}
      <div class="mt-1 mb-2 flex items-center justify-between px-2">
        <span class="text-base-content/40 text-[10px] font-semibold uppercase tracking-wider">
          Servers
        </span>
        <button
          class="btn btn-ghost btn-circle btn-xs text-base-content/40 hover:text-base-content"
          onclick={onaddserver}
          title="Add server"
        >
          <Plus class="h-3.5 w-3.5" />
        </button>
      </div>
    {/if}

    <!-- Server list -->
    {#if connManagerStore.loading}
      <div class="flex justify-center py-4">
        <span class="loading loading-spinner loading-sm text-base-content/30"></span>
      </div>
    {:else if connManagerStore.profiles.length === 0}
      {#if !navStore.sidebarCollapsed}
        <p class="text-base-content/40 px-3 py-2 text-xs">No servers configured.</p>
      {/if}
    {:else}
      {#each connManagerStore.profiles as profile (profile.id)}
        <div class="relative">
          <ServerNode
            {profile}
            connState={stateForProfile(profile.id)}
            databases={databasesForProfile(profile.id)}
            dbLoading={databaseStore.loading}
            isActive={profile.id === connManagerStore.activeProfileId}
            collapsed={navStore.sidebarCollapsed}
            {onconnect}
            {ondisconnect}
            {onactivate}
          />
        </div>
      {/each}
    {/if}

    <!-- Add server — always visible at bottom in collapsed mode -->
    {#if navStore.sidebarCollapsed}
      <div class="mt-2 border-t border-base-content/10 pt-2">
        <button
          class="flex w-full items-center justify-center rounded-lg px-2 py-2 text-base-content/40 transition-colors duration-150 hover:bg-base-content/5 hover:text-base-content"
          onclick={onaddserver}
          title="Add server"
        >
          <Plus class="h-4 w-4" />
        </button>
      </div>
    {/if}

  </nav>
</aside>
