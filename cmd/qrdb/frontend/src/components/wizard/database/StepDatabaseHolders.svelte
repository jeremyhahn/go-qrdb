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

  StepDatabaseHolders.svelte
  ==========================
  A Shamir database's custody, after the create (docs/desktop-gui): enrol
  exactly N holders through the unified holder model, each from a CSR the
  holder generated, then initialize, which splits the database's key at the
  M-of-N it was created with and seals one envelope to each holder.

  This step administers holders and nothing else. Holders claim their
  envelopes, and later unseal, with their own keys in the qrdb CLI or the
  desktop app, whose bindings keep the keys in Go: no share, no envelope and
  no holder key enters this page, and it runs no cryptography.
-->
<script lang="ts">
  import type { Client, DatabaseShamirInitializeResponse, OfficerRegisterRequest } from "@qrdb/sdk";
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";
  import HolderEnrolmentForms from "../HolderEnrolmentForms.svelte";

  let {
    client,
    onDone,
    onLeave,
  }: {
    client: Client;
    /** The custody is initialized; the wizard is finished. */
    onDone: () => void;
    /** The admin closes the wizard before initializing; the database stays sealed. */
    onLeave: () => void;
  } = $props();

  const database = dbWizardStore.name;
  const total = dbWizardStore.sealConfig.sharesTotal ?? 0;
  const required = dbWizardStore.sealConfig.sharesRequired ?? 0;

  let registered: number = $state(0);
  let initializing: boolean = $state(false);
  let error: string = $state("");
  let split = $state<DatabaseShamirInitializeResponse | null>(null);
  let confirmLeave: boolean = $state(false);

  /** Enrols one holder of this database's barrier. */
  function enrol(req: OfficerRegisterRequest) {
    return client.officers.registerDatabaseOfficer({ database, ...req });
  }

  async function initialize(): Promise<void> {
    initializing = true;
    error = "";
    try {
      split = await client.officers.initializeDatabaseShamir({ database });
    } catch (err: unknown) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      initializing = false;
    }
  }
</script>

<div class="flex flex-col items-center gap-6" data-testid="db-holders-step">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Share Holders</h2>
    <p class="text-base-content/70 mt-2">
      <span class="font-mono font-semibold">{database}</span> is created and sealed. Enrol its
      {total} holders and initialize; then any {required} of them unseal it together.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    {#if split === null}
      <HolderEnrolmentForms {client} count={total} bind:registeredCount={registered} {enrol} />
      {#if error}
        <div role="alert" class="alert alert-error" data-testid="db-holders-error">
          <span>{error}</span>
        </div>
      {/if}
      {#if confirmLeave}
        <div role="alert" class="alert alert-warning" data-testid="db-holders-leave-warning">
          <span class="text-sm">
            <span class="font-mono">{database}</span> stays sealed, and nothing it holds is usable,
            until all {total} holders are enrolled and its custody is initialized. You can finish
            later in the database's view, under Share Holders, or with
            <code>qrdb database barrier-officer register --database {database}</code>
            and <code>qrdb database barrier-init-shamir --database {database}</code>.
          </span>
        </div>
      {/if}
      <div class="flex justify-between">
        {#if confirmLeave}
          <button class="btn btn-ghost" onclick={onLeave} data-testid="db-holders-leave-confirm">
            Close and finish later
          </button>
        {:else}
          <button class="btn btn-ghost" onclick={() => (confirmLeave = true)} data-testid="db-holders-leave">
            Finish later
          </button>
        {/if}
        <button
          class="btn btn-primary"
          disabled={registered !== total || initializing}
          onclick={() => void initialize()}
          data-testid="db-holders-initialize"
        >
          {#if initializing}
            <span class="loading loading-spinner loading-sm"></span>
          {/if}
          Initialize
          {#if registered !== total}
            <span class="text-xs opacity-70">({registered}/{total})</span>
          {/if}
        </button>
      </div>
    {:else}
      <div class="alert alert-success" data-testid="db-holders-initialized">
        <span>
          Custody initialized: {split.threshold}-of-{split.totalShares}, {split.shareCount}
          sealed envelopes waiting to be claimed.
        </span>
      </div>
      <div class="bg-base-200 rounded-lg p-4 text-sm space-y-2" data-testid="db-holders-next">
        <p>
          Each holder claims their envelope once, on their own machine, with their own
          certificate key and the qrdb CLI (the desktop app's binary):
        </p>
        <pre class="font-mono text-xs whitespace-pre-wrap">qrdb database barrier-officer claim --database {database} --username &lt;holder&gt; --key &lt;holder.key&gt; --out &lt;holder.envelope&gt;</pre>
        <p>Then {split.threshold} of them unseal the database:</p>
        <pre class="font-mono text-xs whitespace-pre-wrap">qrdb database barrier-quorum submit --database {database} --op unseal --username &lt;holder&gt; --envelope &lt;holder.envelope&gt; --key &lt;holder.key&gt;</pre>
      </div>
      <div class="flex justify-end">
        <button class="btn btn-primary" onclick={onDone} data-testid="db-holders-done">Finish</button>
      </div>
    {/if}
  </div>
</div>
