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
  import type { Client, Role, ListRolesResponse } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "$stores/toast.svelte.ts";
  import { identityStore } from "$stores/identity.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import RoleDetail from "./RoleDetail.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  /** Tenant filter — empty string means all tenants. */
  let filterTenantId: string = $state(identityStore.tenantId || "");

  let roles: Role[] = $state([]);
  let loading: boolean = $state(true);
  let showDetail: boolean = $state(false);
  let detailRoleId: string | null = $state(null);

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const columns: Column<Role>[] = [
    {
      id: "id",
      header: "ID",
      accessor: "id",
      sortable: true,
      width: "25%",
    },
    {
      id: "name",
      header: "Name",
      accessor: "name",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "tenantId",
      header: "Tenant",
      accessor: "tenantId",
      sortable: true,
    },
    {
      id: "permissions",
      header: "Permissions",
      accessor: (row: Role) => String(row.permissions.length),
      sortable: true,
      align: "center",
    },
    {
      id: "type",
      header: "Type",
      accessor: (row: Role) => (row.system ? "System" : "Custom"),
      sortable: true,
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the role list, optionally filtered by tenant. */
  async function fetchRoles(): Promise<void> {
    try {
      loading = true;
      const response: ListRolesResponse = await client.roles.list({
        ...(filterTenantId ? { tenantId: filterTenantId } : {}),
      });
      roles = [...(response.roles ?? [])];
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch roles";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  /** Re-fetches roles when the tenant filter changes. */
  function handleTenantFilterChange(): void {
    void fetchRoles();
  }

  /** Opens the detail panel for creating a new role. */
  function handleCreate(): void {
    detailRoleId = null;
    showDetail = true;
  }

  /** Opens the detail panel for editing an existing role. */
  function handleEdit(role: Role): void {
    detailRoleId = role.id;
    showDetail = true;
  }

  /** Closes the detail panel and refreshes the list. */
  function handleDetailBack(): void {
    showDetail = false;
    detailRoleId = null;
    void fetchRoles();
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchRoles();
  });
</script>

{#if showDetail}
  <RoleDetail {client} roleId={detailRoleId} onBack={handleDetailBack} />
{:else}
  <PageHeader title="Roles" description="Manage roles and permission sets" />
  <div class="flex flex-col gap-6">
    <!-- Tenant filter -->
    <div class="flex items-center gap-3">
      <label class="form-control w-64" for="role-filter-tenant">
        <div class="label">
          <span class="label-text font-semibold">Tenant</span>
        </div>
        <TenantSelect
          id="role-filter-tenant"
          {client}
          bind:value={filterTenantId}
          allowEmpty={true}
          size="sm"
          onchange={handleTenantFilterChange}
        />
      </label>
    </div>

    <CrudTable
      title="Roles"
      data={roles}
      {columns}
      {loading}
      pageSize={10}
      showCreate={true}
      showDelete={false}
      createLabel="Create Role"
      oncreateclick={handleCreate}
      onrowclick={(event: RowClickEvent<Role>) => handleEdit(event.row)}
      rowKey="id"
      emptyMessage="No roles found."
    />
  </div>
{/if}
