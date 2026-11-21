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
  AdminNav renders the server-level administration navigation items for a
  single server. It is shared between the desktop ServerNode component (where
  it appears as a collapsible subtree) and the WebSidebar component (where it
  appears as a top-level section with no server wrapping).

  It drives navigation through the navStore rather than emitting events so
  that the active-item highlight state is derived centrally.
-->
<script lang="ts">
  import { isDesktop } from "@qrdb/sdk";
  import {
    LayoutDashboard,
    Server,
    GitBranch,
    Lock,
    Building2,
    Users,
    Shield,
    FileText,
    HardDrive,
    Activity,
    ClipboardList,
    Archive,
    Scale,
    KeyRound,
    Layers,
    Settings as SettingsIcon,
  } from "lucide-svelte";
  // lucide-svelte icons are legacy Svelte 4 class components.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconComponent = any;
  import { navStore, navigateToAdmin } from "../../stores/navigation.svelte.ts";
  import type { AdminView } from "../../stores/navigation.svelte.ts";
  import { adminViewAllowed, hasAdminViews } from "../../stores/connection-manager.svelte.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  interface Props {
    /** The profile ID this admin nav is scoped to. */
    profileId: string;
    /** Server topology mode — hides cluster nav for standalone servers. */
    serverMode?: 'standalone' | 'raft';
    /**
     * Profile type — hides the Daemon item for embedded ("Local Server")
     * profiles, where qrdbd runs in-process and has no external OS process
     * to start/stop. Defaults to 'embedded' so unscoped callers stay
     * conservative.
     */
    profileType?: 'embedded' | 'external';
    /** When true the nav renders icon-only (sidebar collapsed). */
    collapsed?: boolean;
  }

  let {
    profileId,
    serverMode = 'standalone',
    profileType = 'embedded',
    collapsed = false,
  }: Props = $props();

  // ---------------------------------------------------------------------------
  // Item definitions
  // ---------------------------------------------------------------------------

  interface AdminItem {
    readonly id: AdminView;
    readonly label: string;
    readonly icon: IconComponent;
  }

  interface AdminSection {
    readonly title: string;
    readonly items: readonly AdminItem[];
  }

  // Each admin view shows only when the server allowed the caller its own
  // read route (connect-time probe); a refused (403) view is not offered,
  // and a section left with no view is dropped.
  const allSections: AdminSection[] = $derived.by(() => ([
    {
      title: 'Overview',
      items: [
        { id: 'dashboard' as AdminView, label: 'Dashboard', icon: LayoutDashboard },
        ...(serverMode === 'raft'
          ? [
              { id: 'cluster' as AdminView, label: 'Cluster', icon: Server },
              { id: 'topology' as AdminView, label: 'Topology', icon: GitBranch },
              { id: 'rebalance' as AdminView, label: 'Rebalance', icon: Scale },
            ]
          : []),
      ],
    },
    {
      title: 'Security',
      items: [
        { id: 'seal-manager',  label: 'Seal Manager',  icon: Lock },
        { id: 'tenants',       label: 'Tenants',       icon: Building2 },
        { id: 'users',         label: 'Users',         icon: Users },
        { id: 'roles',         label: 'Roles',         icon: Shield },
        { id: 'volume-pools',  label: 'Volume Pools',  icon: Layers },
        { id: 'certificates',  label: 'Certificates',  icon: FileText },
        { id: 'credentials' as AdminView, label: 'Credentials', icon: KeyRound },
      ],
    },
    {
      title: 'System',
      items: [
        { id: 'databases' as AdminView,  label: 'Databases',  icon: HardDrive },
        { id: 'backups' as AdminView,    label: 'Backups',    icon: Archive },
        ...(isDesktop() && profileType === 'external'
          ? [{ id: 'daemon' as AdminView, label: 'Daemon', icon: Activity }]
          : []),
        { id: 'audit-log' as AdminView,  label: 'Audit Log',  icon: ClipboardList },
        { id: 'settings' as AdminView,   label: 'Settings',   icon: SettingsIcon },
      ],
    },
  ] as AdminSection[])
    .map((s) => ({ ...s, items: s.items.filter((i) => adminViewAllowed(profileId, i.id)) }))
    .filter((s) => hasAdminViews(profileId) && s.items.length > 0));

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /** Whether this server's admin section is the currently active context. */
  const isActiveServer = $derived(
    navStore.context.type === 'server-admin' &&
    (navStore.context as { profileId: string }).profileId === profileId,
  );

  /** The active admin view for this server, or null. */
  const activeView = $derived.by((): AdminView | null => {
    if (!isActiveServer) return null;
    return (navStore.context as { view: AdminView }).view;
  });

  // ---------------------------------------------------------------------------
  // Handlers
  // ---------------------------------------------------------------------------

  function handleItemClick(view: AdminView): void {
    navigateToAdmin(profileId, view);
  }
</script>

{#each allSections as section (section.title)}
  {#if !collapsed}
    <div class="text-base-content/40 mt-2 mb-0.5 px-2 text-[9px] font-semibold uppercase tracking-wider">
      {section.title}
    </div>
  {:else}
    <div class="my-1 border-t border-base-content/10"></div>
  {/if}

  {#each section.items as item (item.id)}
    {@const Icon = item.icon}
    {@const isActive = activeView === item.id}
    <button
      class="nav-item-accent group flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-xs transition-colors duration-150
        {isActive ? 'active text-base-content' : 'text-base-content/60 hover:bg-base-content/5 hover:text-base-content/90'}"
      class:justify-center={collapsed}
      onclick={() => handleItemClick(item.id)}
      title={item.label}
    >
      <Icon class="nav-icon h-3.5 w-3.5 shrink-0" />
      {#if !collapsed}
        <span class="truncate">{item.label}</span>
      {/if}
    </button>
  {/each}
{/each}
