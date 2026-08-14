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
  DatabaseWorkspace renders the database workspace with a horizontal tab bar
  at the top and the active tool view below it. Tab visibility is filtered by
  the database engine and by the presence of SQL protocol configuration.

  Navigation is driven through navStore.selectDbTool.
-->
<script lang="ts">
  import type { Client, DatabaseInfo } from "@qrdb/sdk";
  import { navStore, selectDbTool } from "../../stores/navigation.svelte.ts";
  import type { DbTool } from "../../stores/navigation.svelte.ts";

  // Tool view components
  import KVExplorer from "./KVExplorer.svelte";
  import TransactionView from "./TransactionView.svelte";
  import WatchView from "./WatchView.svelte";
  import BatchView from "./BatchView.svelte";
  import TimeSeriesView from "./TimeSeriesView.svelte";
  import VectorView from "./VectorView.svelte";
  import SQLBrowserScoped from "./SQLBrowserScoped.svelte";
  import CollectionView from "./CollectionView.svelte";
  import LeaseManager from "./LeaseManager.svelte";
  import LockManager from "./LockManager.svelte";
  import IndexManager from "./IndexManager.svelte";
  import DatabaseSettingsView from "./DatabaseSettingsView.svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let { client, database }: { client: Client; database: DatabaseInfo } = $props();

  // Data-engine views operate on a client scoped to this database, so every
  // KV/MVCC/transaction/batch/watch/lease/index/time-series/vector request
  // carries the X-QRDB-Database header and lands in {tenant}/{database}/.
  // Collection, SQL Browser, and Settings embed the database themselves (path-
  // based or explicit prop), so they keep the unscoped client to avoid double
  // scoping; admin/system views elsewhere also keep the unscoped client.
  const dbClient = $derived(client.databaseScoped(database.name));

  // ---------------------------------------------------------------------------
  // Tab definitions
  // ---------------------------------------------------------------------------

  interface TabDef {
    readonly id: DbTool;
    readonly label: string;
  }

  /** Tabs for KV databases. */
  const KV_TABS: readonly TabDef[] = [
    { id: 'data',         label: 'Data' },
    { id: 'collections',  label: 'Collections' },
    { id: 'indexes',      label: 'Indexes' },
    { id: 'transactions', label: 'Transactions' },
    { id: 'batch',        label: 'Batch' },
    { id: 'watch',        label: 'Watch' },
    { id: 'leases',       label: 'Leases' },
    { id: 'locks',        label: 'Locks' },
  ];

  /** Tabs for TimeSeries and Vector databases. */
  const ENGINE_TABS: readonly TabDef[] = [
    { id: 'data',  label: 'Data' },
    { id: 'batch', label: 'Batch' },
  ];

  /** SQL Browser tab — injected when SQL protocols are enabled. */
  const SQL_TAB: TabDef = { id: 'sql', label: 'SQL Browser' };

  /** Settings tab — always last. */
  const SETTINGS_TAB: TabDef = { id: 'db-settings', label: 'Settings' };

  // ---------------------------------------------------------------------------
  // Derived: active tool and visible tabs
  // ---------------------------------------------------------------------------

  const activeTool = $derived.by((): DbTool => {
    const ctx = navStore.context;
    if (ctx.type !== 'database') return 'data';
    return (ctx as { tool: DbTool }).tool;
  });

  const hasSql = $derived(
    (database.protocol?.mysql ?? false) || (database.protocol?.postgresql ?? false),
  );

  const visibleTabs = $derived.by((): readonly TabDef[] => {
    const base: TabDef[] = database.engine === 'kv'
      ? [...KV_TABS]
      : [...ENGINE_TABS];
    if (hasSql) base.push(SQL_TAB);
    base.push(SETTINGS_TAB);
    return base;
  });
</script>

<div class="flex h-full flex-col overflow-hidden">

  <!-- Horizontal tab bar. The nav wraps the tablist for landmark semantics;
       the inner div carries role="tablist" to satisfy ARIA constraints. -->
  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <nav
    class="flex shrink-0 items-end gap-0 overflow-x-auto border-b border-base-content/8 bg-base-200/50 px-4"
    role="tablist"
    aria-label="Database tools"
  >
    {#each visibleTabs as tab (tab.id)}
      {@const isActive = activeTool === tab.id}
      <button
        class="-mb-px shrink-0 border-b-2 px-4 py-2.5 text-sm font-medium transition-colors duration-150
          {isActive
            ? 'border-primary text-primary'
            : 'border-transparent text-base-content/55 hover:text-base-content/80 hover:border-base-content/20'}"
        role="tab"
        aria-selected={isActive}
        aria-controls="tabpanel-{tab.id}"
        onclick={() => selectDbTool(tab.id)}
      >
        {tab.label}
      </button>
    {/each}
  </nav>

  <!-- Tool view -->
  <div class="flex-1 overflow-y-auto p-6" id="tabpanel-{activeTool}" role="tabpanel">
    {#if database.engine === 'kv'}

      {#if activeTool === 'data'}
        <KVExplorer client={dbClient} />
      {:else if activeTool === 'collections'}
        <CollectionView {client} {database} />
      {:else if activeTool === 'indexes'}
        <IndexManager client={dbClient} />
      {:else if activeTool === 'transactions'}
        <TransactionView client={dbClient} />
      {:else if activeTool === 'batch'}
        <BatchView client={dbClient} />
      {:else if activeTool === 'watch'}
        <WatchView client={dbClient} />
      {:else if activeTool === 'leases'}
        <LeaseManager client={dbClient} />
      {:else if activeTool === 'locks'}
        <LockManager client={dbClient} />
      {:else if activeTool === 'sql'}
        <SQLBrowserScoped {client} {database} />
      {:else if activeTool === 'db-settings'}
        <DatabaseSettingsView {client} {database} />
      {/if}

    {:else if database.engine === 'timeseries'}

      {#if activeTool === 'data'}
        <TimeSeriesView client={dbClient} />
      {:else if activeTool === 'batch'}
        <BatchView client={dbClient} />
      {:else if activeTool === 'sql'}
        <SQLBrowserScoped {client} {database} />
      {:else if activeTool === 'db-settings'}
        <DatabaseSettingsView {client} {database} />
      {/if}

    {:else if database.engine === 'vector'}

      {#if activeTool === 'data'}
        <VectorView client={dbClient} />
      {:else if activeTool === 'batch'}
        <BatchView client={dbClient} />
      {:else if activeTool === 'sql'}
        <SQLBrowserScoped {client} {database} />
      {:else if activeTool === 'db-settings'}
        <DatabaseSettingsView {client} {database} />
      {/if}

    {/if}
  </div>

</div>
