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
  import { setupWizardStore } from "$stores/setup-wizard.svelte.ts";
  import type { BarrierMode } from "$stores/setup-wizard.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  const barrierModes: readonly {
    readonly id: BarrierMode;
    readonly label: string;
    readonly description: string;
  }[] = [
    {
      id: "none",
      label: "None",
      description:
        "No encryption at rest. Data is stored unencrypted. Suitable only for development environments.",
    },
    {
      id: "passphrase",
      label: "Passphrase",
      description:
        "Passphrase-based encryption barrier. The server is sealed at rest and unsealed on startup with the passphrase.",
    },
    {
      id: "shamir",
      label: "Shamir (M-of-N)",
      description:
        "Advanced: Shamir secret sharing splits the barrier key across multiple operators. Requires M-of-N shares to unseal.",
    },
  ] as const;

  let barrierMode: BarrierMode = $state(setupWizardStore.barrierConfig.mode);
  let passphrase: string = $state(setupWizardStore.barrierConfig.passphrase);
  let passphraseConfirm: string = $state("");
  let shamirThreshold: number = $state(
    setupWizardStore.barrierConfig.shamirThreshold,
  );
  let shamirShares: number = $state(
    setupWizardStore.barrierConfig.shamirShares,
  );

  /**
   * Validation for passphrase mode. A pristine form (both fields untouched)
   * reports no error so the wizard does not open with a red alert; isValid
   * independently gates Next on a non-empty passphrase.
   */
  let passphraseError = $derived<string | null>(
    barrierMode !== "passphrase"
      ? null
      : passphrase.length === 0 && passphraseConfirm.length === 0
        ? null
        : passphrase.length === 0
          ? "Passphrase is required."
          : passphrase !== passphraseConfirm
            ? "Passphrases do not match."
            : null,
  );

  /**
   * Whether the passphrase validation alert is shown.
   *
   * MUST stay a single-dependency $derived null-check. Using a compound
   * short-circuit expression directly in the {#if} condition (e.g.
   * `passphraseError !== null && (passphrase.length > 0 || ...)`) triggers a
   * Svelte 5 fine-grained reactivity defect where the {#if} block fails to
   * unmount after the derived transitions back to null, leaving a stale empty
   * alert in the DOM. Verified empirically (svelte 5.56.1): the compound form
   * fails the regression spec regardless of where the alert is placed, while
   * this single-dependency derived behaves correctly. Pinned by
   * e2e/web-mode-server-init-wizard.spec.ts and e2e/dom/barrier-passphrase.spec.ts.
   */
  let showPassphraseError = $derived(passphraseError !== null);

  /** Validation for Shamir mode. */
  let shamirError = $derived<string | null>(
    barrierMode !== "shamir"
      ? null
      : shamirThreshold <= 0 || shamirShares <= 0
        ? "Both values must be greater than zero."
        : shamirThreshold > shamirShares
          ? "Shares required cannot exceed total shares."
          : null,
  );

  /** Whether the current configuration passes validation. */
  let isValid = $derived(
    barrierMode === "none" ||
      (barrierMode === "passphrase" && passphraseError === null && passphrase.length > 0) ||
      (barrierMode === "shamir" && shamirError === null),
  );

  function handleNext(): void {
    if (!isValid) return;
    setupWizardStore.barrierConfig = {
      mode: barrierMode,
      passphrase: barrierMode === "passphrase" ? passphrase : "",
      shamirThreshold: barrierMode === "shamir" ? shamirThreshold : 3,
      shamirShares: barrierMode === "shamir" ? shamirShares : 5,
    };
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Global Encryption Barrier</h2>
    <p class="text-base-content/70 mt-2">
      Configure server-wide encryption at rest for all data.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    <!-- Barrier mode cards -->
    <div class="grid grid-cols-3 gap-4">
      {#each barrierModes as mode (mode.id)}
        <button
          type="button"
          class="card cursor-pointer border-2 p-4 text-left transition-colors
            {barrierMode === mode.id
              ? 'border-primary bg-primary/5'
              : 'border-base-300 hover:border-primary/50'}"
          onclick={() => (barrierMode = mode.id)}
        >
          <div class="flex items-start gap-3">
            <input
              type="radio"
              name="setup-barrier-mode"
              class="radio radio-primary mt-1"
              value={mode.id}
              checked={barrierMode === mode.id}
              onchange={() => (barrierMode = mode.id)}
            />
            <div>
              <span class="font-semibold">{mode.label}</span>
              <p class="text-base-content/60 mt-1 text-sm">
                {mode.description}
              </p>
            </div>
          </div>
        </button>
      {/each}
    </div>

    <!-- None: warning -->
    {#if barrierMode === "none"}
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
          Without a global encryption barrier, all server data is stored
          unencrypted on disk. This is not recommended for production
          deployments.
        </span>
      </div>
    {/if}

    <!-- Passphrase configuration -->
    {#if barrierMode === "passphrase"}
      <div class="bg-base-200 rounded-lg p-6 space-y-4">
        <h3 class="font-semibold">Passphrase Configuration</h3>

        <div class="form-control">
          <label class="label" for="setup-passphrase">
            <span class="label-text font-semibold">Passphrase</span>
          </label>
          <input
            id="setup-passphrase"
            type="password"
            class="input input-bordered w-full {passphraseError !== null &&
            passphrase.length > 0
              ? 'input-error'
              : ''}"
            placeholder="Enter a strong passphrase"
            bind:value={passphrase}
          />
        </div>

        <div class="form-control">
          <label class="label" for="setup-passphrase-confirm">
            <span class="label-text font-semibold">Confirm Passphrase</span>
          </label>
          <input
            id="setup-passphrase-confirm"
            type="password"
            class="input input-bordered w-full {passphraseError !== null &&
            passphraseConfirm.length > 0
              ? 'input-error'
              : ''}"
            placeholder="Confirm passphrase"
            bind:value={passphraseConfirm}
          />
          <label class="label" for="setup-passphrase-confirm">
            <span class="label-text-alt text-base-content/60">
              The passphrase is required each time the server starts.
              Store it securely — it cannot be recovered if lost.
            </span>
          </label>
        </div>

        <!--
          Validation alert. The {#if} condition MUST remain the
          single-dependency showPassphraseError derived — see its doc comment
          in the script block for the Svelte 5 reactivity defect this avoids.
        -->
        {#if showPassphraseError}
          <div class="alert alert-error">
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
            <span class="text-sm">{passphraseError}</span>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Shamir configuration -->
    {#if barrierMode === "shamir"}
      <div class="bg-base-200 rounded-lg p-6 space-y-4">
        <h3 class="font-semibold">Shamir Secret Sharing</h3>

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
            The global barrier key is split into <strong>N</strong> shares.
            Any <strong>M</strong> of those shares can reconstruct the key and
            unseal the server. Distribute shares to different operators for
            operational security.
          </span>
        </div>

        <div class="grid grid-cols-2 gap-4">
          <div class="form-control">
            <label class="label" for="setup-shamir-threshold">
              <span class="label-text">Shares Required (M)</span>
            </label>
            <input
              id="setup-shamir-threshold"
              type="number"
              class="input input-bordered w-full"
              min="1"
              max="10"
              bind:value={shamirThreshold}
            />
            <label class="label" for="setup-shamir-threshold">
              <span class="label-text-alt text-base-content/60">
                Minimum shares to unseal
              </span>
            </label>
          </div>
          <div class="form-control">
            <label class="label" for="setup-shamir-shares">
              <span class="label-text">Total Shares (N)</span>
            </label>
            <input
              id="setup-shamir-shares"
              type="number"
              class="input input-bordered w-full"
              min="1"
              max="10"
              bind:value={shamirShares}
            />
            <label class="label" for="setup-shamir-shares">
              <span class="label-text-alt text-base-content/60">
                Total shares generated
              </span>
            </label>
          </div>
        </div>

        {#if shamirError !== null}
          <div class="alert alert-error">
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
            <span class="text-sm">{shamirError}</span>
          </div>
        {/if}

        {#if shamirError === null && shamirShares > 0}
          <p class="text-base-content/50 text-sm">
            Configuration: {shamirThreshold}-of-{shamirShares} scheme.
            The system tolerates the loss of {shamirShares - shamirThreshold}
            share{shamirShares - shamirThreshold !== 1 ? "s" : ""}.
          </p>
        {/if}
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
