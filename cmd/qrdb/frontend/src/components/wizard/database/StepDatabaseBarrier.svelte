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
  import type { SealMode } from "@qrdb/sdk";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } = $props();

  // ---------------------------------------------------------------------------
  // Seal mode
  //
  // The per-database key-scope selector (global vs tenant-scoped key) was
  // removed: the CreateDatabase API has no per-database tenant-scope field
  // (databases are created in the caller's tenant context), so the selector was
  // never sent to the server. Per-tenant encryption barriers are managed in the
  // Tenants admin view; the per-database barrier requires a database barrier
  // manager configured on the daemon.
  // ---------------------------------------------------------------------------

  let sealMode: SealMode = $state(dbWizardStore.sealMode);
  let sharesRequired: number = $state(
    dbWizardStore.sealConfig.sharesRequired ?? 3,
  );
  let sharesTotal: number = $state(
    dbWizardStore.sealConfig.sharesTotal ?? 5,
  );

  // The passphrase and its confirmation live in this step only; the
  // passphrase reaches the store on Next, and the create step takes it from
  // there into the request and clears it. Going back to this step asks for
  // it again rather than redisplaying it.
  let passphrase: string = $state("");
  let passphraseConfirm: string = $state("");

  /** Seal mode options for the card selector. A mode with `unavailable`
   * set is shown, disabled, with that text as its tooltip. */
  const sealModes: readonly {
    id: SealMode;
    label: string;
    description: string;
    unavailable?: string;
  }[] = [
    {
      id: "none",
      label: "None",
      description:
        "No per-database encryption barrier. Data is protected only by the global barrier (if enabled).",
    },
    {
      id: "passphrase",
      label: "Passphrase",
      description:
        "Passphrase-based encryption barrier for this database. Simple and secure.",
    },
    {
      id: "shamir",
      label: "Shamir (M-of-N)",
      description:
        "Advanced: Shamir secret sharing requires multiple key holders to unseal this database.",
      // The server refuses a per-database Shamir barrier until per-database
      // share holders ship; offering it would only produce that refusal.
      unavailable: "Available once per-database Shamir share holders ship.",
    },
  ] as const;

  /** Validation: when Shamir is selected, sharesRequired must be <= sharesTotal, both > 0. */
  let shamirValid = $derived(
    sealMode !== "shamir" ||
      (sharesRequired > 0 &&
        sharesTotal > 0 &&
        sharesRequired <= sharesTotal),
  );

  /** Validation error for the passphrase, when the passphrase mode is selected. */
  let passphraseError = $derived<string | null>(
    sealMode !== "passphrase"
      ? null
      : passphrase.length === 0
        ? "A passphrase is required."
        : passphrase !== passphraseConfirm
          ? "Passphrases do not match."
          : null,
  );

  /** The error shows once the user has typed, not on an untouched form. */
  let showPassphraseError = $derived(
    passphraseError !== null && (passphrase.length > 0 || passphraseConfirm.length > 0),
  );

  let isValid = $derived(shamirValid && passphraseError === null);

  /** Validation error message for Shamir configuration. */
  let validationError = $derived<string | null>(
    sealMode !== "shamir"
      ? null
      : sharesRequired <= 0 || sharesTotal <= 0
        ? "Both values must be greater than zero."
        : sharesRequired > sharesTotal
          ? "Shares required cannot exceed total shares."
          : null,
  );

  function handleNext(): void {
    if (!isValid) return;
    dbWizardStore.sealMode = sealMode;
    dbWizardStore.barrierEnabled = sealMode !== "none";
    dbWizardStore.barrierPassphrase = sealMode === "passphrase" ? passphrase : "";
    passphrase = "";
    passphraseConfirm = "";
    if (sealMode === "shamir") {
      dbWizardStore.sealConfig = { mode: "shamir", sharesRequired, sharesTotal };
    } else {
      dbWizardStore.sealConfig = { mode: sealMode };
    }
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Database Encryption Barrier</h2>
    <p class="text-base-content/70 mt-2">
      Configure per-database encryption at rest for
      <span class="font-mono font-semibold">{dbWizardStore.name}</span>.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">

    <!-- Seal mode cards -->
    <div>
      <p class="text-sm font-semibold text-base-content/80 mb-3">Per-Database Barrier</p>
      <div class="grid grid-cols-3 gap-4">
        {#each sealModes as mode (mode.id)}
          <button
            type="button"
            data-testid={`db-seal-mode-${mode.id}`}
            disabled={mode.unavailable !== undefined}
            title={mode.unavailable}
            class="card border-2 p-4 text-left transition-colors
              {mode.unavailable !== undefined
                ? 'border-base-300 cursor-not-allowed opacity-50'
                : sealMode === mode.id
                  ? 'border-primary bg-primary/5 cursor-pointer'
                  : 'border-base-300 hover:border-primary/50 cursor-pointer'}"
            onclick={() => {
              if (mode.unavailable === undefined) sealMode = mode.id;
            }}
          >
            <div class="flex items-start gap-3">
              <input
                type="radio"
                name="db-seal-mode"
                class="radio radio-primary mt-1"
                value={mode.id}
                checked={sealMode === mode.id}
                disabled={mode.unavailable !== undefined}
                onchange={() => {
                  if (mode.unavailable === undefined) sealMode = mode.id;
                }}
              />
              <div>
                <span class="font-semibold">{mode.label}</span>
                <p class="text-base-content/60 mt-1 text-sm">
                  {mode.description}
                </p>
                {#if mode.unavailable !== undefined}
                  <p class="text-base-content/60 mt-1 text-xs italic">{mode.unavailable}</p>
                {/if}
              </div>
            </div>
          </button>
        {/each}
      </div>
    </div>

    <!-- Passphrase -->
    {#if sealMode === "passphrase"}
      <div class="bg-base-200 space-y-3 rounded-lg p-6">
        <div class="form-control">
          <label class="label" for="db-barrier-passphrase">
            <span class="label-text font-semibold">Passphrase</span>
          </label>
          <input
            id="db-barrier-passphrase"
            type="password"
            autocomplete="new-password"
            class="input input-bordered w-full"
            placeholder="Enter passphrase"
            bind:value={passphrase}
          />
        </div>
        <div class="form-control">
          <label class="label" for="db-barrier-passphrase-confirm">
            <span class="label-text font-semibold">Confirm Passphrase</span>
          </label>
          <input
            id="db-barrier-passphrase-confirm"
            type="password"
            autocomplete="new-password"
            class="input input-bordered w-full"
            placeholder="Re-enter passphrase"
            bind:value={passphraseConfirm}
          />
        </div>
        <p class="text-base-content/60 text-sm">
          The passphrase unseals this database. It is sent once, when the
          database is created, and is not stored here or shown again. A
          per-database barrier needs the database's own data directory, set in
          the Storage step.
        </p>
        <p class="text-error min-h-5 text-sm" role="alert" data-testid="db-barrier-passphrase-error">
          {showPassphraseError ? passphraseError : ""}
        </p>
      </div>
    {/if}

    <!-- Shamir configuration -->
    {#if sealMode === "shamir"}
      <div class="bg-base-200 rounded-lg p-6">
        <h3 class="mb-4 font-semibold">Shamir Secret Sharing</h3>

        <div class="alert alert-info mb-4">
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
            The database encryption key is split into <strong>N</strong> shares.
            Any <strong>M</strong> of those shares can reconstruct the key.
            Distribute shares to different operators for operational security.
          </span>
        </div>

        <div class="grid grid-cols-2 gap-4">
          <div class="form-control">
            <label class="label" for="db-shares-required">
              <span class="label-text">Shares Required (M)</span>
            </label>
            <input
              id="db-shares-required"
              type="number"
              class="input input-bordered w-full"
              min="1"
              max="10"
              bind:value={sharesRequired}
            />
            <label class="label" for="db-shares-required">
              <span class="label-text-alt text-base-content/60">
                Minimum shares to unseal
              </span>
            </label>
          </div>
          <div class="form-control">
            <label class="label" for="db-shares-total">
              <span class="label-text">Total Shares (N)</span>
            </label>
            <input
              id="db-shares-total"
              type="number"
              class="input input-bordered w-full"
              min="1"
              max="10"
              bind:value={sharesTotal}
            />
            <label class="label" for="db-shares-total">
              <span class="label-text-alt text-base-content/60">
                Total shares generated
              </span>
            </label>
          </div>
        </div>

        {#if validationError !== null}
          <div class="alert alert-error mt-4">
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
                d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
            <span class="text-sm">{validationError}</span>
          </div>
        {/if}

        <p class="text-base-content/50 mt-4 text-sm">
          Configuration: {sharesRequired}-of-{sharesTotal} scheme.
          {#if isValid && sharesTotal > 0}
            The system tolerates the loss of {sharesTotal - sharesRequired}
            share{sharesTotal - sharesRequired !== 1 ? "s" : ""}.
          {/if}
        </p>
      </div>
    {:else if sealMode === "none"}
      <div class="alert alert-warning">
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
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L4.082 16.5c-.77.833.192 2.5 1.732 2.5z"
          />
        </svg>
        <span class="text-sm">
          Without a per-database barrier, data in this database relies solely on
          the global encryption barrier for protection at rest.
        </span>
      </div>
    {/if}
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" disabled={!isValid} onclick={handleNext}>
      Next
    </button>
  </div>
</div>
