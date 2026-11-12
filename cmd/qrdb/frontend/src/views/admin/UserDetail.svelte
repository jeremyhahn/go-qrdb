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
    CertBinding,
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
  let email: string = $state("");
  let displayName: string = $state("");
  let tenantId: string = $state("default");
  let selectedRoleIds: string[] = $state([]);
  let existingUser: User | null = $state(null);
  let availableRoles: Role[] = $state([]);
  let databases: DatabaseInfo[] = $state([]);
  let loading: boolean = $state(true);
  let saving: boolean = $state(false);
  let busy: boolean = $state(false);
  let confirmDelete: boolean = $state(false);
  /** The user's certificate bindings as the server lists them. */
  let certBindings: readonly CertBinding[] = $derived.by(
    () => (existingUser as User | null)?.certBindings ?? [],
  );
  let bindingThumbprint: string = $state("");
  let bindingExpiry: string = $state("");

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
      const [kind, name] = perm.split(":");
      if (kind === "database" && name !== undefined) {
        dbNames.add(name);
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
        email = user.email ?? "";
        displayName = user.name ?? "";
        // Never allow tenantId to become undefined — TenantSelect's
        // $bindable has a fallback value which throws if bound to undefined.
        tenantId = user.tenantId ?? "default";
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

      const trimmedEmail = email.trim();
      const trimmedName = displayName.trim();
      if (isCreate) {
        await client.users.create({
          username: username.trim(),
          email: trimmedEmail,
          ...(trimmedName !== "" ? { name: trimmedName } : {}),
          tenantId: tenantId.trim(),
          roleIds: selectedRoleIds,
        });
        showToast("success", `User "${username}" created`);
      } else {
        await client.users.update({
          id: userId!,
          username: username.trim(),
          ...(trimmedEmail !== "" ? { email: trimmedEmail } : {}),
          ...(trimmedName !== "" ? { name: trimmedName } : {}),
          // No roleIds: an existing user's roles change only through
          // roles.assign / roles.unassign (toggleRole), so a Save racing an
          // in-flight toggle cannot resend the stale set and undo it.
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

  /** Set while any role assign / unassign request of this user is running. */
  let roleChangePending = $state(false);

  /**
   * Toggles a role ID in the selected roles set. For an existing user the
   * change is applied at once through roles.assign / roles.unassign. While
   * any role change is in flight every role toggle waits for it: all role
   * checkboxes are disabled and a toggle that still arrives is ignored and
   * its box put back. The server replaces the user's whole role set on each
   * change, so two concurrent changes (even to different roles) could undo
   * each other; one at a time keeps them ordered. selectedRoleIds stays the
   * authoritative state and changes only once the server accepts the
   * change. On a refusal the checkbox is put back to that state, undoing
   * the browser's own toggle.
   */
  async function toggleRole(roleId: string, box?: HTMLInputElement): Promise<void> {
    const revert = (): void => {
      if (box !== undefined) box.checked = selectedRoleIds.includes(roleId);
    };
    if (roleChangePending) {
      revert();
      return;
    }
    const assigned = selectedRoleIds.includes(roleId);
    if (!isCreate) {
      roleChangePending = true;
      try {
        if (assigned) {
          await client.roles.unassign(userId!, roleId);
          showToast("success", "Role unassigned");
        } else {
          await client.roles.assign(userId!, roleId);
          showToast("success", "Role assigned");
        }
      } catch (err: unknown) {
        roleChangePending = false;
        revert();
        showToast("error", err instanceof Error ? err.message : "Failed to change role");
        return;
      }
      roleChangePending = false;
    }
    if (assigned) {
      selectedRoleIds = selectedRoleIds.filter((id) => id !== roleId);
    } else {
      selectedRoleIds = [...selectedRoleIds, roleId];
    }
  }

  /** Enables or disables the existing user. */
  async function handleSetEnabled(enable: boolean): Promise<void> {
    try {
      busy = true;
      if (enable) {
        await client.users.enable(userId!);
      } else {
        await client.users.disable(userId!);
      }
      existingUser = await client.users.get(userId!);
      showToast("success", `User "${username}" ${enable ? "enabled" : "disabled"}`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to change user state";
      showToast("error", msg);
    } finally {
      busy = false;
    }
  }

  /** Deletes the existing user after a second confirming click. */
  async function handleDelete(): Promise<void> {
    if (!confirmDelete) {
      confirmDelete = true;
      return;
    }
    try {
      busy = true;
      await client.users.delete(userId!);
      showToast("success", `User "${username}" deleted`);
      onBack();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to delete user";
      showToast("error", msg);
    } finally {
      busy = false;
      confirmDelete = false;
    }
  }

  /** Binds a certificate thumbprint to the existing user. */
  async function handleAddBinding(): Promise<void> {
    const thumbprint = bindingThumbprint.trim();
    if (thumbprint === "") return;
    let notAfterUnix: number | undefined;
    if (bindingExpiry !== "") {
      notAfterUnix = Math.floor(new Date(bindingExpiry).getTime() / 1000);
    }
    try {
      busy = true;
      existingUser = await client.users.addCertBinding(userId!, thumbprint, notAfterUnix);
      bindingThumbprint = "";
      bindingExpiry = "";
      showToast("success", "Certificate binding added");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to add certificate binding";
      showToast("error", msg);
    } finally {
      busy = false;
    }
  }

  /** Removes a certificate thumbprint binding from the existing user. */
  async function handleRemoveBinding(thumbprint: string): Promise<void> {
    const tp = thumbprint.trim();
    if (tp === "") return;
    try {
      busy = true;
      existingUser = await client.users.removeCertBinding(userId!, tp);
      if (bindingThumbprint.trim() === tp) bindingThumbprint = "";
      showToast("success", "Certificate binding removed");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to remove certificate binding";
      showToast("error", msg);
    } finally {
      busy = false;
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

            <!-- Email -->
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Email</span></div>
              <input
                type="email"
                class="input input-bordered input-sm w-full"
                placeholder="user@example.com"
                data-testid="user-email"
                bind:value={email}
                disabled={existingUser !== null && existingUser.system}
              />
            </label>

            <!-- Display name -->
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Display Name</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="Optional display name"
                data-testid="user-name"
                bind:value={displayName}
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
          </div>

          <!-- Metadata (existing users only) -->
          {#if existingUser !== null}
            <div class="divider text-xs">Metadata</div>
            <div class="grid grid-cols-2 gap-2 text-xs">
              <div>
                <span class="text-base-content/60 font-semibold">Lifecycle</span>
                <p>
                  <span
                    class="badge badge-sm {existingUser.lifecycle === 'active'
                      ? 'badge-success'
                      : existingUser.lifecycle === 'disabled'
                        ? 'badge-error'
                        : 'badge-warning'}"
                    data-testid="user-lifecycle-badge">{existingUser.lifecycle}</span
                  >
                </p>
              </div>
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
                    disabled={roleChangePending}
                    data-testid="user-role-{role.name}"
                    onchange={(e) => void toggleRole(role.id, e.currentTarget)}
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
              <span class="badge badge-lg badge-primary gap-1">
                {db.name}
              </span>
            {/each}
            {#if accessibleDatabases.length === 0}
              <span class="text-base-content/50 text-sm">No database access.</span>
            {/if}
          </div>
        </div>
      </div>
    {/if}

    <!-- Certificate bindings (existing users only) -->
    {#if existingUser !== null}
      <div class="card bg-base-200 shadow" data-testid="user-cert-bindings">
        <div class="card-body">
          <h3 class="card-title text-base">Certificate Bindings</h3>
          <p class="text-base-content/70 text-sm">
            Bind a certificate's base64url SHA-256 SPKI thumbprint to this user.
          </p>
          <div class="flex flex-wrap items-end gap-2">
            <label class="form-control grow">
              <div class="label"><span class="label-text">Thumbprint</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full font-mono"
                placeholder="Certificate thumbprint"
                data-testid="user-binding-thumbprint"
                bind:value={bindingThumbprint}
              />
            </label>
            <label class="form-control">
              <div class="label"><span class="label-text">Expires (optional)</span></div>
              <input
                type="datetime-local"
                class="input input-bordered input-sm"
                data-testid="user-binding-expiry"
                bind:value={bindingExpiry}
              />
            </label>
            <button
              class="btn btn-primary btn-sm"
              data-testid="user-binding-add"
              disabled={busy || bindingThumbprint.trim() === ""}
              onclick={() => void handleAddBinding()}
            >
              Add Binding
            </button>
            <button
              class="btn btn-outline btn-sm"
              data-testid="user-binding-remove"
              disabled={busy || bindingThumbprint.trim() === ""}
              onclick={() => void handleRemoveBinding(bindingThumbprint)}
            >
              Remove Binding
            </button>
          </div>
          {#if certBindings.length === 0}
            <p class="text-base-content/50 pt-2 text-sm" data-testid="user-cert-bindings-empty">
              No certificate bindings.
            </p>
          {:else}
            <ul class="flex flex-col gap-1 pt-2">
              {#each certBindings as binding (binding.thumbprint)}
                <li class="flex items-center gap-2" data-testid="user-binding-row">
                  <span
                    class="font-mono text-xs break-all"
                    data-testid="user-binding-thumbprint-value">{binding.thumbprint}</span
                  >
                  <span class="badge badge-ghost badge-xs">{binding.kind}</span>
                  <span class="text-base-content/60 text-xs">
                    {binding.notAfter !== undefined ? `expires ${binding.notAfter}` : "no expiry"}
                  </span>
                  <button
                    class="btn btn-ghost btn-xs ml-auto"
                    aria-label="Remove binding {binding.thumbprint}"
                    disabled={busy}
                    onclick={() => void handleRemoveBinding(binding.thumbprint)}
                  >
                    Remove
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
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
      {#if existingUser !== null && !existingUser.system}
        {#if existingUser.lifecycle === "disabled"}
          <button
            class="btn btn-outline btn-success btn-sm"
            data-testid="user-enable"
            disabled={busy}
            onclick={() => void handleSetEnabled(true)}
          >
            Enable
          </button>
        {:else}
          <button
            class="btn btn-outline btn-warning btn-sm"
            data-testid="user-disable"
            disabled={busy}
            onclick={() => void handleSetEnabled(false)}
          >
            Disable
          </button>
        {/if}
        <button
          class="btn btn-error btn-sm ml-auto"
          data-testid="user-delete"
          disabled={busy}
          onclick={() => void handleDelete()}
        >
          {confirmDelete ? "Confirm Delete" : "Delete"}
        </button>
      {/if}
    </div>
  {/if}
</div>
