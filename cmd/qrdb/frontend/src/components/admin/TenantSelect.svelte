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
  import type { Client, TenantInfo } from "@qrdb/sdk";
  import { onMount } from "svelte";

  let {
    client,
    value = $bindable(),
    id = undefined,
    placeholder = "Select a tenant\u2026",
    allowEmpty = false,
    allowCreate = false,
    disabled = false,
    size = "sm",
    class: extraClass = "",
    onchange = undefined,
  }: {
    client: Client;
    /** Bound tenant ID. Parent may pass `undefined` before its data is
     * loaded; TenantSelect treats undefined as equivalent to empty string. */
    value: string | undefined;
    id?: string;
    placeholder?: string;
    allowEmpty?: boolean;
    allowCreate?: boolean;
    disabled?: boolean;
    size?: "xs" | "sm" | "md";
    class?: string;
    onchange?: () => void;
  } = $props();

  // Normalise undefined to empty string for all internal comparisons; the
  // bindable `value` can still flow back `undefined` if the parent sets it.
  let currentValue = $derived<string>(value ?? "");

  let tenants: TenantInfo[] = $state([]);
  let loading: boolean = $state(true);
  let fetchError: string | null = $state(null);
  let manualEntry: boolean = $state(false);

  onMount(async () => {
    loading = true;
    fetchError = null;
    try {
      const resp = await client.tenants.list();
      tenants = resp.tenants ?? [];
      // If value is set but not in the tenant list and allowCreate is false,
      // reset to empty string.
      if (currentValue !== "" && !allowCreate) {
        const found = tenants.some((t) => t.id === currentValue);
        if (!found) {
          value = "";
        }
      }
    } catch (err) {
      fetchError =
        err instanceof Error ? err.message : "Failed to load tenants";
      manualEntry = true;
    } finally {
      loading = false;
    }
  });

  function handleSelectChange(event: Event): void {
    const target = event.target as HTMLSelectElement;
    value = target.value;
    onchange?.();
  }

  function handleInputChange(event: Event): void {
    const target = event.target as HTMLInputElement;
    value = target.value;
    onchange?.();
  }

  function toggleManualEntry(): void {
    manualEntry = !manualEntry;
    if (!manualEntry) {
      // Switching back to select: reset if value is not in list
      const found = tenants.some((t) => t.id === currentValue);
      if (!found) {
        value = "";
      }
    }
  }
</script>

<div class={extraClass}>
  {#if loading}
    <div class="flex items-center gap-2 py-2 px-1">
      <span class="loading loading-spinner loading-xs"></span>
      <span class="text-sm text-base-content/50">Loading tenants…</span>
    </div>
  {:else if fetchError !== null || manualEntry}
    <input
      {id}
      type="text"
      class="input input-bordered input-{size} w-full"
      {placeholder}
      {disabled}
      value={currentValue}
      oninput={handleInputChange}
    />
    {#if fetchError !== null}
      <p class="mt-1 text-xs text-warning">
        Tenant list unavailable — enter ID manually
      </p>
    {/if}
    {#if allowCreate && fetchError === null}
      <button
        type="button"
        class="mt-1 text-xs text-primary underline"
        onclick={toggleManualEntry}
      >
        Pick from existing tenants
      </button>
    {/if}
  {:else}
    <select
      {id}
      class="select select-bordered select-{size} w-full"
      {disabled}
      value={currentValue}
      onchange={handleSelectChange}
    >
      {#if allowEmpty}
        <option value="">All tenants</option>
      {:else}
        <option value="" disabled>{placeholder}</option>
      {/if}
      {#each tenants as tenant (tenant.id)}
        <option value={tenant.id}>{tenant.name} ({tenant.id})</option>
      {/each}
    </select>
    {#if allowCreate}
      <button
        type="button"
        class="mt-1 text-xs text-primary underline"
        onclick={toggleManualEntry}
      >
        Enter a new tenant ID manually
      </button>
    {/if}
  {/if}
</div>
