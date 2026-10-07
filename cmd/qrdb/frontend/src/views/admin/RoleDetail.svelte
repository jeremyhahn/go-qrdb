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
  import type { Client, Role, DatabaseInfo } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { ChevronLeft } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import PermissionTreePicker from "../../components/admin/PermissionTreePicker.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let {
    client,
    roleId,
    onBack,
  }: {
    client: Client;
    roleId: string | null;
    onBack: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let name: string = $state("");
  let tenantId: string = $state("default");
  let selectedPermissions: string[] = $state([]);
  let databases: DatabaseInfo[] = $state([]);
  let selectedDatabaseScope: string[] = $state([]);
  let existingRole: Role | null = $state(null);
  let loading: boolean = $state(true);
  let saving: boolean = $state(false);

  /** Whether we are creating a new role (vs editing an existing one). */
  let isCreate = $derived(roleId === null);

  /** Page title derived from the operation mode. */
  let pageTitle = $derived(isCreate ? "Create Role" : "Edit Role");

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Loads the existing role if editing. */
  async function loadData(): Promise<void> {
    try {
      loading = true;

      const dbs = await client.database.list();
      databases = dbs;

      if (roleId !== null) {
        const role: Role = await client.roles.get(roleId);
        existingRole = role;
        name = role.name;
        // Never allow tenantId to become undefined — TenantSelect's $bindable
        // has a fallback value which throws if bound to undefined.
        tenantId = role.tenantId ?? "default";
        selectedPermissions = [...role.permissions];

        // Extract database scope from permissions
        const dbScope = new Set<string>();
        for (const perm of role.permissions) {
          const [kind, name] = perm.split(":");
          if (kind === "database" && name !== undefined) {
            dbScope.add(name);
          }
        }
        selectedDatabaseScope = dbScope.size > 0 ? [...dbScope] : [];
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to load role data";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Creates or updates the role based on the current form state. */
  async function handleSave(): Promise<void> {
    if (name.trim() === "") {
      showToast("warning", "Role name is required");
      return;
    }

    try {
      saving = true;

      if (isCreate) {
        await client.roles.create({
          name: name.trim(),
          tenantId: tenantId.trim(),
          permissions: selectedPermissions,
        });
        showToast("success", `Role "${name}" created`);
      } else {
        await client.roles.update({
          id: roleId!,
          name: name.trim(),
          permissions: selectedPermissions,
        });
        showToast("success", `Role "${name}" updated`);
      }

      onBack();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save role";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  /** Handles permission changes from the PermissionTreePicker. */
  function handlePermissionChange(permissions: string[]): void {
    selectedPermissions = permissions;
  }

  /** Toggles a database name in the selected database scope list. */
  function toggleDatabaseScope(dbName: string): void {
    if (selectedDatabaseScope.includes(dbName)) {
      selectedDatabaseScope = selectedDatabaseScope.filter((n) => n !== dbName);
    } else {
      selectedDatabaseScope = [...selectedDatabaseScope, dbName];
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
    {#if existingRole !== null && existingRole.system}
      <span class="badge badge-info">System Role</span>
    {/if}
  </div>

  {#if loading}
    <div class="flex justify-center py-12">
      <span class="loading loading-spinner loading-lg text-primary"></span>
    </div>
  {:else}
    <!-- Role form -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Details</h3>

        <div class="form-control gap-5">
          <!-- Name -->
          <label class="form-control w-full max-w-md">
            <div class="label"><span class="label-text">Name</span></div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              placeholder="Enter role name"
              bind:value={name}
              disabled={existingRole !== null && existingRole.system}
            />
          </label>

          <!-- Tenant ID -->
          <label class="form-control w-full max-w-md" for="role-detail-tenant">
            <div class="label"><span class="label-text">Tenant ID</span></div>
            <TenantSelect
              id="role-detail-tenant"
              {client}
              bind:value={tenantId}
              allowEmpty={false}
              allowCreate={true}
              size="sm"
            />
          </label>
        </div>

        <!-- Metadata (existing roles only) -->
        {#if existingRole !== null}
          <div class="divider text-xs">Metadata</div>
          <div class="grid grid-cols-2 gap-2 text-xs">
            <div>
              <span class="text-base-content/60 font-semibold">Created</span>
              <p class="font-mono">{existingRole.createdAt}</p>
            </div>
            <div>
              <span class="text-base-content/60 font-semibold">Updated</span>
              <p class="font-mono">{existingRole.updatedAt}</p>
            </div>
          </div>
        {/if}
      </div>
    </div>

    <!-- Permission tree picker -->
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Permissions</h3>
        <p class="text-base-content/70 text-sm">
          Select the permissions to assign to this role.
        </p>
        <PermissionTreePicker
          {client}
          {selectedPermissions}
          onchange={handlePermissionChange}
        />
      </div>
    </div>

    <!-- Database scope -->
    {#if databases.length > 0}
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Database Scope</h3>
          <p class="text-base-content/70 text-sm">
            Select which databases these permissions apply to. Leave empty for all databases.
          </p>
          <div class="flex flex-wrap gap-2 pt-2">
            {#each databases as db (db.name)}
              <label class="cursor-pointer">
                <input
                  type="checkbox"
                  class="hidden"
                  checked={selectedDatabaseScope.includes(db.name)}
                  onchange={() => toggleDatabaseScope(db.name)}
                />
                <span
                  class="badge badge-lg transition-colors"
                  class:badge-primary={selectedDatabaseScope.includes(db.name)}
                  class:badge-outline={!selectedDatabaseScope.includes(db.name)}
                >
                  {db.name}
                </span>
              </label>
            {/each}
          </div>
          {#if selectedDatabaseScope.length === 0}
            <p class="text-base-content/50 mt-1 text-xs italic">
              No scope selected — permissions apply to all databases.
            </p>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Action buttons -->
    <div class="flex gap-2 pt-2">
      <button
        class="btn btn-primary btn-sm"
        onclick={() => void handleSave()}
        disabled={saving || name.trim() === ""}
      >
        {#if saving}
          <span class="loading loading-spinner loading-xs"></span>
        {/if}
        {isCreate ? "Create" : "Save"}
      </button>
      <button class="btn btn-ghost btn-sm" onclick={onBack}>
        Cancel
      </button>
    </div>
  {/if}
</div>
