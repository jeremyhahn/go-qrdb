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
  import type { Client, Group, GroupMember, Role } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { ChevronLeft } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let {
    client,
    groupId,
    defaultTenantId = "",
    onBack,
  }: {
    client: Client;
    groupId: string | null;
    defaultTenantId?: string;
    onBack: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let name: string = $state("");
  let description: string = $state("");
  let tenantId: string = $state("default");
  let selectedRoleIds: string[] = $state([]);
  let roles: Role[] = $state([]);
  let members: GroupMember[] = $state([]);
  let newMemberId: string = $state("");
  let existingGroup: Group | null = $state(null);
  let loading: boolean = $state(true);
  let saving: boolean = $state(false);
  let deleting: boolean = $state(false);
  let confirmDelete: boolean = $state(false);

  /** Whether we are creating a new group (vs editing an existing one). */
  let isCreate = $derived(groupId === null);

  /** Page title derived from the operation mode. */
  let pageTitle = $derived(isCreate ? "Create Group" : "Edit Group");

  // ---------------------------------------------------------------------------
  // Data fetching
  // ---------------------------------------------------------------------------

  /** Loads the tenant's roles, then the group and its members when editing. */
  async function loadData(): Promise<void> {
    try {
      loading = true;
      if (groupId !== null) {
        const group: Group = await client.groups.get(groupId);
        existingGroup = group;
        name = group.name;
        description = group.description;
        // TenantSelect's $bindable throws if bound to undefined.
        tenantId = group.tenantId || "default";
        selectedRoleIds = [...group.roleIds];
        members = [...(await client.groups.members(groupId))];
      } else if (defaultTenantId) {
        tenantId = defaultTenantId;
      }
      await loadRoles(tenantId);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to load group data";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  /** Sequence number of the latest roles request; older replies are dropped. */
  let rolesRequest = 0;

  /**
   * Loads the roles of tenant tid. A reply is applied only while it answers
   * the latest request and tid is still the selected tenant, so a slow reply
   * for a tenant switched away from never replaces the current tenant's roles.
   */
  async function loadRoles(tid: string): Promise<void> {
    const request = ++rolesRequest;
    try {
      const page = await client.roles.list({ tenantId: tid });
      if (request === rolesRequest && tenantId === tid) {
        roles = [...(page.roles ?? [])];
      }
    } catch (err: unknown) {
      if (request === rolesRequest) {
        const msg = err instanceof Error ? err.message : "Failed to load roles";
        showToast("error", msg);
      }
    }
  }

  /**
   * A group carries only roles of its own tenant: switching the tenant drops
   * the selection and the previous tenant's roles, then loads the new ones.
   */
  function handleTenantChange(): void {
    selectedRoleIds = [];
    roles = [];
    void loadRoles(tenantId);
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Creates or updates the group from the current form state. */
  async function handleSave(): Promise<void> {
    if (name.trim() === "") {
      showToast("warning", "Group name is required");
      return;
    }
    try {
      saving = true;
      if (isCreate) {
        await client.groups.create({
          name: name.trim(),
          tenantId: tenantId.trim(),
          description: description.trim(),
          roleIds: selectedRoleIds,
        });
        showToast("success", `Group "${name}" created`);
      } else {
        await client.groups.update({
          id: groupId!,
          name: name.trim(),
          description: description.trim(),
          roleIds: selectedRoleIds,
        });
        showToast("success", `Group "${name}" updated`);
      }
      onBack();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save group";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  /** Deletes the group after a second confirming click. */
  async function handleDelete(): Promise<void> {
    if (!confirmDelete) {
      confirmDelete = true;
      return;
    }
    try {
      deleting = true;
      await client.groups.delete(groupId!);
      showToast("success", `Group "${name}" deleted`);
      onBack();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to delete group";
      showToast("error", msg);
    } finally {
      deleting = false;
      confirmDelete = false;
    }
  }

  /** Re-reads the member list of the group being edited. */
  async function refreshMembers(): Promise<void> {
    members = [...(await client.groups.members(groupId!))];
  }

  /** Adds the user whose decimal ID is in the member field. */
  async function handleAddMember(): Promise<void> {
    const userId = newMemberId.trim();
    if (userId === "") return;
    try {
      await client.groups.addMember(groupId!, userId);
      newMemberId = "";
      await refreshMembers();
      showToast("success", `User ${userId} added`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to add member";
      showToast("error", msg);
    }
  }

  /** Removes a member from the group. */
  async function handleRemoveMember(userId: string): Promise<void> {
    try {
      await client.groups.removeMember(groupId!, userId);
      await refreshMembers();
      showToast("success", `User ${userId} removed`);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to remove member";
      showToast("error", msg);
    }
  }

  /** Toggles a role ID in the group's role set. */
  function toggleRole(roleId: string): void {
    selectedRoleIds = selectedRoleIds.includes(roleId)
      ? selectedRoleIds.filter((id) => id !== roleId)
      : [...selectedRoleIds, roleId];
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void loadData();
  });
</script>

<div class="flex flex-col gap-6">
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
  {:else}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Details</h3>
        <div class="form-control gap-5">
          <label class="form-control w-full max-w-md">
            <div class="label"><span class="label-text">Name</span></div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              placeholder="Enter group name"
              data-testid="group-name"
              bind:value={name}
            />
          </label>
          <label class="form-control w-full max-w-md">
            <div class="label"><span class="label-text">Description</span></div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              data-testid="group-description"
              bind:value={description}
            />
          </label>
          <label class="form-control w-full max-w-md" for="group-detail-tenant">
            <div class="label"><span class="label-text">Tenant ID</span></div>
            <TenantSelect
              id="group-detail-tenant"
              {client}
              bind:value={tenantId}
              allowEmpty={false}
              size="sm"
              disabled={!isCreate}
              onchange={handleTenantChange}
            />
          </label>
        </div>
      </div>
    </div>

    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h3 class="card-title text-base">Roles</h3>
        <p class="text-base-content/70 text-sm">Every member inherits the selected roles.</p>
        <div class="flex flex-wrap gap-2 pt-2">
          {#each roles as role (role.id)}
            <label class="cursor-pointer">
              <input
                type="checkbox"
                class="hidden"
                checked={selectedRoleIds.includes(role.id)}
                onchange={() => toggleRole(role.id)}
              />
              <span
                class="badge badge-lg transition-colors"
                class:badge-primary={selectedRoleIds.includes(role.id)}
                class:badge-outline={!selectedRoleIds.includes(role.id)}
              >
                {role.name}
              </span>
            </label>
          {/each}
        </div>
      </div>
    </div>

    {#if existingGroup !== null}
      <div class="card bg-base-200 shadow">
        <div class="card-body">
          <h3 class="card-title text-base">Members</h3>
          <ul class="flex flex-col gap-1" data-testid="group-members">
            {#each members as member (member.userId)}
              <li class="flex items-center gap-2 text-sm">
                <span class="font-mono">{member.userId}</span>
                <span>{member.username}</span>
                <button
                  class="btn btn-ghost btn-xs ml-auto"
                  onclick={() => void handleRemoveMember(member.userId)}
                >
                  Remove
                </button>
              </li>
            {:else}
              <li class="text-base-content/50 text-xs italic">No members.</li>
            {/each}
          </ul>
          <div class="flex gap-2 pt-2">
            <input
              type="text"
              class="input input-bordered input-sm w-48"
              placeholder="User ID"
              data-testid="group-member-id"
              bind:value={newMemberId}
            />
            <button class="btn btn-sm" onclick={() => void handleAddMember()}>Add member</button>
          </div>
        </div>
      </div>
    {/if}

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
      <button class="btn btn-ghost btn-sm" onclick={onBack}>Cancel</button>
      {#if existingGroup !== null}
        <button
          class="btn btn-error btn-sm ml-auto"
          data-testid="group-delete"
          disabled={deleting}
          onclick={() => void handleDelete()}
        >
          {confirmDelete ? "Confirm Delete" : "Delete"}
        </button>
      {/if}
    </div>
  {/if}
</div>
