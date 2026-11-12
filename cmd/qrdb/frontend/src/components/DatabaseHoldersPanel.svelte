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
  of them completes from the qrdb CLI.

  A database whose custody is not yet initialized -- its barrier status has
  no initialization time, as after the database wizard's "Finish later" --
  shows instead the enrolment of its remaining holders (HolderEnrolmentForms),
  a Remove for each holder already enrolled (unenrolment: before
  initialization a holder holds no share, so no quorum is needed), and
  Initialize, enabled only once exactly N are enrolled. Holder admin only: no
  share, no envelope and no holder key enters this page.
-->
<script lang="ts">
  import type {
    Client,
    DatabaseBarrierStatus,
    DatabaseShamirInitializeResponse,
    OfficerRegisterRequest,
    OfficerStatus,
  } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import HolderEnrolmentForms from "./wizard/HolderEnrolmentForms.svelte";
  import HolderRekeyPanel from "./HolderRekeyPanel.svelte";

  let {
    client,
    database,
    sharesTotal = 0,
    sharesRequired = 0,
  }: {
    client: Client;
    database: string;
    /** N: the share count the database was created with. */
    sharesTotal?: number;
    /** M: shares required to unseal. */
    sharesRequired?: number;
  } = $props();

  let holders: OfficerStatus[] = $state([]);
  let barrier = $state<DatabaseBarrierStatus | null>(null);
  let loading: boolean = $state(true);
  let error: string = $state("");

  // Enrolment and initialization of a database whose custody is pending.
  let registered: number = $state(0);
  let initializing: boolean = $state(false);
  let initError: string = $state("");
  let split = $state<DatabaseShamirInitializeResponse | null>(null);
  // Unenrolment before initialization. formsEpoch remounts the enrolment
  // forms once the holder list is reloaded, so the holders the forms
  // registered are counted once, from the list.
  let unenrolling: string = $state("");
  let unenrolError: string = $state("");
  let formsEpoch: number = $state(0);

  /**
   * Custody is initialized exactly when the database barrier reports when it
   * was first initialized.
   */
  let initialized = $derived(barrier?.initializedAt !== undefined);
  /** Holders still to enrol before Initialize: N less those enrolled. */
  let remaining = $derived(Math.max(0, sharesTotal - holders.length));
  let enrolled = $derived(holders.length + registered);
  /**
   * More holders than N are enrolled -- possible before the server refused
   * enrolment past N -- so Initialize, which needs exactly N, cannot run.
   */
  let overLimit = $derived(
    !initialized && sharesTotal > 0 && holders.length > sharesTotal,
  );

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      [holders, barrier] = await Promise.all([
        client.officers.listDatabaseOfficers(database),
        client.database.barrierStatus(database),
      ]);
    } catch (err: unknown) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  /** Enrols one more holder of this database's barrier. */
  function enrol(req: OfficerRegisterRequest) {
    return client.officers.registerDatabaseOfficer({ database, ...req });
  }

  /** Removes a holder enrolled before initialization. */
  async function unenrol(username: string): Promise<void> {
    unenrolling = username;
    unenrolError = "";
    try {
      await client.officers.unenrolDatabaseOfficer({ database, username });
      registered = 0;
      formsEpoch += 1;
      await load();
    } catch (err: unknown) {
      unenrolError = err instanceof Error ? err.message : String(err);
    } finally {
      unenrolling = "";
    }
  }

  async function initialize(): Promise<void> {
    initializing = true;
    initError = "";
    try {
      split = await client.officers.initializeDatabaseShamir({ database });
      registered = 0;
      await load();
    } catch (err: unknown) {
      initError = err instanceof Error ? err.message : String(err);
    } finally {
      initializing = false;
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
      <button
        class="btn btn-ghost btn-xs"
        onclick={() => void load()}
        data-testid="db-holders-refresh">Refresh</button
      >
    </div>

    {#if loading}
      <span class="loading loading-spinner loading-sm"></span>
    {:else if error}
      <div
        role="alert"
        class="alert alert-error text-sm"
        data-testid="db-holders-load-error"
      >
        <span>{error}</span>
      </div>
    {:else}
      {#if split !== null}
        <div
          class="alert alert-success text-sm"
          data-testid="db-holders-initialized"
        >
          <span>
            Custody initialized: {split.threshold}-of-{split.totalShares}, {split.shareCount}
            sealed envelopes waiting to be claimed. Each holder claims theirs with
            <code
              >qrdb database barrier-officer claim --database {database}</code
            >.
          </span>
        </div>
      {/if}

      {#if holders.length === 0}
        <div
          class="bg-base-300 rounded-lg p-4 text-center text-sm text-base-content/60"
        >
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
                {#if !initialized}
                  <th></th>
                {/if}
              </tr>
            </thead>
            <tbody>
              {#each holders as holder (holder.username)}
                <tr>
                  <td class="font-mono text-sm">{holder.username}</td>
                  <td class="text-xs">{holder.role}</td>
                  <td class="text-center">{holder.shareIndex}</td>
                  <td class="text-xs">
                    {holder.claimedAt
                      ? "Claimed"
                      : holder.sharePending
                        ? "Pending"
                        : "Not claimed"}
                  </td>
                  {#if !initialized}
                    <td class="text-right">
                      <button
                        class="btn btn-ghost btn-xs"
                        disabled={unenrolling !== ""}
                        onclick={() => void unenrol(holder.username)}
                        data-testid="db-holder-unenrol-{holder.username}"
                      >
                        {#if unenrolling === holder.username}
                          <span class="loading loading-spinner loading-xs"
                          ></span>
                        {/if}
                        Remove
                      </button>
                    </td>
                  {/if}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if unenrolError}
          <div
            role="alert"
            class="alert alert-error text-sm"
            data-testid="db-holders-unenrol-error"
          >
            <span>{unenrolError}</span>
          </div>
        {/if}
      {/if}

      {#if !initialized && sharesTotal > 0}
        <div
          class="border-t border-base-300 space-y-4 pt-4"
          data-testid="db-holders-resume"
        >
          <div role="alert" class="alert alert-warning text-sm">
            <span>
              Custody is not initialized: <span class="font-mono"
                >{database}</span
              >
              stays sealed until all {sharesTotal} holders are enrolled and you initialize;
              then any
              {sharesRequired} of them unseal it together.
            </span>
          </div>
          {#if overLimit}
            <div
              role="alert"
              class="alert alert-error text-sm"
              data-testid="db-holders-over-limit"
            >
              <span>
                {holders.length} holders are enrolled, but the key of
                <span class="font-mono">{database}</span> is split to exactly {sharesTotal}.
                Initialization needs exactly {sharesTotal}: remove
                {holders.length - sharesTotal} holder{holders.length -
                  sharesTotal ===
                1
                  ? ""
                  : "s"}
                before initializing.
              </span>
            </div>
          {/if}
          {#if remaining > 0}
            {#key formsEpoch}
              <HolderEnrolmentForms
                {client}
                count={remaining}
                bind:registeredCount={registered}
                {enrol}
              />
            {/key}
          {/if}
          {#if initError}
            <div
              role="alert"
              class="alert alert-error text-sm"
              data-testid="db-holders-init-error"
            >
              <span>{initError}</span>
            </div>
          {/if}
          <div class="flex justify-end">
            <button
              class="btn btn-primary btn-sm"
              disabled={enrolled !== sharesTotal || initializing}
              onclick={() => void initialize()}
              data-testid="db-holders-panel-initialize"
            >
              {#if initializing}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Initialize
              {#if enrolled !== sharesTotal}
                <span class="text-xs opacity-70"
                  >({enrolled}/{sharesTotal})</span
                >
              {/if}
            </button>
          </div>
        </div>
      {:else if initialized}
        <div class="border-t border-base-300 pt-4">
          <HolderRekeyPanel
            submitCommand="qrdb database barrier-quorum submit --database {database} --op rekey"
            onStartRekey={(r) =>
              client.officers.startDatabaseRekey({
                database,
                threshold: r.threshold,
                policy: r.policy,
              })}
            onRevoke={(r) =>
              client.officers.revokeDatabaseOfficer({
                database,
                username: r.username,
                ...(r.threshold > 0 ? { threshold: r.threshold } : {}),
              })}
          />
        </div>
      {/if}
    {/if}
  </div>
</div>
