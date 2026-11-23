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
  import { getClient, isDesktop, TransportError } from "@qrdb/sdk";
  import type { Client, SealStatus } from "@qrdb/sdk";
  import { Lock, Ticket, XCircle } from "lucide-svelte";
  import RedeemInvitationModal from "./components/RedeemInvitationModal.svelte";
  import MainApp from "./views/MainApp.svelte";
  import SealManager from "./views/admin/SealManager.svelte";
  import { isClusterHeldError } from "./lib/clusterHeld.ts";
  import ServerInitWizard from "./components/ServerInitWizard.svelte";
  import ToastContainer from "./components/ToastContainer.svelte";
  import { showToast } from "./stores/toast.svelte.ts";
  import { initTheme } from "./stores/theme.svelte.ts";
  import { loadProfiles, webStartupTarget } from "./stores/connection-manager.svelte.ts";

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
   *   held          - Node barrier unsealed, but a cluster seal holds the node
   *                   (status refused with 503 SEALED). The Seal Manager is
   *                   shown alone and offers the cluster unseal.
   *   unauthenticated - Web mode: the page's own server refused the status
   *                   read with 401/403, as an initialized auth-enabled server
   *                   does for a visitor with no client certificate. An
   *                   invitee redeems an invitation here (the redeem route is
   *                   public), or retries once a credential is in place.
   *   app           - Fully ready; MainApp handles empty state and connections.
   *   error         - Unrecoverable startup failure.
   */
  type AppState = "loading" | "init" | "unseal" | "held" | "unauthenticated" | "app" | "error";

  let appState: AppState = $state("loading");
  let client: Client | null = $state(null);
  let errorMessage: string = $state("");
  /** Whether the redeem-an-invitation modal is open (unauthenticated state). */
  let showRedeem: boolean = $state(false);

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
        // The saved active profile's server (page origin for the built-in
        // profile): the label and the REST target never disagree on reload.
        const origin = webStartupTarget();
        try {
          console.debug("[App] initialize: connecting REST client to", origin);
          await sdk.connection.connect(origin);
          console.debug("[App] initialize: REST client connected");

          // A sealed server only serves the barrier endpoints. Inspect the seal
          // state and route to the bootstrap (uninitialized) or unseal
          // (initialized) flow before attempting to load the full app shell,
          // which would otherwise fail against a sealed backend.
          let status: SealStatus;
          try {
            status = await sdk.seal.status();
          } catch (err: unknown) {
            // An unsealed node held by a cluster seal refuses status with 503
            // SEALED. That is a recoverable state, not a startup failure:
            // route to the Seal Manager, which offers the cluster unseal.
            if (isClusterHeldError(err)) {
              enterClusterHeld();
              return;
            }
            // The page's own server refusing an unauthenticated status read
            // is not a startup failure either: an invitee has no credential
            // yet, and invitation redemption is public. Offer it.
            if (origin === window.location.origin && isAuthRefusal(err)) {
              console.debug("[App] initialize: status refused unauthenticated");
              appState = "unauthenticated";
              return;
            }
            throw err;
          }
          sealStatus = status;
          if (status.clusterHeld === true) {
            enterClusterHeld();
            return;
          }
          if (status.sealed) {
            appState = status.initialized ? "unseal" : "init";
            console.debug("[App] initialize: server sealed, state =", appState);
            return;
          }
        } catch (err: unknown) {
          // The hosting server failing is unrecoverable. A saved external
          // profile's server being down is not: load the profile picker so
          // the user can pick This Server, another server, or remove the
          // failed profile. loadProfiles marks the active profile's error.
          if (origin === window.location.origin) throw err;
          console.warn("[App] initialize: saved server unavailable", origin, err);
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
      const msg = err instanceof Error ? err.message : "An unknown error occurred";
      errorMessage = msg;
      showToast("error", msg);
      appState = "error";
    }
  }

  /**
   * Whether a request was refused for want of authentication or permission:
   * the SDK's TransportError carrying HTTP 401 or 403.
   */
  function isAuthRefusal(err: unknown): boolean {
    return err instanceof TransportError && (err.statusCode === 401 || err.statusCode === 403);
  }

  /**
   * The node barrier is unsealed but a cluster seal holds the node: it
   * answers no status question, so the app shell cannot load. Mount the
   * Seal Manager alone; it detects the hold itself and offers the cluster
   * unseal, then re-runs initialize once the node serves again.
   */
  function enterClusterHeld(): void {
    sealStatus = null;
    appState = "held";
    console.debug("[App] initialize: node held by a cluster seal");
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
                Unsealing needs <span class="font-mono">{sealStatus.sharesRequired}</span>
                of <span class="font-mono">{sealStatus.sharesTotal}</span> share holders.
              </p>
            {/if}
            <p class="text-base-content/60">
              Each holder submits from the qrdb CLI
              (<code class="font-mono">qrdb barrier quorum submit --op unseal</code>)
              or the desktop app. This page continues once the quorum is met.
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

{:else if appState === "held" && client !== null}
  <main class="mx-auto min-h-screen max-w-4xl p-6" data-testid="app-cluster-held">
    <SealManager {client} onClusterReleased={initialize} />
  </main>

{:else if appState === "unauthenticated" && client !== null}
  <div class="flex min-h-screen items-center justify-center">
    <div class="card bg-base-200 w-96 shadow-xl" data-testid="app-unauthenticated">
      <div class="card-body items-center text-center">
        <h2 class="card-title">Sign-in Required</h2>
        <p class="text-base-content/70 text-sm">
          This server requires a client certificate. If you were sent an invitation, redeem it
          to receive your credentials.
        </p>
        <div class="card-actions mt-4">
          <button
            class="btn btn-primary"
            data-testid="app-redeem-invitation"
            onclick={() => (showRedeem = true)}
          >
            <Ticket class="h-4 w-4" />
            Redeem Invitation
          </button>
          <button class="btn btn-ghost" data-testid="app-unauthenticated-retry" onclick={initialize}>
            Retry
          </button>
        </div>
      </div>
    </div>
  </div>
  <RedeemInvitationModal {client} open={showRedeem} onClose={() => (showRedeem = false)} />

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
