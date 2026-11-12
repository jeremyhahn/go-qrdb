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
  MainApp is the top-level application shell rendered after the server is
  reachable and unsealed. It owns the three-part layout (header, sidebar,
  content) and coordinates SDK interactions — connection lifecycle, database
  loading, and metadata polling — delegating all presentation to purpose-built
  child components.

  Navigation state lives in navStore. This component only reacts to that store
  and performs the data-loading side-effects that keep it accurate.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type { Client, SealStatus, DatabaseInfo } from "@qrdb/sdk";
  import { isDesktop } from "@qrdb/sdk";
  import { showToast } from "../stores/toast.svelte.ts";
  import { toggleTheme, themeStore } from "../stores/theme.svelte.ts";
  import { databaseStore, setDatabases, setLoading } from "../stores/database.svelte.ts";
  import { dbWizardStore, resetDatabaseWizard } from "../stores/database-wizard.svelte.ts";
  import { sealStore, updateSealStatus } from "../stores/seal.svelte.ts";
  import { withRetry } from "../lib/retry.ts";
  import {
    navStore,
    toggleSidebar,
    navigateToNone,
    navigateToAdmin,
    navigateToDatabase,
    cancelCreateDatabase,
    expandServer,
    collapseServer,
  } from "../stores/navigation.svelte.ts";
  import {
    ADMIN_VIEW_LABELS,
    DB_TOOL_LABELS,
  } from "../stores/navigation.svelte.ts";
  import {
    connManagerStore,
    loadProfiles,
    connectProfile,
    disconnectProfile,
    setActiveProfile,
    isMultiNodeServer,
  } from "../stores/connection-manager.svelte.ts";
  import type { ConnectionState } from "../stores/connection-manager.svelte.ts";

  import {
    Database,
    Server,
    Sun,
    Moon,
    Menu,
  } from "lucide-svelte";
  import logo from "../assets/logo.svg";

  // Layout components
  import Sidebar from "../components/sidebar/Sidebar.svelte";
  import StatusBadge from "../components/StatusBadge.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import ConnectionManager from "../components/ConnectionManager.svelte";

  // Database workspace
  import DatabaseWorkspace from "./user/DatabaseWorkspace.svelte";
  import DatabaseWizard from "../components/wizard/database/DatabaseWizard.svelte";

  // Admin views
  import Dashboard from "./admin/Dashboard.svelte";
  import Cluster from "./admin/Cluster.svelte";
  import Topology from "./admin/Topology.svelte";
  import SealManager from "./admin/SealManager.svelte";
  import UserManager from "./admin/UserManager.svelte";
  import RoleManager from "./admin/RoleManager.svelte";
  import VolumePoolManager from "./admin/VolumePoolManager.svelte";
  import CertManager from "./admin/CertManager.svelte";
  import AuditLog from "./admin/AuditLog.svelte";
  import DatabaseManager from "./admin/DatabaseManager.svelte";
  import BackupManager from "./admin/BackupManager.svelte";
  import RebalancePanel from "./admin/RebalancePanel.svelte";
  import CredentialManager from "./admin/CredentialManager.svelte";
  import Settings from "./admin/Settings.svelte";
  import DaemonPanel from "../components/admin/DaemonPanel.svelte";
  import TenantManager from "./admin/TenantManager.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let sealStatus: SealStatus | null = $state(null);
  let transportInfo: string = $state("");

  /**
   * Single source of truth for connection status.
   * Reads from the connection manager's per-profile state so the header and
   * Settings view always agree, regardless of how the connection was made.
   */
  const connected = $derived(
    connManagerStore.states.get(connManagerStore.activeProfileId)?.status === 'connected',
  );

  /**
   * Whether the Daemon panel is available for the active profile. Only
   * external profiles have a separate qrdbd OS process to start/stop;
   * embedded ("Local Server") profiles run in-process.
   */
  const daemonAvailable = $derived(
    isDesktop() &&
      connManagerStore.profiles.find(
        (p) => p.id === connManagerStore.activeProfileId,
      )?.type === 'external',
  );
  let showAddServerModal: boolean = $state(false);

  /**
   * Per-server database lists keyed by profile ID.
   * The active server's databases are in databaseStore; others live here so
   * expanding a second server shows its databases without clobbering the
   * active server's list.
   */
  let perServerDatabases: Map<string, readonly DatabaseInfo[]> = $state(new Map());

  // ---------------------------------------------------------------------------
  // Derived: breadcrumb
  // ---------------------------------------------------------------------------

  const breadcrumb = $derived.by((): { server: string; page: string } => {
    const ctx = navStore.context;
    const serverName = (): string => {
      const profile = connManagerStore.profiles.find(
        (p) => p.id === connManagerStore.activeProfileId,
      );
      return profile?.name ?? 'QRDB';
    };

    switch (ctx.type) {
      case 'none':
        return { server: 'QRDB', page: '' };
      case 'server-admin':
        return { server: serverName(), page: ADMIN_VIEW_LABELS[ctx.view] };
      case 'database': {
        const db = databaseStore.databases.find((d) => d.name === ctx.dbName);
        const toolLabel = DB_TOOL_LABELS[ctx.tool];
        return { server: serverName(), page: db ? `${db.name} / ${toolLabel}` : ctx.dbName };
      }
      case 'create-database':
        return { server: serverName(), page: 'New Database' };
    }
  });

  // ---------------------------------------------------------------------------
  // Current database for the workspace
  // ---------------------------------------------------------------------------

  const currentDatabase = $derived.by((): DatabaseInfo | null => {
    const ctx = navStore.context;
    if (ctx.type !== 'database') return null;
    return databaseStore.databases.find((d) => d.name === ctx.dbName) ?? null;
  });

  // ---------------------------------------------------------------------------
  // Connection lifecycle handlers (called by Sidebar)
  // ---------------------------------------------------------------------------

  async function handleConnect(profileId: string): Promise<void> {
    try {
      const state = await connectProfile(client, profileId);
      if (state.status === 'connected') {
        await setActiveProfile(client, profileId);
        await loadDatabases();
        await loadMetadata();
        expandServer(profileId);
        navigateToAdmin(profileId, 'dashboard');
      } else if (state.status === 'sealed') {
        showToast('warning', 'Server is sealed. Use Seal Manager to unseal.');
      } else if (state.status === 'needs-init') {
        showToast('warning', 'Server needs initialization. Use the CLI: qrdb barrier init');
      } else if (state.status === 'error') {
        showToast('error', state.error ?? 'Connection failed');
      }
    } catch (err: unknown) {
      showToast('error', err instanceof Error ? err.message : 'Connection failed');
    }
  }

  async function handleDisconnect(profileId: string): Promise<void> {
    try {
      await disconnectProfile(client, profileId);
      collapseServer(profileId);
      // If this was the active server, clear nav to none.
      if (profileId === connManagerStore.activeProfileId) {
        navigateToNone();
      }
    } catch (err: unknown) {
      showToast('error', err instanceof Error ? err.message : 'Disconnect failed');
    }
  }

  async function handleActivate(profileId: string): Promise<void> {
    await setActiveProfile(client, profileId);
    await loadDatabases();
    await loadMetadata();
  }

  // ---------------------------------------------------------------------------
  // Add-server modal
  // ---------------------------------------------------------------------------

  async function handleAddServerComplete(state: ConnectionState): Promise<void> {
    showAddServerModal = false;
    await loadProfiles(client);
    if (state.status === 'connected') {
      await loadDatabases();
      await loadMetadata();
      expandServer(state.profileId);
      navigateToAdmin(state.profileId, 'dashboard');
    }
  }

  // ---------------------------------------------------------------------------
  // Create-database wizard lifecycle
  // ---------------------------------------------------------------------------

  /**
   * Initialises the wizard store whenever the nav context transitions to
   * create-database. DatabaseList calls navigateToCreateDatabase directly
   * (pure nav), and this effect handles the wizard setup side-effect.
   */
  $effect(() => {
    const ctx = navStore.context;
    if (ctx.type !== 'create-database') return;
    const profileId = ctx.profileId;
    resetDatabaseWizard();
    const profile = connManagerStore.profiles.find((p) => p.id === profileId);
    if (profile?.type === 'external' || !isDesktop()) {
      dbWizardStore.mode = 'remote';
      dbWizardStore.raftConfig = null;
    } else {
      dbWizardStore.mode = 'embedded';
    }
    dbWizardStore.serverMode = isMultiNodeServer() ? 'raft' : 'standalone';
  });

  function handleWizardComplete(): void {
    const createdName = dbWizardStore.name;
    void loadDatabases().then(() => {
      const profileId = connManagerStore.activeProfileId;
      if (createdName) {
        const created = databaseStore.databases.find((db) => db.name === createdName);
        if (created) {
          navigateToDatabase(profileId, created.name);
          return;
        }
      }
      cancelCreateDatabase();
    });
  }

  function handleWizardCancel(): void {
    cancelCreateDatabase();
  }

  // ---------------------------------------------------------------------------
  // Data loading
  // ---------------------------------------------------------------------------

  async function loadDatabases(): Promise<void> {
    setLoading(true);
    try {
      const list = await withRetry(() => client.database.list());
      const databases = list as DatabaseInfo[];
      setDatabases(databases);
      if (connManagerStore.activeProfileId !== '') {
        perServerDatabases = new Map(perServerDatabases).set(
          connManagerStore.activeProfileId,
          databases,
        );
      }
    } catch (err: unknown) {
      setDatabases([]);
      if (connected) {
        showToast('error', err instanceof Error ? err.message : 'Failed to load databases');
      }
    } finally {
      setLoading(false);
    }
  }

  async function loadMetadata(): Promise<void> {
    try {
      const seal = await withRetry(() => client.seal.status());
      sealStatus = seal;
      // Publish to the shared seal store so the footer badge stays consistent
      // with the Seal Manager, which polls and mutates the same store after
      // seal/unseal actions. Without this the badge would reflect only the
      // mount-time snapshot and go stale after a seal/unseal.
      updateSealStatus(seal);
      const version = client.connection.getVersion();
      transportInfo = Object.entries(version)
        .map(([k, v]) => `${k}: ${v}`)
        .join(' | ');
    } catch {
      transportInfo = 'disconnected';
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadMetadata();
    void loadDatabases();

    // Profiles were loaded by App.svelte before rendering MainApp. React to
    // the already-populated store to set the initial navigation state.
    // Both desktop and web modes share the same profile-driven flow.
    const activeId = connManagerStore.activeProfileId;
    if (activeId !== '') {
      expandServer(activeId);
      navigateToAdmin(activeId, 'dashboard');
    } else if (connManagerStore.profiles.length === 0) {
      // First launch with no profiles: auto-open the Add Server modal.
      showAddServerModal = true;
    }
  });
</script>

<div class="flex h-screen flex-col overflow-hidden">

  <!-- ======================================================================
       HEADER
       ====================================================================== -->
  <header class="bg-base-200/80 border-base-300/50 z-20 border-b backdrop-blur-sm">
    <div class="flex h-12 items-center px-4">

      <!-- Left: Brand + sidebar toggle -->
      <div class="flex items-center gap-3">
        <button
          class="btn btn-ghost btn-square btn-sm"
          onclick={toggleSidebar}
          aria-label="Toggle sidebar"
        >
          <Menu class="h-5 w-5" />
        </button>
        <img src={logo} alt="QRDB" class="h-7" />
        <StatusBadge
          label={connected ? 'Connected' : 'Disconnected'}
          variant={connected ? 'success' : 'error'}
          pulse={!connected}
        />
      </div>

      <!-- Center: Breadcrumb -->
      <div class="flex flex-1 items-center justify-center">
        <div class="breadcrumbs text-sm">
          <ul>
            {#if breadcrumb.server}
              <li class="text-base-content/50">{breadcrumb.server}</li>
            {/if}
            {#if breadcrumb.page}
              <li class="font-medium">{breadcrumb.page}</li>
            {/if}
          </ul>
        </div>
      </div>

      <!-- Right: Theme toggle -->
      <div class="flex items-center gap-1">
        <button
          class="btn btn-ghost btn-square btn-sm"
          onclick={() => toggleTheme()}
          aria-label="Toggle theme"
        >
          {#if themeStore.theme === 'dark'}
            <Sun class="h-5 w-5" />
          {:else}
            <Moon class="h-5 w-5" />
          {/if}
        </button>
      </div>

    </div>
  </header>

  <!-- ======================================================================
       MAIN: sidebar + content
       ====================================================================== -->
  <div class="flex flex-1 overflow-hidden">

    <!-- Primary sidebar -->
    <Sidebar
      {perServerDatabases}
      onconnect={handleConnect}
      ondisconnect={handleDisconnect}
      onactivate={handleActivate}
      onaddserver={() => (showAddServerModal = true)}
    />

    <!-- Content area: overflow-y-auto allows long views (e.g. wizard) to scroll. -->
    <main class="content-bg flex-1 overflow-y-auto">

      {#if connManagerStore.profiles.length === 0 && !connManagerStore.loading}
        <!-- ── First-run: no servers configured ─────────────────────── -->
        <div class="flex h-full items-center justify-center">
          <EmptyState
            icon={Server}
            title="Welcome to QRDB"
            description="Add a server to get started. Run an embedded database locally or connect to an external qrdbd instance."
            actionLabel="Add Server"
            onaction={() => (showAddServerModal = true)}
          />
        </div>

      {:else if navStore.context.type === 'create-database'}
        <!-- ── New database wizard ───────────────────────────────────── -->
        <div class="mx-auto max-w-3xl p-6">
          <DatabaseWizard
            {client}
            onComplete={handleWizardComplete}
            onCancel={handleWizardCancel}
          />
        </div>

      {:else if navStore.context.type === 'database'}
        <!-- ── Database workspace (secondary rail + tool view) ───────── -->
        {#if currentDatabase !== null}
          <DatabaseWorkspace {client} database={currentDatabase} />
        {:else}
          <!-- Database no longer exists or list not yet loaded -->
          <div class="flex h-full items-center justify-center">
            <EmptyState
              icon={Database}
              title="Database Not Found"
              description="The selected database could not be found. It may have been deleted."
            />
          </div>
        {/if}

      {:else if navStore.context.type === 'server-admin'}
        <!-- ── Server admin views ────────────────────────────────────── -->
        <div class="w-full p-6">
          {#if navStore.context.view === 'dashboard'}
            <Dashboard {client} />
          {:else if navStore.context.view === 'cluster'}
            <Cluster {client} />
          {:else if navStore.context.view === 'topology'}
            <Topology {client} />
          {:else if navStore.context.view === 'seal-manager'}
            <SealManager {client} />
          {:else if navStore.context.view === 'tenants'}
            <TenantManager {client} />
          {:else if navStore.context.view === 'users'}
            <UserManager {client} />
          {:else if navStore.context.view === 'roles'}
            <RoleManager {client} />
          {:else if navStore.context.view === 'volume-pools'}
            <VolumePoolManager {client} />
          {:else if navStore.context.view === 'certificates'}
            <CertManager {client} />
          {:else if navStore.context.view === 'databases'}
            <DatabaseManager {client} />
          {:else if navStore.context.view === 'backups'}
            <BackupManager {client} />
          {:else if navStore.context.view === 'rebalance'}
            <RebalancePanel {client} />
          {:else if navStore.context.view === 'credentials'}
            <CredentialManager {client} />
          {:else if navStore.context.view === 'audit-log'}
            <AuditLog {client} />
          {:else if navStore.context.view === 'daemon' && daemonAvailable}
            <DaemonPanel {client} />
          {:else if navStore.context.view === 'settings'}
            <Settings {client} />
          {/if}
        </div>

      {:else}
        <!-- ── No context (none): select or add a server ────────────── -->
        <div class="flex h-full items-center justify-center">
          <EmptyState
            icon={Server}
            title="No Server Selected"
            description="Select a connected server from the sidebar, or add a new one."
            actionLabel="Add Server"
            onaction={() => (showAddServerModal = true)}
          />
        </div>
      {/if}

    </main>
  </div>

  <!-- ======================================================================
       STATUS BAR
       ====================================================================== -->
  <footer class="bg-base-300/80 border-base-300/50 flex items-center justify-between border-t px-4 py-2 text-xs backdrop-blur-sm">
    <span class="text-base-content/50">
      {transportInfo || 'loading...'}
    </span>
    <div class="flex items-center gap-3">
      {#if sealStatus !== null}
        {#if sealStore.strategy === 'passphrase' || sealStore.strategy === 'shamir'}
          <StatusBadge
            label={sealStore.sealed ? 'Sealed' : 'Unsealed'}
            variant={sealStore.sealed ? 'warning' : 'success'}
          />
        {:else}
          <StatusBadge label="Not encrypted" variant="warning" />
        {/if}
      {/if}
    </div>
  </footer>

</div>

<!-- ======================================================================
     ADD SERVER MODAL
     ====================================================================== -->
{#if showAddServerModal}
  <div class="modal modal-open z-50">
    <div class="modal-box max-w-3xl max-h-[90vh] overflow-y-auto">
      <button
        class="btn btn-sm btn-circle btn-ghost absolute right-2 top-2"
        onclick={() => (showAddServerModal = false)}
        aria-label="Close"
      >
        ✕
      </button>
      <ConnectionManager {client} onConnect={handleAddServerComplete} />
    </div>
    <button
      type="button"
      class="modal-backdrop"
      aria-label="Close modal"
      onclick={() => (showAddServerModal = false)}
    ></button>
  </div>
{/if}
