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
  ServerNode renders a single server entry in the desktop sidebar tree.

  The node has two collapsible child sections: Databases and Server Admin.
  Both sections are scoped to this server — clicking admin items here
  always targets this specific server.

  Expansion state lives in navStore.expandedServers. Connection actions are
  delegated upward via callbacks so MainApp can coordinate SDK calls.
-->
<script lang="ts">
  import type { DatabaseInfo } from "@qrdb/sdk";
  import {
    HardDrive,
    Server,
    ChevronRight,
    Database,
    Shield,
    LogOut,
  } from "lucide-svelte";
  import type { ConnectionProfile, ConnectionState } from "../../stores/connection-manager.svelte.ts";
  import { hasAdminViews } from "../../stores/connection-manager.svelte.ts";
  import {
    navStore,
    toggleServerExpanded,
    navigateToAdmin,
    expandServer,
  } from "../../stores/navigation.svelte.ts";
  import DatabaseList from "./DatabaseList.svelte";
  import AdminNav from "./AdminNav.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  interface Props {
    profile: ConnectionProfile;
    connState: ConnectionState | undefined;
    databases: readonly DatabaseInfo[];
    dbLoading?: boolean;
    isActive: boolean;
    collapsed?: boolean;
    onconnect: (profileId: string) => Promise<void>;
    ondisconnect: (profileId: string) => Promise<void>;
    onactivate: (profileId: string) => Promise<void>;
  }

  let {
    profile,
    connState,
    databases,
    dbLoading = false,
    isActive,
    collapsed = false,
    onconnect,
    ondisconnect,
    onactivate,
  }: Props = $props();

  // ---------------------------------------------------------------------------
  // Internal state
  // ---------------------------------------------------------------------------

  /** Whether the Databases sub-section is expanded. */
  let dbSectionOpen: boolean = $state(true);
  /** Whether the Server Admin sub-section is expanded. */
  let adminSectionOpen: boolean = $state(true);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const isExpanded = $derived(navStore.expandedServers.has(profile.id));
  const isConnected = $derived(connState?.status === 'connected');
  const isSealed = $derived(connState?.status === 'sealed');
  const isConnecting = $derived(connState?.status === 'connecting');
  const ProfileIcon = $derived(profile.type === 'embedded' ? HardDrive : Server);

  /** CSS class for the status indicator dot. */
  const statusDotClass = $derived.by((): string => {
    switch (connState?.status) {
      case 'connected':   return 'bg-success';
      case 'sealed':
      case 'needs-init':  return 'bg-warning';
      case 'connecting':  return 'bg-base-content/30 animate-pulse';
      default:            return 'bg-base-content/20';
    }
  });

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  async function handleServerRowClick(): Promise<void> {
    if (isConnected) {
      if (!isActive) {
        // Switch active server then expand.
        await onactivate(profile.id);
        navigateToAdmin(profile.id, 'dashboard');
        expandServer(profile.id);
      } else {
        // Toggle expand/collapse on already-active server.
        toggleServerExpanded(profile.id);
      }
    } else if (!isConnecting) {
      await onconnect(profile.id);
    }
  }

  function handleDisconnect(e: Event): void {
    e.stopPropagation();
    void ondisconnect(profile.id);
  }

  function toggleDbSection(e: Event): void {
    e.stopPropagation();
    dbSectionOpen = !dbSectionOpen;
  }

  function toggleAdminSection(e: Event): void {
    e.stopPropagation();
    adminSectionOpen = !adminSectionOpen;
  }
</script>

<!-- Server row -->
<button
  class="group flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-sm transition-colors duration-150
    {isActive && isConnected
      ? 'bg-primary/8 text-base-content'
      : 'text-base-content/70 hover:bg-base-content/5 hover:text-base-content/90'}"
  class:justify-center={collapsed}
  onclick={handleServerRowClick}
  title={collapsed ? profile.name : undefined}
>
  <!-- Expand chevron (full width only) -->
  {#if !collapsed}
    <ChevronRight
      class="h-3 w-3 shrink-0 text-base-content/30 transition-transform duration-150
        {isExpanded ? 'rotate-90' : ''}"
    />
  {/if}

  <!-- Server type icon -->
  <ProfileIcon class="h-4 w-4 shrink-0" />

  <!-- Server name + status -->
  {#if !collapsed}
    <span class="flex-1 truncate text-left text-xs font-medium">{profile.name}</span>

    {#if isConnecting}
      <span class="loading loading-spinner loading-xs shrink-0 text-base-content/40"></span>
    {:else}
      <span class="h-1.5 w-1.5 shrink-0 rounded-full {statusDotClass}"></span>
    {/if}
  {:else}
    <!-- Collapsed: status dot only -->
    {#if isConnecting}
      <span class="loading loading-spinner loading-xs absolute bottom-0.5 right-0.5 h-1.5 w-1.5 text-base-content/40"></span>
    {:else}
      <span class="absolute bottom-0.5 right-0.5 h-1.5 w-1.5 rounded-full {statusDotClass}"></span>
    {/if}
  {/if}
</button>

<!-- Expanded subtree (hidden when collapsed or not expanded) -->
{#if isExpanded && !collapsed}
  <div class="ml-4 border-l border-base-content/8 pl-2">

    {#if isConnected || isSealed}
      <!-- ── Databases section (only when fully connected) ────────── -->
      {#if isConnected}
        <button
          class="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 text-[10px] font-semibold uppercase tracking-wider text-base-content/40 hover:text-base-content/60 transition-colors"
          onclick={toggleDbSection}
        >
          <ChevronRight class="h-2.5 w-2.5 transition-transform {dbSectionOpen ? 'rotate-90' : ''}" />
          <Database class="h-3 w-3" />
          Databases
        </button>

        {#if dbSectionOpen}
          <div class="ml-1 mt-0.5 mb-1">
            <DatabaseList
              profileId={profile.id}
              {databases}
              loading={isActive && dbLoading}
              {isActive}
              collapsed={false}
            />
          </div>
        {/if}
      {/if}

      <!-- ── Server Admin section: not offered when the server refused the
           caller its admin topology (403), i.e. to a tenant user. ── -->
      {#if hasAdminViews(profile.id)}
      <button
        class="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 text-[10px] font-semibold uppercase tracking-wider text-base-content/40 hover:text-base-content/60 transition-colors"
        onclick={toggleAdminSection}
      >
        <ChevronRight class="h-2.5 w-2.5 transition-transform {adminSectionOpen ? 'rotate-90' : ''}" />
        <Shield class="h-3 w-3" />
        Server Admin
      </button>

      {#if adminSectionOpen}
        <div class="ml-1 mt-0.5 mb-1">
          <AdminNav
            profileId={profile.id}
            serverMode={profile.serverMode ?? 'standalone'}
            profileType={profile.type}
            collapsed={false}
          />
        </div>
      {/if}
      {/if}

      <!-- ── Disconnect ────────────────────────────────────────────── -->
      {#if isConnected}
        <button
          class="mt-1 flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-base-content/30 transition-colors duration-150 hover:bg-error/8 hover:text-error"
          onclick={handleDisconnect}
        >
          <LogOut class="h-3.5 w-3.5 shrink-0" />
          <span>Disconnect</span>
        </button>
      {/if}

    {:else}
      <!-- Not connected — show connect affordance -->
      <button
        class="mt-0.5 flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-xs text-base-content/40 transition-colors duration-150 hover:bg-base-content/5 hover:text-primary"
        onclick={() => onconnect(profile.id)}
        disabled={isConnecting}
      >
        <span>Connect</span>
      </button>
    {/if}

  </div>
{/if}
