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
  import { onMount } from "svelte";
  import { getClient, isDesktop } from "@qrdb/sdk";
  import type { Client, SealStatus } from "@qrdb/sdk";
  import { Lock, XCircle } from "lucide-svelte";
  import MainApp from "./views/MainApp.svelte";
  import ServerInitWizard from "./components/ServerInitWizard.svelte";
  import ToastContainer from "./components/ToastContainer.svelte";
  import { showToast } from "./stores/toast.svelte.ts";
  import { initTheme } from "./stores/theme.svelte.ts";
  import { loadProfiles } from "./stores/connection-manager.svelte.ts";

  // ---------------------------------------------------------------------------
  // App state machine
  // ---------------------------------------------------------------------------

  /**
   * Top-level routing states:
   *   loading       - SDK initializing, profile list not yet fetched.
   *   init          - Connected embedded server needs first-run barrier setup.
   *   unseal        - Connected server is sealed. A passphrase barrier is
   *                   unsealed here; a Shamir barrier opens through a quorum
   *                   of its share holders, who submit from the CLI or the
   *                   desktop app, and this screen waits for them.
   *   app           - Fully ready; MainApp handles empty state and connections.
   *   error         - Unrecoverable startup failure.
   */
  type AppState = "loading" | "init" | "unseal" | "app" | "error";

  let appState: AppState = $state("loading");
  let client: Client | null = $state(null);
  let errorMessage: string = $state("");

  // Unseal state
  let sealStatus = $state<SealStatus | null>(null);
  let unsealShare: string = $state("");
  let unsealError: string = $state("");
  let unsealSubmitting: boolean = $state(false);

  /**
   * Whether the sealed barrier uses Shamir secret sharing (M-of-N shares)
   * rather than a single passphrase. Derived from the reported strategy, with
   * a share-count fallback for transports that don't surface the strategy
   * string. Drives the unseal screen's copy and progress display.
   */
  let unsealIsShamir = $derived(
    sealStatus !== null &&
      (sealStatus.strategy === "shamir" || sealStatus.sharesRequired > 0),
  );

  // ---------------------------------------------------------------------------
  // Initialization
  // ---------------------------------------------------------------------------

  async function initialize(): Promise<void> {
    try {
      console.debug("[App] initialize: loading");
      appState = "loading";
      errorMessage = "";

      console.debug("[App] initialize: getClient");
      const sdk = await getClient();
      client = sdk;
      console.debug("[App] initialize: client ready, isDesktop =", isDesktop());

      // In web mode, connect the REST client to the hosting origin before
      // loading profiles so that the connection state probe in loadProfiles
      // can reach the server.
      if (!isDesktop()) {
        const origin = window.location.origin;
        console.debug("[App] initialize: connecting REST client to", origin);
        await sdk.connection.connect(origin);
        console.debug("[App] initialize: REST client connected");

        // A sealed server only serves the barrier endpoints. Inspect the seal
        // state and route to the bootstrap (uninitialized) or unseal
        // (initialized) flow before attempting to load the full app shell,
        // which would otherwise fail against a sealed backend.
        const status = await sdk.seal.status();
        sealStatus = status;
        if (status.sealed) {
          appState = status.initialized ? "unseal" : "init";
          console.debug("[App] initialize: server sealed, state =", appState);
          return;
        }
      }

      // Both desktop and web modes load profiles then render the app shell.
      // MainApp handles empty profile state, connection errors, and seal flows.
      console.debug("[App] initialize: loading connection profiles");
      await loadProfiles(sdk);

      console.debug("[App] initialize: transition to app");
      appState = "app";
    } catch (err: unknown) {
      console.error("[App] initialize: error", err);
      const msg =
        err instanceof Error ? err.message : "An unknown error occurred";
      errorMessage = msg;
      showToast("error", msg);
      appState = "error";
    }
  }

  // ---------------------------------------------------------------------------
  // Unseal
  // ---------------------------------------------------------------------------

  /**
   * Detects whether an unseal error message indicates a bad credential (wrong
   * passphrase) rather than a genuine transport failure. The server maps a
   * wrong passphrase to 403 and the error carries the decryption-failure
   * signature; we match those robustly so real network errors stay visible.
   */
  function isCredentialFailure(message: string): boolean {
    return (
      /decryption failed/i.test(message) ||
      /invalid credentials/i.test(message) ||
      /\b403\b/.test(message) ||
      /forbidden/i.test(message)
    );
  }

  async function handleUnsealSubmit(): Promise<void> {
    if (client === null || unsealIsShamir || unsealShare.trim() === "") return;

    console.debug("[App] handleUnsealSubmit");
    unsealError = "";
    unsealSubmitting = true;

    try {
      const progress = await client.seal.unseal(unsealShare.trim());
      unsealShare = "";
      console.debug("[App] handleUnsealSubmit: progress", progress);

      if (sealStatus !== null) {
        sealStatus = {
          ...sealStatus,
          sealed: progress.sealed,
          sharesProvided: progress.sharesProvided,
          sharesRequired: progress.sharesRequired,
        };
      }

      if (!progress.sealed) {
        // Fully unsealed: re-run initialization so connection profiles load
        // and the app shell mounts against the now-available backend.
        console.debug("[App] handleUnsealSubmit: unsealed, re-initializing");
        await initialize();
      }
    } catch (err: unknown) {
      console.error("[App] handleUnsealSubmit: error", err);
      const raw = err instanceof Error ? err.message : "Invalid share";
      // A wrong passphrase surfaces as the server's 403 decryption-failure
      // signature. Show a clean, friendly message rather than leaking the raw
      // transport string; preserve the raw message for genuine
      // (non-credential) transport failures.
      unsealError = isCredentialFailure(raw)
        ? "Incorrect passphrase. Please try again."
        : raw;
    } finally {
      unsealSubmitting = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    initTheme();
    initialize();
  });
</script>

{#if appState === "loading"}
  <div class="flex min-h-screen items-center justify-center">
    <div class="flex flex-col items-center gap-4">
      <span class="loading loading-spinner loading-lg text-primary"></span>
      <p class="text-base-content/70 text-sm">Initializing QRDB...</p>
    </div>
  </div>
{:else if appState === "init" && client !== null}
  <ServerInitWizard {client} onComplete={initialize} />
{:else if appState === "unseal" && client !== null}
  <div class="flex min-h-screen items-center justify-center px-4">
    <div class="card bg-base-100 w-full max-w-md shadow-xl">
      <div class="card-body">
        <div class="flex flex-col items-center gap-2">
          <Lock class="h-12 w-12 text-warning" strokeWidth={2} />
          <h2 class="card-title">QRDB is Sealed</h2>
          <p class="text-base-content/60 text-center text-sm">
            {#if unsealIsShamir}
              This barrier is held by its share holders.
            {:else}
              Enter the barrier passphrase to unseal the encryption barrier.
            {/if}
          </p>
        </div>

        {#if unsealIsShamir}
          <!-- A Shamir barrier opens through a holder quorum. Holders claim
               and submit with their own keys, which never enter this page. -->
          <div class="mt-4 space-y-3 text-sm">
            {#if sealStatus !== null && sealStatus.sharesRequired > 0}
              <p class="text-base-content/80">
                Unsealing needs <span class="font-mono"
                  >{sealStatus.sharesRequired}</span
                >
                of <span class="font-mono">{sealStatus.sharesTotal}</span> share holders.
              </p>
            {/if}
            <p class="text-base-content/60">
              Each holder submits from the qrdb CLI (<code class="font-mono"
                >qrdb barrier quorum submit --op unseal</code
              >) or the desktop app. This page continues once the quorum is met.
            </p>
            <button class="btn btn-outline btn-block" onclick={initialize}>
              Check again
            </button>
          </div>
        {:else}
          <!-- Passphrase input -->
          <form
            class="mt-4 space-y-3"
            onsubmit={(e) => {
              e.preventDefault();
              handleUnsealSubmit();
            }}
          >
            <div class="form-control">
              <label class="label" for="unseal-share">
                <span class="label-text">Passphrase</span>
              </label>
              <input
                id="unseal-share"
                type="password"
                placeholder="Enter passphrase"
                class="input input-bordered w-full font-mono"
                bind:value={unsealShare}
                disabled={unsealSubmitting}
              />
            </div>

            {#if unsealError}
              <div role="alert" class="alert alert-error py-2 text-sm">
                <XCircle class="h-5 w-5 shrink-0" />
                <span>{unsealError}</span>
              </div>
            {/if}

            <button
              type="submit"
              class="btn btn-warning btn-block"
              disabled={unsealSubmitting || unsealShare.trim() === ""}
            >
              {#if unsealSubmitting}
                <span class="loading loading-spinner loading-sm"></span>
              {/if}
              Unseal
            </button>
          </form>
        {/if}
      </div>
    </div>
  </div>
{:else if appState === "error"}
  <div class="flex min-h-screen items-center justify-center">
    <div class="card bg-base-200 w-96 shadow-xl">
      <div class="card-body items-center text-center">
        <h2 class="card-title text-error">Connection Error</h2>
        <p class="text-base-content/70 break-all text-sm">{errorMessage}</p>
        <div class="card-actions mt-4">
          <button class="btn btn-primary" onclick={initialize}>Retry</button>
        </div>
      </div>
    </div>
  </div>
{:else if appState === "app" && client !== null}
  <MainApp {client} />
{/if}

<ToastContainer />
