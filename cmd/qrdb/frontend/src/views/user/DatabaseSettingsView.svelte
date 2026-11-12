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
  DatabaseSettingsView displays read-only database metadata and allows the
  user to toggle SQL wire protocol compatibility for the current database.

  It receives the already-loaded DatabaseInfo as a prop so it does not need
  to perform its own fetch — the workspace parent owns the data lifecycle.
  SQL toggling calls client.database.updateProtocol() and reflects the result
  locally without a full page reload.
-->
<script lang="ts">
  import type { Client, DatabaseInfo, DatabaseProtocolConfig } from "@qrdb/sdk";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { updateDatabase } from "../../stores/database.svelte.ts";
  import StatusBadge from "../../components/StatusBadge.svelte";
  import DatabaseSettingsForms from "../../components/database/DatabaseSettingsForms.svelte";
  import type { SettingsSection } from "../../components/database/DatabaseSettingsForms.svelte";

  /** Settings forms the user role may change on its own database. */
  const USER_SECTIONS: readonly SettingsSection[] = ["sql", "compaction"];

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    client,
    database: initialDatabase,
  }: {
    client: Client;
    database: DatabaseInfo;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  /** Local copy of database info — updated when SQL toggles succeed or when
   *  the parent passes a new database prop (e.g. user switches databases). */
  let database: DatabaseInfo = $state(initialDatabase);

  $effect(() => {
    database = initialDatabase;
  });

  /** Whether a SQL protocol toggle request is in flight. */
  let sqlUpdating: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  /** Maps a database status string to a StatusBadge variant. */
  function statusVariant(
    status: string,
  ): "success" | "warning" | "error" | "info" {
    const map: Record<string, "success" | "warning" | "error" | "info"> = {
      open: "success",
      closed: "warning",
      error: "error",
    };
    return map[status] ?? "info";
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Toggles a SQL wire protocol on the current database. */
  async function handleSQLToggle(
    protocol: "mysql" | "postgresql",
    enabled: boolean,
  ): Promise<void> {
    const updated: DatabaseProtocolConfig = {
      mysql:
        protocol === "mysql" ? enabled : (database.protocol?.mysql ?? false),
      postgresql:
        protocol === "postgresql"
          ? enabled
          : (database.protocol?.postgresql ?? false),
    };

    try {
      sqlUpdating = true;
      database = await client.database.updateProtocol(database.name, updated);
      // Propagate the fresh snapshot into the shared store: the workspace tab
      // bar derives the SQL Browser tab from the store's database entry, so
      // without this the tab only appears after a full list reload.
      updateDatabase(database);
      showToast(
        "success",
        `${protocol === "mysql" ? "MySQL" : "PostgreSQL"} ${enabled ? "enabled" : "disabled"}`,
      );
    } catch (err: unknown) {
      showToast(
        "error",
        err instanceof Error
          ? err.message
          : "Failed to update SQL configuration",
      );
    } finally {
      sqlUpdating = false;
    }
  }
</script>

<div class="flex flex-col gap-6">
  <!-- Database Info (read-only) -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h3 class="card-title text-base">Database Info</h3>
      <div class="grid grid-cols-2 gap-x-6 gap-y-4 pt-2 text-sm md:grid-cols-3">
        <div>
          <span class="text-xs font-semibold text-base-content/60">Name</span>
          <p class="font-mono">{database.name}</p>
        </div>
        <div>
          <span class="text-xs font-semibold text-base-content/60">Backend</span
          >
          <p>{database.backend}</p>
        </div>
        <div>
          <span class="text-xs font-semibold text-base-content/60">Status</span>
          <p>
            <StatusBadge
              label={database.status}
              variant={statusVariant(database.status)}
              pulse={database.status === "open"}
            />
          </p>
        </div>
        {#if database.dataDir}
          <div class="col-span-2">
            <span class="text-xs font-semibold text-base-content/60"
              >Data Directory</span
            >
            <p class="break-all font-mono text-xs">{database.dataDir}</p>
          </div>
        {/if}
        {#if database.shardId !== undefined}
          <div>
            <span class="text-xs font-semibold text-base-content/60"
              >Shard ID</span
            >
            <p class="font-mono">{database.shardId}</p>
          </div>
        {/if}
      </div>
    </div>
  </div>

  <!-- SQL Wire Protocols -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h3 class="card-title text-base">SQL Wire Protocols</h3>
      <p class="text-sm text-base-content/70">
        Enable or disable SQL wire protocol compatibility for this database.
        Changes take effect immediately without a restart.
      </p>
      <div class="flex flex-col gap-3 pt-2">
        <label class="flex cursor-pointer items-center gap-3">
          <input
            type="checkbox"
            class="toggle toggle-sm toggle-primary"
            checked={database.protocol?.mysql ?? false}
            disabled={sqlUpdating}
            onchange={(e: Event) => {
              void handleSQLToggle(
                "mysql",
                (e.target as HTMLInputElement).checked,
              );
            }}
          />
          <span class="text-sm font-medium">MySQL</span>
          <span class="text-xs text-base-content/50"
            >MySQL wire protocol (port 3306)</span
          >
          {#if sqlUpdating}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
        </label>
        <label class="flex cursor-pointer items-center gap-3">
          <input
            type="checkbox"
            class="toggle toggle-sm toggle-primary"
            checked={database.protocol?.postgresql ?? false}
            disabled={sqlUpdating}
            onchange={(e: Event) => {
              void handleSQLToggle(
                "postgresql",
                (e.target as HTMLInputElement).checked,
              );
            }}
          />
          <span class="text-sm font-medium">PostgreSQL</span>
          <span class="text-xs text-base-content/50"
            >PostgreSQL wire protocol (port 5432)</span
          >
          {#if sqlUpdating}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
        </label>
      </div>
    </div>
  </div>

  <!-- SQL Protocol Status (read-only summary) -->
  <div class="card bg-base-200 shadow">
    <div class="card-body">
      <h3 class="card-title text-base">Protocol Status</h3>
      <div class="flex flex-wrap items-center gap-3 pt-1">
        <span
          class="badge badge-sm {database.protocol?.mysql
            ? 'badge-success'
            : 'badge-ghost'}"
        >
          MySQL {database.protocol?.mysql ? "enabled" : "disabled"}
        </span>
        <span
          class="badge badge-sm {database.protocol?.postgresql
            ? 'badge-success'
            : 'badge-ghost'}"
        >
          PostgreSQL {database.protocol?.postgresql ? "enabled" : "disabled"}
        </span>
      </div>
    </div>
  </div>

  <!-- Database-scoped tuning: the user role may change SQL concurrency and
       scheduled compaction; Raft, barrier and connection stay admin-only. -->
  <DatabaseSettingsForms
    {client}
    {database}
    sections={USER_SECTIONS}
    onUpdated={(db) => {
      database = db;
      updateDatabase(db);
    }}
  />
</div>
