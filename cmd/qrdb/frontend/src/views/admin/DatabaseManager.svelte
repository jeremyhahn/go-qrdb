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
  import type { Client, DatabaseInfo } from "@qrdb/sdk";
  import type {
    Column,
    RowClickEvent,
  } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import PageHeader from "../../components/PageHeader.svelte";
  import DatabaseDetail from "./DatabaseDetail.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let databases: DatabaseInfo[] = $state([]);
  let loading: boolean = $state(true);
  let showDetail: boolean = $state(false);
  let detailDatabaseName: string | null = $state(null);

  // ---------------------------------------------------------------------------
  // Column Definitions
  // ---------------------------------------------------------------------------

  const columns: Column<DatabaseInfo>[] = [
    {
      id: "name",
      header: "Name",
      accessor: "name",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "backend",
      header: "Backend",
      accessor: "backend",
      sortable: true,
    },
    {
      id: "status",
      header: "Status",
      accessor: "status",
      sortable: true,
    },
  ];

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches all databases from the server. */
  async function fetchDatabases(): Promise<void> {
    try {
      loading = true;
      const list: DatabaseInfo[] = await client.database.list();
      databases = [...list];
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to fetch databases";
      showToast("error", msg);
    } finally {
      loading = false;
    }
  }

  /** Opens the detail view for creating a new database. */
  function handleCreate(): void {
    detailDatabaseName = null;
    showDetail = true;
  }

  /** Opens the detail view for an existing database. */
  function handleSelectRow(db: DatabaseInfo): void {
    detailDatabaseName = db.name;
    showDetail = true;
  }

  /** Handles row click events from CrudTable. */
  function handleRowClick(event: RowClickEvent<DatabaseInfo>): void {
    handleSelectRow(event.row);
  }

  /** Closes the detail view and refreshes the list. */
  function handleDetailBack(): void {
    showDetail = false;
    detailDatabaseName = null;
    void fetchDatabases();
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchDatabases();
  });
</script>

{#if showDetail}
  <DatabaseDetail
    {client}
    databaseName={detailDatabaseName}
    onBack={handleDetailBack}
  />
{:else}
  <PageHeader
    title="Databases"
    description="Create, manage, and monitor databases"
  />

  <CrudTable
    title="Databases"
    data={databases}
    {columns}
    {loading}
    pageSize={10}
    showCreate={true}
    showDelete={false}
    createLabel="Create Database"
    emptyMessage="No databases found."
    rowKey="name"
    oncreateclick={handleCreate}
    onrowclick={handleRowClick}
  />
{/if}
