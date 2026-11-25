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
  import type { Client, Group, ListGroupsResponse } from "@qrdb/sdk";
  import type { Column, RowClickEvent } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import { showToast } from "$stores/toast.svelte.ts";
  import { identityStore } from "$stores/identity.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import GroupDetail from "./GroupDetail.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  /** Tenant whose groups are listed — empty string means the caller's tenant. */
  let filterTenantId: string = $state(identityStore.tenantId || "");

  let groups: Group[] = $state([]);
  let loading: boolean = $state(true);
  let showDetail: boolean = $state(false);
  let detailGroupId: string | null = $state(null);

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const columns: Column<Group>[] = [
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
      id: "description",
      header: "Description",
      accessor: "description",
    },
    {
      id: "roles",
      header: "Roles",
      accessor: (row: Group) => String(row.roleIds.length),
      sortable: true,
      align: "center",
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the tenant's groups. */
  async function fetchGroups(): Promise<void> {
    try {
      loading = true;
      const response: ListGroupsResponse = await client.groups.list({
        ...(filterTenantId ? { tenantId: filterTenantId } : {}),
      });
      groups = [...(response.groups ?? [])];
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch groups";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  /** Re-fetches groups when the tenant filter changes. */
  function handleTenantFilterChange(): void {
    void fetchGroups();
  }

  /** Opens the detail panel for creating a new group. */
  function handleCreate(): void {
    detailGroupId = null;
    showDetail = true;
  }

  /** Opens the detail panel for editing an existing group. */
  function handleEdit(group: Group): void {
    detailGroupId = group.id;
    showDetail = true;
  }

  /** Closes the detail panel and refreshes the list. */
  function handleDetailBack(): void {
    showDetail = false;
    detailGroupId = null;
    void fetchGroups();
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchGroups();
  });
</script>

{#if showDetail}
  <GroupDetail
    {client}
    groupId={detailGroupId}
    defaultTenantId={filterTenantId}
    onBack={handleDetailBack}
  />
{:else}
  <PageHeader title="Groups" description="Manage IAM groups, their roles and members" />
  <div class="flex flex-col gap-6">
    <!-- Tenant filter -->
    <div class="flex items-center gap-3">
      <label class="form-control w-64" for="group-filter-tenant">
        <div class="label"><span class="label-text font-semibold">Tenant</span></div>
        <TenantSelect
          id="group-filter-tenant"
          {client}
          bind:value={filterTenantId}
          allowEmpty={true}
          size="sm"
          onchange={handleTenantFilterChange}
        />
      </label>
    </div>

    <CrudTable
      title="Groups"
      data={groups}
      {columns}
      {loading}
      pageSize={10}
      showCreate={true}
      showDelete={false}
      createLabel="Create Group"
      oncreateclick={handleCreate}
      onrowclick={(event: RowClickEvent<Group>) => handleEdit(event.row)}
      rowKey="id"
      emptyMessage="No groups found."
    />
  </div>
{/if}
