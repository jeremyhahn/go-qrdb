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
  import type { Client, DatabaseInfo, CreateDatabaseRequest, DatabaseProtocolConfig } from "@qrdb/sdk";
  import { ChevronLeft, Server, HardDrive, MemoryStick, Network } from "lucide-svelte";
  import { onMount } from "svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import StatusBadge from "../../components/StatusBadge.svelte";
  import DatabaseHoldersPanel from "../../components/DatabaseHoldersPanel.svelte";

  let {
    client,
    databaseName,
    onBack,
  }: {
    client: Client;
    databaseName: string | null;
    onBack: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Constants
  // ---------------------------------------------------------------------------

  const NAME_PATTERN = /^[a-zA-Z0-9_-]+$/;

  interface BackendOption {
    readonly value: string;
    readonly label: string;
    readonly description: string;
    readonly recommended: boolean;
  }

  const BACKEND_OPTIONS: readonly BackendOption[] = [
    {
      value: "memory",
      label: "Memory",
      description:
        "In-memory storage. Fast but non-persistent. Best for testing and development.",
      recommended: false,
    },
    {
      value: "file",
      label: "File",
      description:
        "Flat-file persistence. Simple local storage for small datasets.",
      recommended: false,
    },
    {
      value: "pebble",
      label: "Pebble",
      description:
        "PebbleDB LSM-tree storage. Production-grade persistent storage with high throughput.",
      recommended: true,
    },
  ] as const;

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let loading: boolean = $state(true);
  let saving: boolean = $state(false);
  let existingDb: DatabaseInfo | null = $state(null);
  let confirmDrop: boolean = $state(false);
  let dropLoading: boolean = $state(false);

  // Create form state
  let name: string = $state("");
  let backend: string = $state("pebble");
  let sqlMySQL: boolean = $state(false);
  let sqlPostgreSQL: boolean = $state(false);

  // View mode SQL toggle state
  let sqlUpdating: boolean = $state(false);

  // Replica scaling state
  let targetReplicas: number = $state(1);
  let scaling: boolean = $state(false);

  /** Whether we are creating a new database (vs viewing an existing one). */
  let isCreate = $derived(databaseName === null);

  /** Page title derived from the operation mode. */
  let pageTitle = $derived(isCreate ? "Create Database" : databaseName);

  /** Name validation: non-empty and matches the allowed pattern. */
  let nameError = $derived.by(() => {
    if (name.length === 0) return "";
    if (!NAME_PATTERN.test(name)) {
      return "Only letters, numbers, hyphens, and underscores are allowed.";
    }
    return "";
  });

  /** Whether the create form can be submitted. */
  let canCreate = $derived(name.trim().length > 0 && nameError === "");

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
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Loads the existing database info when in view mode. */
  async function loadData(): Promise<void> {
    if (isCreate) {
      loading = false;
      return;
    }

    try {
      loading = true;
      existingDb = await client.database.get(databaseName!);
      if (existingDb.replicas) {
        targetReplicas = existingDb.replicas;
      }
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to load database";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Creates a new database using the form state. */
  async function handleCreate(): Promise<void> {
    if (!canCreate) return;

    try {
      saving = true;
      const req: CreateDatabaseRequest = {
        name: name.trim(),
        backend,
        ...((sqlMySQL || sqlPostgreSQL)
          ? { protocol: { mysql: sqlMySQL, postgresql: sqlPostgreSQL } }
          : {}),
      };
      await client.database.create(req);
      showToast("success", `Database "${name.trim()}" created`);
      onBack();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to create database";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  /** Drops the current database after confirmation. */
  async function handleDrop(): Promise<void> {
    if (databaseName === null) return;

    try {
      dropLoading = true;
      await client.database.drop(databaseName);
      showToast("success", `Database "${databaseName}" dropped`);
      onBack();
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to drop database";
      showToast("error", msg);
    } finally {
      dropLoading = false;
    }
  }

  /** Toggles a SQL wire protocol on an existing database. */
  async function handleSQLToggle(protocol: "mysql" | "postgresql", enabled: boolean): Promise<void> {
    if (existingDb === null) return;

    const updated: DatabaseProtocolConfig = {
      mysql: protocol === "mysql" ? enabled : (existingDb.protocol?.mysql ?? false),
      postgresql: protocol === "postgresql" ? enabled : (existingDb.protocol?.postgresql ?? false),
    };

    try {
      sqlUpdating = true;
      existingDb = await client.database.updateProtocol(existingDb.name, updated);
      showToast("success", `${protocol === "mysql" ? "MySQL" : "PostgreSQL"} ${enabled ? "enabled" : "disabled"}`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to update SQL config";
      showToast("error", msg);
    } finally {
      sqlUpdating = false;
    }
  }

  /** Changes the replication factor of a dedicated-raft database. */
  async function handleScale(): Promise<void> {
    if (existingDb === null || targetReplicas < 1) return;

    try {
      scaling = true;
      existingDb = await client.database.scale(existingDb.name, targetReplicas);
      showToast("success", `Database "${existingDb.name}" scaled to ${targetReplicas} replicas`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to scale database";
      showToast("error", msg);
    } finally {
      scaling = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadData();
  });
</script>

<div class="flex flex-col gap-6">
  <!-- Header with back button -->
  <div class="flex items-center gap-3">
    <button class="btn btn-ghost btn-sm" onclick={onBack}>
      <ChevronLeft class="h-4 w-4" />
      Back
    </button>
    <h2 class="text-lg font-semibold">{pageTitle}</h2>
  </div>

  {#if loading}
    <div class="flex justify-center py-12">
      <span class="loading loading-spinner loading-lg text-primary"></span>
    </div>
  {:else if isCreate}
    <!-- ===================================================================
         CREATE MODE
         =================================================================== -->
    <form
      class="flex flex-col gap-6"
      onsubmit={(e: SubmitEvent) => {
        e.preventDefault();
        void handleCreate();
      }}
    >
      <!-- Name input -->
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Database Name</h3>
          <label class="form-control w-full max-w-md">
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              class:input-error={nameError !== ""}
              placeholder="e.g. my-database"
              bind:value={name}
            />
            {#if nameError !== ""}
              <div class="label">
                <span class="label-text-alt text-error">{nameError}</span>
              </div>
            {/if}
          </label>
        </div>
      </div>

      <!-- Backend selection cards -->
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Backend</h3>
          <p class="text-base-content/70 text-sm">
            Choose the storage backend for this database.
          </p>
          <div class="grid grid-cols-1 gap-3 pt-2 sm:grid-cols-2 lg:grid-cols-4">
            {#each BACKEND_OPTIONS as opt (opt.value)}
              <button
                type="button"
                class="card cursor-pointer border-2 bg-base-100 p-4 text-left transition-all hover:shadow-md"
                class:border-primary={backend === opt.value}
                class:border-transparent={backend !== opt.value}
                onclick={() => {
                  backend = opt.value;
                }}
              >
                <div class="flex items-center gap-2">
                  {#if opt.value === "memory"}
                    <MemoryStick class="h-5 w-5 text-info" />
                  {:else if opt.value === "file"}
                    <HardDrive class="h-5 w-5 text-base-content/70" />
                  {:else if opt.value === "pebble"}
                    <Server class="h-5 w-5 text-success" />
                  {:else}
                    <Network class="h-5 w-5 text-primary" />
                  {/if}
                  <span class="font-semibold">{opt.label}</span>
                  {#if opt.recommended}
                    <span class="badge badge-xs badge-success">Recommended</span>
                  {/if}
                </div>
                <p class="text-base-content/70 mt-2 text-xs leading-relaxed">
                  {opt.description}
                </p>
              </button>
            {/each}
          </div>
        </div>
      </div>

      <!-- SQL Wire Protocols -->
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">SQL Wire Protocols</h3>
          <p class="text-base-content/70 text-sm">
            Enable SQL wire protocol compatibility for this database.
          </p>
          <div class="flex flex-col gap-3 pt-2">
            <label class="flex items-center gap-3 cursor-pointer">
              <input
                type="checkbox"
                class="checkbox checkbox-sm checkbox-primary"
                bind:checked={sqlMySQL}
              />
              <span class="text-sm font-medium">MySQL</span>
              <span class="text-base-content/50 text-xs">MySQL wire protocol (port 3306)</span>
            </label>
            <label class="flex items-center gap-3 cursor-pointer">
              <input
                type="checkbox"
                class="checkbox checkbox-sm checkbox-primary"
                bind:checked={sqlPostgreSQL}
              />
              <span class="text-sm font-medium">PostgreSQL</span>
              <span class="text-base-content/50 text-xs">PostgreSQL wire protocol (port 5432)</span>
            </label>
          </div>
        </div>
      </div>

      <!-- Action buttons -->
      <div class="flex gap-2 pt-2">
        <button
          type="submit"
          class="btn btn-primary btn-sm"
          disabled={saving || !canCreate}
        >
          {#if saving}
            <span class="loading loading-spinner loading-xs"></span>
          {/if}
          Create Database
        </button>
        <button type="button" class="btn btn-ghost btn-sm" onclick={onBack}>
          Cancel
        </button>
      </div>
    </form>
  {:else if existingDb !== null}
    <!-- ===================================================================
         VIEW MODE
         =================================================================== -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Database Info</h3>
        <div class="grid grid-cols-2 gap-x-6 gap-y-4 pt-2 text-sm md:grid-cols-3">
          <div>
            <span class="text-base-content/60 text-xs font-semibold">Name</span>
            <p class="font-mono">{existingDb.name}</p>
          </div>
          <div>
            <span class="text-base-content/60 text-xs font-semibold">Backend</span>
            <p>{existingDb.backend}</p>
          </div>
          <div>
            <span class="text-base-content/60 text-xs font-semibold">Status</span>
            <p>
              <StatusBadge
                label={existingDb.status}
                variant={statusVariant(existingDb.status)}
                pulse={existingDb.status === "open"}
              />
            </p>
          </div>
          {#if existingDb.dataDir}
            <div>
              <span class="text-base-content/60 text-xs font-semibold">Data Directory</span>
              <p class="font-mono text-xs break-all">{existingDb.dataDir}</p>
            </div>
          {/if}
          {#if existingDb.shardId !== undefined}
            <div>
              <span class="text-base-content/60 text-xs font-semibold">Shard ID</span>
              <p class="font-mono">{existingDb.shardId}</p>
            </div>
          {/if}
          <div>
            <span class="text-base-content/60 text-xs font-semibold">SQL Protocols</span>
            <div class="flex items-center gap-2 mt-1">
              <span class="badge badge-sm {existingDb.protocol?.mysql ? 'badge-success' : 'badge-ghost'}">
                MySQL {existingDb.protocol?.mysql ? "on" : "off"}
              </span>
              <span class="badge badge-sm {existingDb.protocol?.postgresql ? 'badge-success' : 'badge-ghost'}">
                PostgreSQL {existingDb.protocol?.postgresql ? "on" : "off"}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>

    {#if existingDb.barrier?.enabled && existingDb.barrier.mode === "shamir"}
      <DatabaseHoldersPanel
        {client}
        database={existingDb.name}
        sharesTotal={existingDb.barrier.sharesTotal ?? 0}
        sharesRequired={existingDb.barrier.sharesRequired ?? 0}
      />
    {/if}

    <!-- SQL Wire Protocol Toggles -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">SQL Wire Protocols</h3>
        <p class="text-base-content/70 text-sm">
          Enable or disable SQL wire protocol compatibility.
        </p>
        <div class="flex flex-col gap-3 pt-2">
          <label class="flex items-center gap-3 cursor-pointer">
            <input
              type="checkbox"
              class="toggle toggle-sm toggle-primary"
              checked={existingDb.protocol?.mysql ?? false}
              disabled={sqlUpdating}
              onchange={(e: Event) => {
                void handleSQLToggle("mysql", (e.target as HTMLInputElement).checked);
              }}
            />
            <span class="text-sm font-medium">MySQL</span>
            {#if sqlUpdating}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
          </label>
          <label class="flex items-center gap-3 cursor-pointer">
            <input
              type="checkbox"
              class="toggle toggle-sm toggle-primary"
              checked={existingDb.protocol?.postgresql ?? false}
              disabled={sqlUpdating}
              onchange={(e: Event) => {
                void handleSQLToggle("postgresql", (e.target as HTMLInputElement).checked);
              }}
            />
            <span class="text-sm font-medium">PostgreSQL</span>
            {#if sqlUpdating}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
          </label>
        </div>
      </div>
    </div>

    <!-- Replica scaling section (dedicated-raft databases only) -->
    {#if existingDb.replicas}
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Replication</h3>
          <p class="text-base-content/70 text-sm">
            Change the replication factor of this dedicated-raft database. Current
            replication factor: <span class="font-mono">{existingDb.replicas}</span>.
          </p>
          <div class="flex items-end gap-3 pt-2">
            <label class="form-control w-full max-w-[10rem]">
              <div class="label">
                <span class="label-text text-xs">Target Replicas</span>
              </div>
              <input
                id="db-target-replicas"
                type="number"
                min="1"
                class="input input-bordered input-sm w-full"
                bind:value={targetReplicas}
                disabled={scaling}
              />
            </label>
            <button
              class="btn btn-primary btn-sm"
              disabled={scaling || targetReplicas < 1}
              onclick={() => void handleScale()}
            >
              {#if scaling}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Scale Replicas
            </button>
          </div>
        </div>
      </div>
    {/if}

    <!-- Drop section -->
    <div class="card border border-error/20 bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base text-error">Danger Zone</h3>
        {#if confirmDrop}
          <div class="flex items-center justify-between gap-4">
            <span class="text-sm">
              Drop database <strong class="font-mono">"{existingDb.name}"</strong>? This action cannot be undone.
            </span>
            <div class="flex gap-2">
              <button
                class="btn btn-error btn-sm"
                disabled={dropLoading}
                onclick={() => void handleDrop()}
              >
                {#if dropLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                Confirm Drop
              </button>
              <button
                class="btn btn-ghost btn-sm"
                onclick={() => {
                  confirmDrop = false;
                }}
              >
                Cancel
              </button>
            </div>
          </div>
        {:else}
          <button
            class="btn btn-error btn-sm w-fit"
            title="Permanently drop this database and all its data. This action cannot be undone."
            onclick={() => {
              confirmDrop = true;
            }}
          >
            Drop Database
          </button>
        {/if}
      </div>
    </div>
  {/if}
</div>
