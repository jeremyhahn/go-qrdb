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
  import type {
    Client,
    User,
    Role,
    ListRolesResponse,
    DatabaseInfo,
  } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { ChevronLeft } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let {
    client,
    userId,
    onBack,
  }: {
    client: Client;
    userId: string | null;
    onBack: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let username: string = $state("");
  let tenantId: string = $state("default");
  let enabled: boolean = $state(true);
  let selectedRoleIds: string[] = $state([]);
  let existingUser: User | null = $state(null);
  let availableRoles: Role[] = $state([]);
  let databases: DatabaseInfo[] = $state([]);
  let loading: boolean = $state(true);
  let saving: boolean = $state(false);

  /** Whether we are creating a new user (vs editing an existing one). */
  let isCreate = $derived(userId === null);

  /** Page title derived from the operation mode. */
  let pageTitle = $derived(isCreate ? "Create User" : "Edit User");

  /** Databases the user has access to based on their selected roles. */
  let accessibleDatabases = $derived.by(() => {
    if (databases.length === 0) return [];
    const userPerms = availableRoles
      .filter((r) => selectedRoleIds.includes(r.id))
      .flatMap((r) => r.permissions);
    const hasWildcard = userPerms.some(
      (p) => p === "*" || p === "database:*" || p.startsWith("admin:"),
    );
    if (hasWildcard) return databases;
    const dbNames = new Set<string>();
    for (const perm of userPerms) {
      const parts = perm.split(":");
      if (parts[0] === "database" && parts.length >= 2) {
        dbNames.add(parts[1]);
      }
    }
    if (dbNames.size === 0) return databases;
    return databases.filter((db) => dbNames.has(db.name));
  });

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Loads the existing user (if editing) and the available roles list. */
  async function loadData(): Promise<void> {
    try {
      loading = true;

      // Always fetch available roles for the role selector.
      const rolesResponse: ListRolesResponse = await client.roles.list({});
      availableRoles = [...rolesResponse.roles];

      // Fetch available databases for the access summary.
      const dbs = await client.database.list();
      databases = dbs;

      // If editing, fetch the existing user record.
      if (userId !== null) {
        const user: User = await client.users.get(userId);
        existingUser = user;
        username = user.username;
        // Never allow tenantId to become undefined — TenantSelect's
        // $bindable has a fallback value which throws if bound to undefined.
        tenantId = user.tenantId ?? "default";
        enabled = user.enabled;
        selectedRoleIds = [...user.roleIds];
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to load user data";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Creates or updates the user based on the current form state. */
  async function handleSave(): Promise<void> {
    if (username.trim() === "") {
      showToast("warning", "Username is required");
      return;
    }

    try {
      saving = true;

      if (isCreate) {
        await client.users.create({
          username: username.trim(),
          tenantId: tenantId.trim(),
          roleIds: selectedRoleIds,
        });
        showToast("success", `User "${username}" created`);
      } else {
        await client.users.update({
          id: userId!,
          username: username.trim(),
          roleIds: selectedRoleIds,
        });
        showToast("success", `User "${username}" updated`);
      }

      onBack();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save user";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  /** Toggles a role ID in the selected roles set. */
  function toggleRole(roleId: string): void {
    if (selectedRoleIds.includes(roleId)) {
      selectedRoleIds = selectedRoleIds.filter((id) => id !== roleId);
    } else {
      selectedRoleIds = [...selectedRoleIds, roleId];
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
    {#if existingUser !== null && existingUser.system}
      <span class="badge badge-info">System User</span>
    {/if}
  </div>

  {#if loading}
    <div class="flex justify-center py-12">
      <span class="loading loading-spinner loading-lg text-primary"></span>
    </div>
  {:else}
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <!-- User form -->
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Details</h3>

          <div class="form-control gap-5">
            <!-- Username -->
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Username</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="Enter username"
                bind:value={username}
                disabled={existingUser !== null && existingUser.system}
              />
            </label>

            <!-- Tenant ID -->
            <label class="form-control w-full" for="user-detail-tenant">
              <div class="label"><span class="label-text">Tenant ID</span></div>
              <TenantSelect
                id="user-detail-tenant"
                {client}
                bind:value={tenantId}
                allowEmpty={false}
                allowCreate={true}
                size="sm"
              />
            </label>

            <!-- Enabled toggle -->
            <div class="form-control">
              <label class="label cursor-pointer justify-start gap-3">
                <input
                  type="checkbox"
                  class="toggle toggle-success toggle-sm"
                  bind:checked={enabled}
                  disabled={existingUser !== null && existingUser.system}
                />
                <span class="label-text">Enabled</span>
              </label>
            </div>
          </div>

          <!-- Metadata (existing users only) -->
          {#if existingUser !== null}
            <div class="divider text-xs">Metadata</div>
            <div class="grid grid-cols-2 gap-2 text-xs">
              <div>
                <span class="text-base-content/60 font-semibold">Created</span>
                <p class="font-mono">{existingUser.createdAt}</p>
              </div>
              <div>
                <span class="text-base-content/60 font-semibold">Updated</span>
                <p class="font-mono">{existingUser.updatedAt}</p>
              </div>
            </div>
          {/if}
        </div>
      </div>

      <!-- Role selection -->
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Roles</h3>
          {#if availableRoles.length === 0}
            <p class="text-base-content/50 text-sm">No roles available.</p>
          {:else}
            <div class="flex flex-col gap-1 max-h-80 overflow-y-auto">
              {#each availableRoles as role (role.id)}
                <label class="label cursor-pointer justify-start gap-3 rounded px-2 hover:bg-base-300">
                  <input
                    type="checkbox"
                    class="checkbox checkbox-sm checkbox-primary"
                    checked={selectedRoleIds.includes(role.id)}
                    onchange={() => toggleRole(role.id)}
                  />
                  <div class="flex flex-col">
                    <span class="text-sm">{role.name}</span>
                    <span class="text-base-content/50 text-xs">
                      {role.tenantId} &middot; {role.permissions.length} permission{role.permissions.length !== 1 ? "s" : ""}
                    </span>
                  </div>
                  {#if role.system}
                    <span class="badge badge-xs badge-info ml-auto">System</span>
                  {/if}
                </label>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    </div>

    <!-- Database access summary -->
    {#if databases.length > 0}
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Database Access</h3>
          <p class="text-base-content/70 text-sm">
            Databases accessible through assigned roles.
          </p>
          <div class="flex flex-wrap gap-2 pt-2">
            {#each accessibleDatabases as db (db.name)}
              <span
                class="badge badge-lg gap-1"
                class:badge-primary={db.engine === "kv"}
                class:badge-secondary={db.engine === "timeseries"}
                class:badge-accent={db.engine === "vector"}
              >
                {db.name}
                <span class="text-xs opacity-70">({db.engine})</span>
              </span>
            {/each}
            {#if accessibleDatabases.length === 0}
              <span class="text-base-content/50 text-sm">No database access.</span>
            {/if}
          </div>
        </div>
      </div>
    {/if}

    <!-- Action buttons -->
    <div class="flex gap-2 pt-2">
      <button
        class="btn btn-primary btn-sm"
        onclick={() => void handleSave()}
        disabled={saving || username.trim() === ""}
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
