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
  import type { Client, WatchHandle, WatchOptions, EventType } from "@qrdb/sdk";
  import type { Column } from "../../components/core/DataTable/types.ts";
  import {
    watchStore,
    addWatch,
    removeWatch,
    pushEvents,
    clearEvents,
  } from "../../stores/watch.svelte.ts";
  import { showToast } from "../../stores/toast.svelte.ts";
  import { CrudTable } from "../../components/core/CrudTable";
  import { X, Play, Trash2 } from "lucide-svelte";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Event type label map
  // ---------------------------------------------------------------------------

  /** Maps SDK EventType numeric values to human-readable labels. */
  const EVENT_TYPE_LABELS = new Map<EventType, string>([
    [0, "PUT"],
    [1, "DELETE"],
  ]);

  function eventTypeLabel(type: EventType): string {
    const label = EVENT_TYPE_LABELS.get(type);
    if (label === undefined) {
      console.warn(`WatchView: unknown event type value: ${type}`);
      return `UNKNOWN(${type})`;
    }
    return label;
  }

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  interface WatchEventRow {
    time: string;
    type: string;
    key: string;
    value: string;
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let watchKey: string = $state("");
  let startRevision: string = $state("");
  let isPrefix: boolean = $state(false);
  let loading: boolean = $state(false);

  /** Map of active watch handles keyed by server-assigned watch ID. */
  const handles = new Map<number, WatchHandle>();

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  let watchCount = $derived(watchStore.watches.length);

  const eventTableData: WatchEventRow[] = $derived(
    watchStore.events.map((event) => ({
      time: formatTimestamp(event.timestamp),
      type: event.type,
      key: event.key,
      value: event.value ?? "",
    })),
  );

  // ---------------------------------------------------------------------------
  // Column definitions
  // ---------------------------------------------------------------------------

  const eventColumns: Column<WatchEventRow>[] = [
    {
      id: "time",
      header: "Time",
      accessor: "time",
      sortable: true,
      width: "120px",
    },
    {
      id: "type",
      header: "Type",
      accessor: "type",
      sortable: true,
      width: "80px",
    },
    {
      id: "key",
      header: "Key",
      accessor: "key",
      sortable: true,
      filterable: true,
      filterType: "text",
    },
    { id: "value", header: "Value", accessor: "value", sortable: false },
  ];

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function formatTimestamp(ts: number): string {
    return new Date(ts).toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      fractionalSecondDigits: 3,
    });
  }

  function truncateKey(key: string, maxLen: number = 24): string {
    return key.length > maxLen ? key.slice(0, maxLen) + "..." : key;
  }

  // ---------------------------------------------------------------------------
  // Watch lifecycle
  // ---------------------------------------------------------------------------

  async function startWatch(): Promise<void> {
    if (watchKey.trim() === "") {
      showToast("warning", "Key or prefix is required.");
      return;
    }
    loading = true;
    try {
      const revParsed = parseInt(startRevision, 10);
      const opts: WatchOptions =
        !Number.isNaN(revParsed) && revParsed > 0
          ? { startRevision: revParsed }
          : {};

      let handle: WatchHandle;
      if (isPrefix) {
        handle = await client.watch.watchPrefix(watchKey, opts);
      } else {
        handle = await client.watch.watch(watchKey, opts);
      }

      addWatch({ id: handle.id, key: watchKey, isPrefix });
      handles.set(handle.id, handle);

      handle.onEvent((events) => {
        const mapped = events.map((evt) => ({
          type: eventTypeLabel(evt.type),
          key: evt.kv.key,
          value: evt.kv.value,
        }));
        pushEvents(handle.id, mapped);
      });

      handle.onCancel(() => {
        cancelWatchById(handle.id);
      });

      // Clear inputs after successful subscription
      watchKey = "";
      startRevision = "";
      showToast("success", "Watch started successfully.");
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function cancelWatchById(id: number): void {
    const handle = handles.get(id);
    if (handle !== undefined) {
      handle.cancel();
      handles.delete(id);
    }
    removeWatch(id);
  }

  function cancelAllWatches(): void {
    for (const [id, handle] of handles) {
      handle.cancel();
      handles.delete(id);
    }
    watchStore.watches = [];
  }

  function handleClearEvents(): void {
    clearEvents();
  }
</script>

<div class="flex flex-col gap-4">
  <PageHeader
    title="Watch"
    description="Stream real-time change events for keys/prefixes"
  />

  <!-- Zone 1: Compact creation form -->
  <div
    class="flex flex-wrap items-center gap-2 rounded-lg bg-base-200 px-4 py-3"
  >
    <input
      type="text"
      class="input input-bordered input-sm w-full max-w-xs"
      placeholder={isPrefix ? "Key prefix..." : "Exact key..."}
      bind:value={watchKey}
    />
    <input
      type="number"
      class="input input-bordered input-sm w-28"
      placeholder="Start rev"
      bind:value={startRevision}
    />
    <label class="label cursor-pointer gap-1.5">
      <span class="label-text text-xs">Prefix</span>
      <input
        type="checkbox"
        class="toggle toggle-primary toggle-sm"
        bind:checked={isPrefix}
      />
    </label>
    <button
      class="btn btn-primary btn-sm gap-1"
      onclick={startWatch}
      disabled={loading}
    >
      {#if loading}
        <span class="loading loading-spinner loading-xs"></span>
      {:else}
        <Play class="h-3.5 w-3.5" />
      {/if}
      Start Watch
    </button>
  </div>

  <!-- Zone 2: Active watches as chips -->
  {#if watchCount > 0}
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-base-content/50 text-xs font-medium"
        >{watchCount} active watch{watchCount !== 1 ? "es" : ""}</span
      >
      {#each watchStore.watches as w (w.id)}
        <div
          class="flex items-center gap-1.5 rounded-full bg-base-200 px-3 py-1 text-xs"
        >
          <span class="font-mono">{truncateKey(w.key)}</span>
          {#if w.isPrefix}
            <span class="badge badge-info badge-xs leading-none">pfx</span>
          {/if}
          <button
            class="btn btn-ghost btn-circle btn-xs"
            onclick={() => cancelWatchById(w.id)}
            aria-label="Cancel watch {w.key}"
          >
            <X class="h-3 w-3" />
          </button>
        </div>
      {/each}
      <button class="btn btn-ghost btn-xs text-error" onclick={cancelAllWatches}
        >Cancel All</button
      >
    </div>
  {/if}

  <!-- Zone 3: Event stream -->
  <div class="flex flex-col gap-2">
    <div class="flex items-center justify-end">
      <button class="btn btn-ghost btn-xs gap-1" onclick={handleClearEvents}>
        <Trash2 class="h-3 w-3" />
        Clear
      </button>
    </div>
    <CrudTable
      title="Events"
      data={eventTableData}
      columns={eventColumns}
      loading={false}
      showCreate={false}
      showDelete={false}
      pageSize={25}
      emptyMessage="Waiting for events..."
    />
  </div>
</div>
