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
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";
  import { connManagerStore } from "$stores/connection-manager.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  let sqlMySQL: boolean = $state(dbWizardStore.sqlMySQL);
  let sqlPostgreSQL: boolean = $state(dbWizardStore.sqlPostgreSQL);
  let etcdEnabled: boolean = $state(dbWizardStore.etcdEnabled);

  let isRaft = $derived(connManagerStore.activeServerMode === "raft" || dbWizardStore.serverMode === "raft");

  function handleNext(): void {
    dbWizardStore.sqlMySQL = sqlMySQL;
    dbWizardStore.sqlPostgreSQL = sqlPostgreSQL;
    dbWizardStore.etcdEnabled = isRaft ? etcdEnabled : false;
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">{isRaft ? "Protocols" : "SQL Wire Protocols"}</h2>
    <p class="text-base-content/70 mt-2">
      Enable compatible wire protocols for
      <span class="font-mono font-semibold">{dbWizardStore.name}</span>.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    <div class="bg-base-200 rounded-lg p-6">
      <p class="text-base-content/70 mb-4 text-sm">
        Wire protocols allow external clients to connect using standard
        database drivers. This is optional and can be changed later.
      </p>

      <div class="flex flex-col gap-4">
        <label class="flex items-center gap-4 cursor-pointer">
          <input
            type="checkbox"
            class="checkbox checkbox-primary"
            bind:checked={sqlMySQL}
          />
          <div>
            <span class="font-semibold">MySQL</span>
            <p class="text-base-content/60 text-sm">
              MySQL wire protocol (default port 3306). Compatible with MySQL
              clients, ORMs, and database tools.
            </p>
          </div>
        </label>

        <div class="divider my-0"></div>

        <label class="flex items-center gap-4 cursor-pointer">
          <input
            type="checkbox"
            class="checkbox checkbox-primary"
            bind:checked={sqlPostgreSQL}
          />
          <div>
            <span class="font-semibold">PostgreSQL</span>
            <p class="text-base-content/60 text-sm">
              PostgreSQL wire protocol (default port 5432). Compatible with
              PostgreSQL clients, psql, and database tools.
            </p>
          </div>
        </label>

        {#if isRaft}
          <div class="divider my-0"></div>

          <label class="flex items-center gap-4 cursor-pointer">
            <input
              type="checkbox"
              class="checkbox checkbox-primary"
              bind:checked={etcdEnabled}
            />
            <div>
              <span class="font-semibold">etcd</span>
              <span class="badge badge-sm badge-outline ml-1">Raft only</span>
              <p class="text-base-content/60 text-sm">
                etcd-compatible API for this database's Raft group. Enables
                etcd clients, watches, and leases over the distributed KV store.
              </p>
            </div>
          </label>
        {/if}
      </div>
    </div>

    {#if !sqlMySQL && !sqlPostgreSQL && (!isRaft || !etcdEnabled)}
      <div class="alert alert-info">
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-5 w-5 shrink-0"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
        <span class="text-sm">
          No wire protocols selected. The database will be accessible via
          the native REST/gRPC API and the built-in query browser.
        </span>
      </div>
    {/if}
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" onclick={handleNext}>Next</button>
  </div>
</div>
