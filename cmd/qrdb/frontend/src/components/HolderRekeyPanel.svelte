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

  HolderRekeyPanel.svelte
  =======================
  Opens a rekey round, or a revocation (a rekey that removes one holder), on
  a Shamir barrier, and shows the round it opened. It serves the system
  barrier (with a domain and the SO PIN) and a tenant barrier (unseal domain
  only, authorized by RBAC) through the two callbacks its owner supplies.

  Opening a round is all this does: a quorum of the current holders
  completes it from the qrdb CLI or the desktop app, with keys that never
  enter the page.
-->
<script lang="ts" module>
  import type { FactorPolicy, QuorumDomain } from "@qrdb/sdk";

  /** A rekey request as the panel collects it. */
  export interface RekeyInput {
    readonly domain: QuorumDomain;
    readonly threshold: number;
    readonly policy: FactorPolicy;
    readonly soPin: string;
  }

  /** A revocation request as the panel collects it. */
  export interface RevokeInput {
    readonly domain: QuorumDomain;
    readonly username: string;
    /** 0 keeps the current threshold. */
    readonly threshold: number;
    readonly soPin: string;
  }
</script>

<script lang="ts">
  import type { QuorumRound } from "@qrdb/sdk";
  import { Info } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";

  let {
    onStartRekey,
    onRevoke,
    systemScope = false,
    submitCommand,
  }: {
    onStartRekey: (req: RekeyInput) => Promise<QuorumRound>;
    onRevoke: (req: RevokeInput) => Promise<QuorumRound>;
    /** The system barrier: shows the domain choice and the SO PIN. */
    systemScope?: boolean;
    /** The CLI command holders run to complete the round. */
    submitCommand: string;
  } = $props();

  let domain: QuorumDomain = $state("unseal");
  let threshold: number = $state(2);
  let policy: FactorPolicy = $state("designated");
  let revokeUsername: string = $state("");
  let revokeThreshold: number = $state(0);
  let soPin: string = $state("");
  let loading: boolean = $state(false);
  let openRound = $state<QuorumRound | null>(null);

  async function startRekey(): Promise<void> {
    try {
      loading = true;
      openRound = await onStartRekey({ domain, threshold, policy, soPin });
      soPin = "";
      showToast("success", `Rekey round opened: ${openRound.required} holders must submit`);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : "Failed to start the rekey");
    } finally {
      loading = false;
    }
  }

  async function revoke(): Promise<void> {
    const username = revokeUsername.trim();
    if (username === "") {
      showToast("warning", "The holder to revoke is required");
      return;
    }
    try {
      loading = true;
      openRound = await onRevoke({ domain, username, threshold: revokeThreshold, soPin });
      soPin = "";
      revokeUsername = "";
      showToast("success", `Revocation of ${username} opened: ${openRound.required} holders must submit`);
    } catch (err: unknown) {
      showToast("error", err instanceof Error ? err.message : "Failed to revoke the holder");
    } finally {
      loading = false;
    }
  }
</script>

<div class="space-y-3" data-testid="rekey-panel">
  <h3 class="font-semibold text-sm">Rekey or Revoke</h3>
  <p class="text-base-content/60 text-xs">
    {systemScope ? "Opening a round needs the SO PIN." : "Opening a round needs the tenant barrier's rekey permission."}
    A quorum of the current holders completes it
    (<span class="font-mono">{submitCommand}</span>); each holder in the new share
    set then claims a new envelope.
  </p>

  {#if systemScope}
    <div class="grid grid-cols-2 gap-4">
      <div class="form-control">
        <label class="label" for="rekey-domain">
          <span class="label-text text-xs">Domain</span>
        </label>
        <select id="rekey-domain" class="select select-bordered select-sm" bind:value={domain} data-testid="rekey-domain">
          <option value="unseal">Unseal</option>
          <option value="recovery">Recovery</option>
        </select>
      </div>
      <div class="form-control">
        <label class="label" for="rekey-so-pin">
          <span class="label-text text-xs">SO PIN</span>
        </label>
        <input id="rekey-so-pin" type="password" class="input input-bordered input-sm" bind:value={soPin} data-testid="rekey-so-pin" />
      </div>
    </div>
  {/if}

  <div class="grid grid-cols-3 items-end gap-4">
    <div class="form-control">
      <label class="label" for="rekey-threshold">
        <span class="label-text text-xs">New threshold</span>
      </label>
      <input id="rekey-threshold" type="number" min="2" class="input input-bordered input-sm" bind:value={threshold} data-testid="rekey-threshold" />
    </div>
    <div class="form-control">
      <label class="label" for="rekey-policy">
        <span class="label-text text-xs">Factor policy</span>
      </label>
      <select id="rekey-policy" class="select select-bordered select-sm" bind:value={policy} data-testid="rekey-policy">
        <option value="designated">Designated factor</option>
        <option value="any-enrolled">Any enrolled factor</option>
      </select>
    </div>
    <button
      class="btn btn-primary btn-sm"
      onclick={() => void startRekey()}
      disabled={loading || threshold < 2}
      data-testid="start-rekey-btn"
    >
      Start Rekey
    </button>
  </div>

  <div class="grid grid-cols-3 items-end gap-4">
    <div class="form-control">
      <label class="label" for="revoke-username">
        <span class="label-text text-xs">Holder to revoke</span>
      </label>
      <input id="revoke-username" type="text" class="input input-bordered input-sm" bind:value={revokeUsername} data-testid="revoke-username" />
    </div>
    <div class="form-control">
      <label class="label" for="revoke-threshold">
        <span class="label-text text-xs">New threshold (0 keeps it)</span>
      </label>
      <input id="revoke-threshold" type="number" min="0" class="input input-bordered input-sm" bind:value={revokeThreshold} data-testid="revoke-threshold" />
    </div>
    <button
      class="btn btn-error btn-sm"
      onclick={() => void revoke()}
      disabled={loading || revokeUsername.trim() === ""}
      data-testid="revoke-holder-btn"
    >
      Revoke
    </button>
  </div>

  {#if openRound !== null}
    <div role="status" class="alert alert-info py-2 text-xs" data-testid="open-round">
      <Info class="h-3 w-3 shrink-0" />
      <span>
        Open {openRound.op} round on the {openRound.domain} domain:
        {openRound.submitted} of {openRound.required} submissions,
        expires {new Date(openRound.expiresAt).toLocaleString()}.
        {#if openRound.remove && openRound.remove.length > 0}
          Removes {openRound.remove.join(", ")}.
        {/if}
      </span>
    </div>
  {/if}
</div>
