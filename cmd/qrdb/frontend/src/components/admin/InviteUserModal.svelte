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
  import type { Client, Invitation } from "@qrdb/sdk";
  import FormModal from "../FormModal.svelte";
  import { showToast } from "$stores/toast.svelte.ts";

  let {
    client,
    open = false,
    onClose,
  }: {
    client: Client;
    open?: boolean;
    onClose: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let username: string = $state("");
  let email: string = $state("");
  let name: string = $state("");
  let roles: string = $state("");
  let saving: boolean = $state(false);

  /**
   * The issued invitation, once Invite succeeds. The server returns its
   * token exactly once -- this is where that one appearance is shown, so the
   * modal switches into a read-only "copy this now" view rather than closing.
   */
  let issued: Invitation | null = $state(null);

  /** Resets all form state when the modal is reopened. */
  function reset(): void {
    username = "";
    email = "";
    name = "";
    roles = "";
    saving = false;
    issued = null;
  }

  $effect(() => {
    if (open) reset();
  });

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function handleSubmit(): Promise<void> {
    if (!username.trim()) {
      showToast("error", "Username is required");
      return;
    }
    try {
      saving = true;
      issued = await client.invitations.invite({
        username: username.trim(),
        ...(email.trim() ? { email: email.trim() } : {}),
        ...(name.trim() ? { name: name.trim() } : {}),
        ...(roles.trim()
          ? { roleIDs: roles.split(",").map((r) => r.trim()).filter(Boolean) }
          : {}),
      });
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to invite user";
      showToast("error", msg);
    } finally {
      saving = false;
    }
  }

  async function copyToken(): Promise<void> {
    if (!issued) return;
    try {
      await navigator.clipboard.writeText(issued.token);
      showToast("success", "Token copied to clipboard");
    } catch {
      // Clipboard access can be denied by the environment; the token is
      // still selectable text in the modal, so this is not fatal.
      showToast("error", "Could not copy automatically -- select and copy the token manually");
    }
  }
</script>

<FormModal
  {open}
  title={issued ? "Invitation created" : "Invite User"}
  submitLabel={issued ? "Done" : "Invite"}
  loading={saving}
  onsubmit={issued ? onClose : handleSubmit}
  oncancel={onClose}
>
  {#snippet body()}
    {#if issued}
      <div class="flex flex-col gap-3">
        <p class="text-sm text-base-content/70">
          This token is shown <strong>once</strong>. Deliver it to {issued.username} out of
          band; they redeem it themselves with <code>qrdb enroll</code> -- you never hold a
          credential for their account.
        </p>
        <p class="text-sm">
          Tenant <code data-testid="invite-tenant">{issued.tenantID}</code> -- the invitee
          redeems the token in this tenant.
        </p>
        <div class="form-control">
          <div class="label"><span class="label-text">One-time token</span></div>
          <div class="join w-full">
            <input
              type="text"
              readonly
              value={issued.token}
              class="input input-bordered input-sm join-item w-full font-mono"
              onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
            />
            <button type="button" class="btn btn-sm join-item" onclick={copyToken}>
              Copy
            </button>
          </div>
        </div>
        <p class="text-xs text-base-content/60">Expires {issued.expiresAt}</p>
      </div>
    {:else}
      <div class="flex flex-col gap-3">
        <label class="form-control w-full" for="invite-username">
          <div class="label"><span class="label-text">Username</span></div>
          <input
            id="invite-username"
            type="text"
            bind:value={username}
            class="input input-bordered input-sm w-full"
            placeholder="alice"
          />
        </label>
        <label class="form-control w-full" for="invite-email">
          <div class="label"><span class="label-text">Email (canonical login key)</span></div>
          <input
            id="invite-email"
            type="email"
            bind:value={email}
            class="input input-bordered input-sm w-full"
            placeholder="alice@acme.com"
          />
        </label>
        <label class="form-control w-full" for="invite-name">
          <div class="label"><span class="label-text">Display name</span></div>
          <input
            id="invite-name"
            type="text"
            bind:value={name}
            class="input input-bordered input-sm w-full"
            placeholder="Alice"
          />
        </label>
        <label class="form-control w-full" for="invite-roles">
          <div class="label"><span class="label-text">Role IDs (comma-separated)</span></div>
          <input
            id="invite-roles"
            type="text"
            bind:value={roles}
            class="input input-bordered input-sm w-full"
            placeholder="operator-role-id"
          />
        </label>
        <p class="text-xs text-base-content/60">
          Leaving only Username set re-invites that still-pending account with a fresh token.
        </p>
      </div>
    {/if}
  {/snippet}
</FormModal>
