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
  import type { Client, EnrolledCert } from "@qrdb/sdk";
  import FormModal from "./FormModal.svelte";
  import { showToast } from "$stores/toast.svelte.ts";

  /**
   * RedeemInvitationModal exchanges a one-time invitation token plus a CSR for
   * the invited user's first certificate (invitations.redeem). Redeem is the
   * one operation reachable with no credential at all -- the token is the
   * authority -- so it dials the server given here, not an active connection.
   */
  let {
    client,
    open = false,
    onClose,
  }: {
    client: Client;
    open?: boolean;
    onClose: () => void;
  } = $props();

  const TRANSPORTS = ["rest", "grpc", "quic", "mcp", "unix"] as const;

  let server: string = $state("");
  let transport: string = $state("rest");
  let caFile: string = $state("");
  let tenantID: string = $state("");
  let token: string = $state("");
  let csrPEM: string = $state("");
  let saving: boolean = $state(false);
  let error: string = $state("");
  let enrolled: EnrolledCert | null = $state(null);

  function reset(): void {
    server = "";
    transport = "rest";
    caFile = "";
    tenantID = "";
    token = "";
    csrPEM = "";
    saving = false;
    error = "";
    enrolled = null;
  }

  $effect(() => {
    if (open) reset();
  });

  async function handleSubmit(): Promise<void> {
    error = "";
    if (!server.trim()) {
      error = "Server address is required.";
      return;
    }
    if (!tenantID.trim()) {
      error = "Tenant ID is required.";
      return;
    }
    if (!token.trim()) {
      error = "Invitation token is required.";
      return;
    }
    if (!csrPEM.trim()) {
      error = "CSR PEM is required.";
      return;
    }
    try {
      saving = true;
      enrolled = await client.invitations.redeem(
        { tenantID: tenantID.trim(), token: token.trim(), csrPEM: csrPEM.trim() },
        {
          server: server.trim(),
          transport,
          ...(caFile.trim() ? { caFile: caFile.trim() } : {}),
        },
      );
    } catch (err: unknown) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function copyCert(): Promise<void> {
    if (!enrolled) return;
    try {
      await navigator.clipboard.writeText(enrolled.certPEM);
      showToast("success", "Certificate copied to clipboard");
    } catch {
      showToast("error", "Could not copy automatically -- select and copy the certificate manually");
    }
  }
</script>

<FormModal
  {open}
  title={enrolled ? "Invitation redeemed" : "Redeem Invitation"}
  submitLabel={enrolled ? "Done" : "Redeem"}
  loading={saving}
  onsubmit={enrolled ? onClose : handleSubmit}
  oncancel={onClose}
>
  {#snippet body()}
    {#if enrolled}
      <div class="flex flex-col gap-3" data-testid="redeem-result">
        <p class="text-sm text-base-content/70">
          Enrolled <strong data-testid="redeem-username">{enrolled.username}</strong>
          (user ID <code data-testid="redeem-user-id">{enrolled.userID}</code>) in tenant
          <code>{enrolled.tenantID}</code>. Pair this certificate with the private key that
          signed the CSR to connect.
        </p>
        <div class="form-control">
          <div class="label"><span class="label-text">Certificate PEM</span></div>
          <textarea
            readonly
            class="textarea textarea-bordered textarea-sm w-full font-mono text-xs"
            rows="6"
            data-testid="redeem-cert-pem"
            value={enrolled.certPEM}
          ></textarea>
        </div>
        <button type="button" class="btn btn-sm self-start" onclick={copyCert}>
          Copy certificate
        </button>
      </div>
    {:else}
      <div class="flex flex-col gap-3">
        <label class="form-control w-full" for="redeem-server">
          <div class="label"><span class="label-text">Server</span></div>
          <input
            id="redeem-server"
            type="text"
            bind:value={server}
            class="input input-bordered input-sm w-full"
            placeholder="qrdb.example.com:8443"
          />
        </label>
        <label class="form-control w-full" for="redeem-transport">
          <div class="label"><span class="label-text">Transport</span></div>
          <select
            id="redeem-transport"
            bind:value={transport}
            class="select select-bordered select-sm w-full"
          >
            {#each TRANSPORTS as t (t)}
              <option value={t}>{t}</option>
            {/each}
          </select>
        </label>
        <label class="form-control w-full" for="redeem-ca-file">
          <div class="label"><span class="label-text">CA file (optional)</span></div>
          <input
            id="redeem-ca-file"
            type="text"
            bind:value={caFile}
            class="input input-bordered input-sm w-full"
            placeholder="/path/to/ca.pem"
          />
        </label>
        <label class="form-control w-full" for="redeem-tenant">
          <div class="label"><span class="label-text">Tenant ID</span></div>
          <input
            id="redeem-tenant"
            type="text"
            bind:value={tenantID}
            class="input input-bordered input-sm w-full"
            placeholder="default"
          />
        </label>
        <label class="form-control w-full" for="redeem-token">
          <div class="label"><span class="label-text">Invitation token</span></div>
          <input
            id="redeem-token"
            type="text"
            bind:value={token}
            class="input input-bordered input-sm w-full font-mono"
          />
        </label>
        <label class="form-control w-full" for="redeem-csr">
          <div class="label">
            <span class="label-text">CSR PEM (your public key, generated offline)</span>
          </div>
          <textarea
            id="redeem-csr"
            bind:value={csrPEM}
            class="textarea textarea-bordered textarea-sm w-full font-mono text-xs"
            rows="5"
            placeholder="-----BEGIN CERTIFICATE REQUEST-----"
          ></textarea>
        </label>
        {#if error}
          <div class="alert alert-error text-sm" role="alert" data-testid="redeem-error">
            {error}
          </div>
        {/if}
      </div>
    {/if}
  {/snippet}
</FormModal>
