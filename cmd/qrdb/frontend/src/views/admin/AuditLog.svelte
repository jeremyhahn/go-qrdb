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
  import type { Client, AuditEvent, AuditFilter } from "@qrdb/sdk";
  import { TransportError } from "@qrdb/sdk";
  import type { Column } from "../../components/core/DataTable/types.ts";
  import { onMount } from "svelte";
  import { RefreshCw, ChevronRight } from "lucide-svelte";
  import { CrudTable } from "../../components/core/CrudTable";
  import PageHeader from "../../components/PageHeader.svelte";
  import SlidePanel from "../../components/core/SlidePanel.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Columns
  // ---------------------------------------------------------------------------

  const columns: Column<AuditEvent>[] = [
    {
      id: "timestamp",
      header: "Timestamp",
      accessor: "timestamp",
      sortable: true,
    },
    {
      id: "tenantID",
      header: "Tenant",
      accessor: (row: AuditEvent) => row.tenantID ?? "",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "actor",
      header: "Actor",
      accessor: "actor",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "action",
      header: "Action",
      accessor: "action",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    {
      id: "resource",
      header: "Resource",
      accessor: "resource",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    { id: "outcome", header: "Outcome", accessor: "outcome", sortable: true },
  ];

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let events: AuditEvent[] = $state([]);
  let loading: boolean = $state(true);
  let total: number = $state(0);
  let nextCursor: string = $state("");
  /** Set when the audit API is not available on the active connection. */
  let unavailable: boolean = $state(false);

  /** Filter state — mirrors AuditFilter. */
  let filterTenant: string = $state("");
  let filterActor: string = $state("");
  let filterResource: string = $state("");
  let filterAction: string = $state("");
  let filterOutcome: string = $state("");
  let filterFrom: string = $state("");
  let filterTo: string = $state("");

  /** Detail panel state. */
  let showDetail: boolean = $state(false);
  let selectedEvent: AuditEvent | null = $state(null);

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Builds the AuditFilter from the current filter inputs. */
  function buildFilter(cursor: string = ""): AuditFilter {
    const f: AuditFilter = {};
    if (filterTenant.trim() !== "") f.tenantId = filterTenant.trim();
    if (filterActor.trim() !== "") f.actor = filterActor.trim();
    if (filterResource.trim() !== "") f.resource = filterResource.trim();
    if (filterAction.trim() !== "") f.action = filterAction.trim();
    if (filterOutcome.trim() !== "") f.outcome = filterOutcome.trim();
    if (filterFrom !== "") f.from = new Date(filterFrom).toISOString();
    if (filterTo !== "") f.to = new Date(filterTo).toISOString();
    if (cursor !== "") f.cursor = cursor;
    return f;
  }

  /** Queries the real compliance-grade audit API with the active filters. */
  async function fetchAuditEvents(): Promise<void> {
    loading = true;
    unavailable = false;
    try {
      const page = await client.audit.query(buildFilter());
      events = [...page.events];
      total = page.total;
      nextCursor = page.next ?? "";
    } catch (err: unknown) {
      events = [];
      total = 0;
      nextCursor = "";
      const message = err instanceof Error ? err.message : String(err);
      // A 404 (route missing on older servers) or 501 (no audit logger
      // configured) means the audit API is simply not available on this
      // connection; render the friendly "not available" panel instead of an
      // error toast. The desktop transport surfaces the same condition as
      // AuditClientUnavailableError, which carries no HTTP status code, so
      // fall back to a message substring check for that path.
      const statusUnavailable =
        err instanceof TransportError &&
        (err.statusCode === 404 || err.statusCode === 501);
      const messageUnavailable =
        message.toLowerCase().includes("not implemented") ||
        message.toLowerCase().includes("no audit client available");
      if (statusUnavailable || messageUnavailable) {
        unavailable = true;
      } else {
        showToast("error", message);
      }
    } finally {
      loading = false;
    }
  }

  /** Loads the next page of results, appending to the current list. */
  async function loadMore(): Promise<void> {
    if (nextCursor === "") return;
    loading = true;
    try {
      const page = await client.audit.query(buildFilter(nextCursor));
      events = [...events, ...page.events];
      total = page.total;
      nextCursor = page.next ?? "";
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  /** Clears all filter inputs and re-queries. */
  function clearFilters(): void {
    filterTenant = "";
    filterActor = "";
    filterResource = "";
    filterAction = "";
    filterOutcome = "";
    filterFrom = "";
    filterTo = "";
    void fetchAuditEvents();
  }

  function handleRowClick(event: { row: AuditEvent }): void {
    selectedEvent = event.row;
    showDetail = true;
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchAuditEvents();
  });
</script>

<PageHeader title="Audit Log" description="Query compliance-grade audit events">
  {#snippet actions()}
    <button
      class="btn btn-outline btn-sm"
      onclick={() => void fetchAuditEvents()}
      disabled={loading}
    >
      {#if loading}
        <span class="loading loading-spinner loading-xs"></span>
      {:else}
        <RefreshCw class="h-4 w-4" />
      {/if}
      Refresh
    </button>
  {/snippet}
</PageHeader>

<div class="flex flex-col gap-6">
  <!-- Filter row -->
  <div class="card bg-base-200 shadow">
    <div class="card-body p-5">
      <div class="flex flex-wrap items-end gap-3">
        <label
          class="form-control w-full max-w-[180px]"
          for="audit-filter-tenant"
        >
          <div class="label">
            <span class="label-text text-xs">Tenant</span>
          </div>
          <TenantSelect
            id="audit-filter-tenant"
            {client}
            bind:value={filterTenant}
            allowEmpty={true}
            size="sm"
          />
        </label>
        <label class="form-control w-full max-w-[160px]">
          <div class="label"><span class="label-text text-xs">Actor</span></div>
          <input
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="Filter by actor"
            bind:value={filterActor}
          />
        </label>
        <label class="form-control w-full max-w-[160px]">
          <div class="label">
            <span class="label-text text-xs">Resource</span>
          </div>
          <input
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="Filter by resource"
            bind:value={filterResource}
          />
        </label>
        <label class="form-control w-full max-w-[160px]">
          <div class="label">
            <span class="label-text text-xs">Action</span>
          </div>
          <input
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="Filter by action"
            bind:value={filterAction}
          />
        </label>
        <label class="form-control w-full max-w-[160px]">
          <div class="label">
            <span class="label-text text-xs">Outcome</span>
          </div>
          <select
            class="select select-bordered select-sm w-full"
            bind:value={filterOutcome}
          >
            <option value="">Any</option>
            <option value="success">Success</option>
            <option value="failure">Failure</option>
            <option value="denied">Denied</option>
          </select>
        </label>
        <label class="form-control w-full max-w-[180px]">
          <div class="label"><span class="label-text text-xs">From</span></div>
          <input
            type="datetime-local"
            class="input input-bordered input-sm w-full"
            bind:value={filterFrom}
          />
        </label>
        <label class="form-control w-full max-w-[180px]">
          <div class="label"><span class="label-text text-xs">To</span></div>
          <input
            type="datetime-local"
            class="input input-bordered input-sm w-full"
            bind:value={filterTo}
          />
        </label>
        <div class="flex gap-1">
          <button class="btn btn-ghost btn-sm" onclick={clearFilters}>
            Clear
          </button>
          <button
            class="btn btn-primary btn-sm"
            onclick={() => void fetchAuditEvents()}
            disabled={loading}
          >
            Apply
          </button>
        </div>
      </div>
    </div>
  </div>

  {#if unavailable}
    <div class="alert alert-warning">
      <span>
        The audit log API is not available on this connection. Configure an
        audit logger on the server and ensure the connected user has the <code
          >audit:query</code
        > permission.
      </span>
    </div>
  {:else}
    <!-- Audit events table -->
    <CrudTable
      title="Audit Events"
      {columns}
      data={events}
      {loading}
      showCreate={false}
      showDelete={false}
      onrowclick={handleRowClick}
      emptyMessage="No audit events found."
      pageSize={20}
      rowKey={(row: AuditEvent) =>
        `${row.timestamp}|${row.requestID ?? ""}|${row.action}|${row.resource}`}
    />

    {#if nextCursor !== ""}
      <div class="flex justify-center">
        <button
          class="btn btn-outline btn-sm"
          onclick={() => void loadMore()}
          disabled={loading}
        >
          Load more
          <ChevronRight class="h-4 w-4" />
        </button>
      </div>
    {/if}

    {#if total >= 0 && events.length > 0}
      <p class="text-base-content/50 text-xs text-center">
        Showing {events.length}{total > 0 ? ` of ${total}` : ""} events
      </p>
    {/if}
  {/if}
</div>

<!-- Event detail panel -->
<SlidePanel
  open={showDetail}
  title="Audit Event"
  onclose={() => (showDetail = false)}
>
  {#if selectedEvent !== null}
    <div class="flex flex-col gap-3">
      <table class="table table-sm">
        <tbody>
          <tr
            ><th class="w-32">Timestamp</th><td class="font-mono text-xs"
              >{selectedEvent.timestamp}</td
            ></tr
          >
          <tr><th>Actor</th><td>{selectedEvent.actor}</td></tr>
          {#if selectedEvent.tenantID}
            <tr><th>Tenant</th><td>{selectedEvent.tenantID}</td></tr>
          {/if}
          {#if selectedEvent.role}
            <tr><th>Role</th><td>{selectedEvent.role}</td></tr>
          {/if}
          <tr><th>Resource</th><td>{selectedEvent.resource}</td></tr>
          <tr><th>Action</th><td>{selectedEvent.action}</td></tr>
          <tr><th>Outcome</th><td>{selectedEvent.outcome}</td></tr>
          {#if selectedEvent.clientIP}
            <tr
              ><th>Client IP</th><td class="font-mono text-xs"
                >{selectedEvent.clientIP}</td
              ></tr
            >
          {/if}
          {#if selectedEvent.requestID}
            <tr
              ><th>Request ID</th><td class="font-mono text-xs"
                >{selectedEvent.requestID}</td
              ></tr
            >
          {/if}
          {#if selectedEvent.prevHash}
            <tr
              ><th>Prev Hash</th><td class="font-mono text-xs break-all"
                >{selectedEvent.prevHash}</td
              ></tr
            >
          {/if}
        </tbody>
      </table>
      {#if selectedEvent.attributes && Object.keys(selectedEvent.attributes).length > 0}
        <div>
          <h4 class="text-sm font-semibold mb-1">Attributes</h4>
          <pre
            class="bg-base-300 rounded p-3 text-xs overflow-x-auto">{JSON.stringify(
              selectedEvent.attributes,
              null,
              2,
            )}</pre>
        </div>
      {/if}
    </div>
  {/if}
</SlidePanel>
