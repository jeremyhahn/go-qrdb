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

  DatabaseHoldersPanel.svelte
  ===========================
  A Shamir database's share holders: who holds a share and whether they have
  claimed it, and the rekey and revocation rounds (HolderRekeyPanel) a quorum
  of them completes from the qrdb CLI or the desktop app.
-->
<script lang="ts">
  import type { Client, OfficerStatus } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import HolderRekeyPanel from "./HolderRekeyPanel.svelte";

  let { client, database }: { client: Client; database: string } = $props();

  let holders: OfficerStatus[] = $state([]);
  let loading: boolean = $state(true);
  let error: string = $state("");

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      holders = await client.officers.listDatabaseOfficers(database);
    } catch (err: unknown) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
  });
</script>

<div class="card bg-base-200 shadow" data-testid="db-holders-panel">
  <div class="card-body space-y-4">
    <div class="flex items-center justify-between">
      <h3 class="card-title text-base">Share Holders</h3>
      <button class="btn btn-ghost btn-xs" onclick={() => void load()} data-testid="db-holders-refresh">Refresh</button>
    </div>

    {#if loading}
      <span class="loading loading-spinner loading-sm"></span>
    {:else if error}
      <div role="alert" class="alert alert-error text-sm" data-testid="db-holders-load-error">
        <span>{error}</span>
      </div>
    {:else if holders.length === 0}
      <div class="bg-base-300 rounded-lg p-4 text-center text-sm text-base-content/60">
        No share holders enrolled.
      </div>
    {:else}
      <div class="overflow-x-auto">
        <table class="table table-sm" data-testid="db-holders-table">
          <thead>
            <tr>
              <th>Holder</th>
              <th>Role</th>
              <th>Share #</th>
              <th>Claim</th>
            </tr>
          </thead>
          <tbody>
            {#each holders as holder (holder.username)}
              <tr>
                <td class="font-mono text-sm">{holder.username}</td>
                <td class="text-xs">{holder.role}</td>
                <td class="text-center">{holder.shareIndex}</td>
                <td class="text-xs">
                  {holder.claimedAt ? "Claimed" : holder.sharePending ? "Pending" : "Not claimed"}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    <div class="border-t border-base-300 pt-4">
      <HolderRekeyPanel
        submitCommand="qrdb database barrier-quorum submit --database {database} --op rekey"
        onStartRekey={(r) => client.officers.startDatabaseRekey({
          database,
          threshold: r.threshold,
          policy: r.policy,
        })}
        onRevoke={(r) => client.officers.revokeDatabaseOfficer({
          database,
          username: r.username,
          ...(r.threshold > 0 ? { threshold: r.threshold } : {}),
        })}
      />
    </div>
  </div>
</div>
