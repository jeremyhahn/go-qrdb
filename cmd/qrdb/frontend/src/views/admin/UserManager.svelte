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
  import type { Client, User, ListUsersResponse } from "@qrdb/sdk";
  import { TransportError } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "$stores/toast.svelte.ts";
  import { identityStore } from "$stores/identity.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import UserDetail from "./UserDetail.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";
  import InviteUserModal from "../../components/admin/InviteUserModal.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  /** Tenant filter — empty string means all tenants. */
  let filterTenantId: string = $state(identityStore.tenantId || "");

  let users: User[] = $state([]);
  let loading: boolean = $state(true);
  let showDetail: boolean = $state(false);
  let detailUserId: string | null = $state(null);
  let showInvite: boolean = $state(false);
  let searchThumbprint: string = $state("");
  let searching: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const columns: Column<User>[] = [
    {
      id: "id",
      header: "ID",
      accessor: "id",
      sortable: true,
      width: "25%",
    },
    {
      id: "username",
      header: "Username",
      accessor: "username",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "tenantId",
      header: "Tenant",
      accessor: "tenantId",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "lifecycle",
      header: "Lifecycle",
      accessor: "lifecycle",
      sortable: true,
    },
    {
      id: "type",
      header: "Type",
      accessor: (row: User) => (row.system ? "System" : "User"),
      sortable: true,
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the user list, optionally filtered by tenant. */
  async function fetchUsers(): Promise<void> {
    try {
      loading = true;
      const response: ListUsersResponse = await client.users.list({
        ...(filterTenantId ? { tenantId: filterTenantId } : {}),
      });
      users = [...(response.users ?? [])];
    } catch (err: unknown) {
      if (
        err instanceof TransportError &&
        (err.statusCode === 404 ||
          err.statusCode === 501 ||
          err.statusCode === 503)
      ) {
        users = [];
      } else {
        const msg =
          err instanceof Error ? err.message : "Failed to fetch users";
        showToast("error", msg);
      }
    } finally {
      loading = false;
    }
  }

  /** Re-fetches users when the tenant filter changes. */
  function handleTenantFilterChange(): void {
    void fetchUsers();
  }

  /** Opens the detail panel for creating a new user. */
  function handleCreate(): void {
    detailUserId = null;
    showDetail = true;
  }

  /** Opens the detail panel for an existing user. */
  function handleSelectRow(event: RowClickEvent<User>): void {
    detailUserId = event.row.id;
    showDetail = true;
  }

  /** Closes the detail panel and refreshes the list. */
  function handleDetailBack(): void {
    showDetail = false;
    detailUserId = null;
    void fetchUsers();
  }

  /** Looks up the user bound to a certificate thumbprint and opens it. */
  async function handleThumbprintSearch(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const thumbprint = searchThumbprint.trim();
    if (thumbprint === "") return;
    try {
      searching = true;
      const user = await client.users.getUserByCertThumbprint(thumbprint);
      searchThumbprint = "";
      detailUserId = user.id;
      showDetail = true;
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "No user bound to that thumbprint";
      showToast("error", msg);
    } finally {
      searching = false;
    }
  }

  /** Opens the invite-a-user modal (see InviteUserModal.svelte). */
  function handleInvite(): void {
    showInvite = true;
  }

  /** Closes the invite modal and refreshes the list -- a successful invite
   * creates a new pending account. */
  function handleInviteClose(): void {
    showInvite = false;
    void fetchUsers();
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchUsers();
  });
</script>

{#if showDetail}
  <UserDetail {client} userId={detailUserId} onBack={handleDetailBack} />
{:else}
  <PageHeader
    title="Users"
    description="Manage user accounts and role assignments"
  />
  <div class="flex flex-col gap-6">
    <!-- Tenant filter -->
    <div class="flex items-center justify-between gap-3">
      <label class="form-control w-64" for="user-filter-tenant">
        <div class="label">
          <span class="label-text font-semibold">Tenant</span>
        </div>
        <TenantSelect
          id="user-filter-tenant"
          {client}
          bind:value={filterTenantId}
          allowEmpty={true}
          size="sm"
          onchange={handleTenantFilterChange}
        />
      </label>
      <button
        type="button"
        class="btn btn-sm btn-outline"
        onclick={handleInvite}
      >
        Invite User
      </button>
    </div>

    <!-- Certificate thumbprint lookup -->
    <form
      class="flex items-end gap-2"
      data-testid="user-thumbprint-search"
      onsubmit={(e) => void handleThumbprintSearch(e)}
    >
      <label class="form-control grow">
        <div class="label">
          <span class="label-text font-semibold"
            >Find user by certificate thumbprint</span
          >
        </div>
        <input
          type="text"
          class="input input-bordered input-sm w-full font-mono"
          placeholder="Certificate thumbprint"
          data-testid="user-thumbprint-input"
          bind:value={searchThumbprint}
        />
      </label>
      <button
        type="submit"
        class="btn btn-sm btn-outline"
        data-testid="user-thumbprint-submit"
        disabled={searching || searchThumbprint.trim() === ""}
      >
        Find
      </button>
    </form>

    <CrudTable
      title="Users"
      data={users}
      {columns}
      {loading}
      pageSize={10}
      showCreate={true}
      showDelete={false}
      createLabel="Create User"
      emptyMessage="No users found."
      oncreateclick={handleCreate}
      onrowclick={handleSelectRow}
    />
  </div>
  <InviteUserModal {client} open={showInvite} onClose={handleInviteClose} />
{/if}
