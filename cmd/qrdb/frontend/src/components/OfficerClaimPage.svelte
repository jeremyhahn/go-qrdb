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

  OfficerClaimPage.svelte
  =======================
  The Security Officer (SO)-facing page for claiming a Shamir share.

  Flow per design §8.2:
    1. SO connects with their mTLS certificate. The page shows their username
       and officer info from the server (via the officer claim status endpoint).
    2. SO clicks "Claim My Share". The page calls
       POST /v1/barrier/officers/{username}/claim/begin.
    3. Server issues a 32-byte challenge nonce (hex). The SO signs the nonce
       with their private key. Because the browser does NOT hold the SO's
       private key (design R4: key-type agnostic), the prescribed UX is a
       paste-based flow: the UI presents the exact openssl command the SO must
       run offline, and they paste the resulting base64 signature back.
    4. Page calls POST /v1/barrier/officers/{username}/claim/complete with
       the pasted signature.
    5. Server returns {share_index, share_bytes} exactly once.
    6. Page displays the share bytes with a prominent "one-time" warning and
       a copy-to-clipboard button.
    7. SO checks "I have recorded my share securely" — confirmation clears the
       share from the DOM. Navigation away also clears it.

  The share is NEVER persisted to localStorage, stores, or any external sink.
  After the DOM clears it, it is gone. No re-display is possible.

  This page is accessible in the app routing. The SO navigates here after
  logging in with their own mTLS certificate.
-->
<script lang="ts">
  import type { Client, OfficerStatus } from "@qrdb/sdk";
  import {
    ShieldCheck,
    AlertTriangle,
    AlertCircle,
    Copy,
    Check,
    KeyRound,
    Terminal,
    Eye,
    EyeOff,
  } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import { onDestroy } from "svelte";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    client,
    username,
    tenantId = undefined,
  }: {
    client: Client;
    /** Username of the authenticated Security Officer (from mTLS CN or session). */
    username: string;
    /** Present when claiming a tenant-barrier share; absent for system barrier. */
    tenantId?: string;
  } = $props();

  // ---------------------------------------------------------------------------
  // State machine
  // ---------------------------------------------------------------------------

  type ClaimStep =
    | "loading"    // fetching officer info
    | "idle"       // officer info loaded, awaiting click
    | "already-claimed" // share already claimed — nothing to do
    | "nonce-ready"   // nonce issued, awaiting SO signature paste
    | "completing"    // claim-complete in flight
    | "revealed"      // share bytes shown — one-time display
    | "done"          // SO acknowledged, share wiped from DOM
    | "error";

  let step: ClaimStep = $state("loading");
  let errorMessage: string = $state("");
  let officerInfo: OfficerStatus | null = $state(null);

  // Nonce challenge state.
  let nonceHex: string = $state("");
  let nonceExpiresAt: string = $state("");
  let signaturePaste: string = $state("");
  let signatureError: string = $state("");

  // Share display (one-time only — wiped on acknowledge or navigation).
  let shareBytes: string = $state("");
  let shareIndex: number = $state(0);
  let shareRevealed: boolean = $state(false);
  let shareAcknowledged: boolean = $state(false);
  let copiedShare: boolean = $state(false);

  // Signing command shown to the officer for offline execution.
  let opensslDisplayCommand: string = $derived(
    nonceHex
      ? `# 1. Decode nonce from hex to a binary file:\n` +
        `echo '${nonceHex}' | xxd -r -p > /tmp/officer_nonce.bin\n\n` +
        `# 2. Sign with your private key (adjust paths):\n` +
        `openssl dgst -sha256 -sign officer_private.pem \\\n` +
        `  -out /tmp/officer_sig.bin /tmp/officer_nonce.bin\n\n` +
        `# 3. Base64-encode the signature and paste it below:\n` +
        `base64 -w 0 /tmp/officer_sig.bin`
      : ""
  );

  // ---------------------------------------------------------------------------
  // Lifecycle — load officer info on mount, wipe share on unmount
  // ---------------------------------------------------------------------------

  async function loadOfficerInfo(): Promise<void> {
    step = "loading";
    try {
      let statuses: OfficerStatus[];
      if (tenantId !== undefined) {
        statuses = await client.officers.tenantOfficerClaimStatus(tenantId);
      } else {
        statuses = await client.officers.officerClaimStatus();
      }
      officerInfo = statuses.find((o) => o.username === username) ?? null;

      if (officerInfo === null) {
        errorMessage = `Officer "${username}" not found in registry. Ensure the admin has registered this officer.`;
        step = "error";
      } else if (!officerInfo.sharePending) {
        step = "already-claimed";
      } else {
        step = "idle";
      }
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : "Failed to load officer status";
      step = "error";
    }
  }

  // Wipe share bytes from memory when the component is destroyed.
  onDestroy(() => {
    wipeShare();
  });

  function wipeShare(): void {
    // Overwrite reactive state to ensure DOM clears.
    shareBytes = "";
    shareIndex = 0;
    shareRevealed = false;
  }

  // Start component lifecycle.
  loadOfficerInfo();

  // ---------------------------------------------------------------------------
  // Claim flow
  // ---------------------------------------------------------------------------

  async function beginClaim(): Promise<void> {
    errorMessage = "";
    try {
      let resp;
      if (tenantId !== undefined) {
        resp = await client.officers.tenantClaimBegin({ tenantId, username });
      } else {
        resp = await client.officers.claimBegin({ username });
      }
      nonceHex = resp.nonceHex;
      nonceExpiresAt = resp.expiresAt;
      step = "nonce-ready";
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : "Failed to begin claim";
      step = "error";
    }
  }

  async function completeClaim(): Promise<void> {
    signatureError = "";
    const sig = signaturePaste.trim();
    if (!sig) {
      signatureError = "Paste your base64-encoded signature before continuing.";
      return;
    }
    step = "completing";
    try {
      let resp;
      if (tenantId !== undefined) {
        resp = await client.officers.tenantClaimComplete({
          tenantId,
          username,
          nonceHex,
          signature: sig,
        });
      } else {
        resp = await client.officers.claimComplete({ username, nonceHex, signature: sig });
      }
      shareBytes = resp.shareBytes;
      shareIndex = resp.shareIndex;
      step = "revealed";
    } catch (err: unknown) {
      errorMessage = err instanceof Error ? err.message : "Claim failed — invalid signature or expired nonce";
      step = "error";
    }
  }

  function handleAcknowledge(): void {
    if (!shareAcknowledged) return;
    wipeShare();
    step = "done";
    showToast("success", "Share acknowledged and cleared from memory.");
  }

  async function copyShare(): Promise<void> {
    if (!shareBytes) return;
    try {
      await navigator.clipboard.writeText(shareBytes);
      copiedShare = true;
      setTimeout(() => { copiedShare = false; }, 2000);
    } catch {
      showToast("error", "Failed to copy to clipboard");
    }
  }
</script>

<div class="flex min-h-screen flex-col items-center justify-center px-4 py-8">
  <div class="w-full max-w-2xl">
    <!-- Header -->
    <div class="mb-6 text-center">
      <div class="flex items-center justify-center gap-3 mb-2">
        <KeyRound class="text-warning h-8 w-8" strokeWidth={2} />
        <h1 class="text-3xl font-bold">Security Officer Share Claim</h1>
      </div>
      <p class="text-base-content/60 mt-1 text-sm">
        Authenticated as <span class="font-mono font-semibold">{username}</span>
        {#if tenantId !== undefined}
          &nbsp;(tenant: <span class="font-mono">{tenantId}</span>)
        {/if}
      </p>
    </div>

    <div class="card bg-base-100 shadow-xl">
      <div class="card-body">

        <!-- ------------------------------------------------------------------ -->
        <!-- Loading                                                              -->
        <!-- ------------------------------------------------------------------ -->
        {#if step === "loading"}
          <div class="flex flex-col items-center gap-4 py-8">
            <span class="loading loading-spinner loading-lg text-warning"></span>
            <p class="text-base-content/60 text-sm">Loading officer status...</p>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Error                                                                -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "error"}
          <div role="alert" class="alert alert-error">
            <AlertCircle class="h-5 w-5 shrink-0" />
            <div>
              <p class="font-semibold">Error</p>
              <p class="text-sm">{errorMessage}</p>
            </div>
          </div>
          <button class="btn btn-outline btn-sm mt-4" onclick={() => void loadOfficerInfo()}>
            Retry
          </button>

        <!-- ------------------------------------------------------------------ -->
        <!-- Already claimed                                                      -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "already-claimed"}
          <div class="flex flex-col items-center gap-4 py-8">
            <ShieldCheck class="text-success h-16 w-16" strokeWidth={1.5} />
            <h2 class="text-xl font-bold">Share Already Claimed</h2>
            <p class="text-base-content/60 text-center text-sm max-w-md">
              Your Shamir share has already been claimed
              {#if officerInfo?.claimedAt}
                on {new Date(officerInfo.claimedAt).toLocaleString()}.
              {:else}
                in a previous session.
              {/if}
              It cannot be retrieved again. If you have lost your share, contact
              your system administrator to initiate a rekey ceremony.
            </p>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Idle — show officer info and Claim button                           -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "idle" && officerInfo !== null}
          <div class="flex flex-col gap-4">
            <div class="flex flex-col items-center gap-2 text-center">
              <KeyRound class="text-warning h-12 w-12" strokeWidth={1.5} />
              <h2 class="text-xl font-bold">Claim Your Shamir Share</h2>
              <p class="text-base-content/60 text-sm">
                Share <strong>#{officerInfo.shareIndex}</strong> is sealed and awaiting your claim.
              </p>
            </div>

            <div class="stats shadow">
              <div class="stat">
                <div class="stat-title">Username</div>
                <div class="stat-value text-base font-mono">{officerInfo.username}</div>
              </div>
              <div class="stat">
                <div class="stat-title">Share Index</div>
                <div class="stat-value text-base">#{officerInfo.shareIndex}</div>
              </div>
              <div class="stat">
                <div class="stat-title">Enrolled</div>
                <div class="stat-value text-base text-sm">
                  {new Date(officerInfo.enrolledAt).toLocaleDateString()}
                </div>
              </div>
            </div>

            <div role="alert" class="alert alert-warning text-sm">
              <AlertTriangle class="h-5 w-5 shrink-0" />
              <span>
                Your share will be returned <strong>exactly once</strong>.
                After you claim it, the server destroys the enclave — it
                cannot be retrieved again. Save it securely before closing
                this page.
              </span>
            </div>

            <button
              class="btn btn-warning btn-block"
              data-testid="claim-begin-btn"
              onclick={() => void beginClaim()}
            >
              <KeyRound class="h-4 w-4" />
              Claim My Share
            </button>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Nonce ready — show signing instructions                             -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "nonce-ready"}
          <div class="flex flex-col gap-4">
            <h2 class="text-lg font-bold flex items-center gap-2">
              <Terminal class="h-5 w-5 text-info" />
              Sign the Challenge Nonce
            </h2>

            <p class="text-sm text-base-content/70">
              The server has issued a one-time challenge nonce. Sign it with
              your Security Officer private key and paste the base64-encoded
              signature below. The nonce expires at
              <span class="font-mono font-semibold text-warning">
                {nonceExpiresAt ? new Date(nonceExpiresAt).toLocaleTimeString() : "(unknown)"}
              </span>.
            </p>

            <!-- Nonce display -->
            <div class="form-control">
              <span class="label-text font-semibold mb-1">Challenge Nonce (hex)</span>
              <div class="bg-base-200 rounded-lg p-3 flex items-center gap-2">
                <span
                  data-testid="challenge-nonce"
                  class="flex-1 break-all font-mono text-xs"
                >{nonceHex}</span>
              </div>
            </div>

            <!-- Signing command -->
            <div class="form-control">
              <span class="label-text font-semibold mb-1 flex items-center gap-1">
                <Terminal class="h-4 w-4" />
                Signing Commands (run offline)
              </span>
              <div class="mockup-code text-xs">
                <pre><code>{opensslDisplayCommand}</code></pre>
              </div>
              <p class="text-xs text-base-content/50 mt-1">
                Replace <span class="font-mono">officer_private.pem</span> with
                the path to your private key file (PEM or PKCS#12). The signing
                algorithm must match the key type in your registered certificate.
              </p>
            </div>

            <!-- Signature paste area -->
            <div class="form-control">
              <label class="label" for="signature-paste">
                <span class="label-text font-semibold">Paste Base64 Signature</span>
              </label>
              <textarea
                id="signature-paste"
                data-testid="signature-paste"
                class="textarea textarea-bordered font-mono text-xs h-24 resize-none
                       {signatureError ? 'textarea-error' : ''}"
                placeholder="Paste the base64-encoded signature output here..."
                bind:value={signaturePaste}
              ></textarea>
              {#if signatureError}
                <div class="label">
                  <span class="label-text-alt text-error">{signatureError}</span>
                </div>
              {/if}
            </div>

            <div class="flex gap-2">
              <button
                class="btn btn-outline btn-sm"
                onclick={() => { step = "idle"; nonceHex = ""; signaturePaste = ""; }}
              >
                Cancel
              </button>
              <button
                class="btn btn-warning flex-1"
                data-testid="claim-complete-btn"
                onclick={() => void completeClaim()}
                disabled={signaturePaste.trim() === ""}
              >
                Complete Claim
              </button>
            </div>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Completing                                                           -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "completing"}
          <div class="flex flex-col items-center gap-4 py-8">
            <span class="loading loading-spinner loading-lg text-warning"></span>
            <p class="text-base-content/60 text-sm">Verifying signature and retrieving share...</p>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Revealed — one-time share display                                   -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "revealed"}
          <div class="flex flex-col gap-5">
            <div class="flex flex-col items-center gap-2 text-center">
              <ShieldCheck class="text-success h-12 w-12" strokeWidth={1.5} />
              <h2 class="text-2xl font-bold">Your Share — Save It Now</h2>
            </div>

            <div role="alert" class="alert alert-error text-sm">
              <AlertTriangle class="h-5 w-5 shrink-0" />
              <span>
                <strong>This is the only time your share will be shown.</strong>
                The server has permanently destroyed its copy. Copy and store
                this value in a secure, offline location before proceeding.
                Navigating away will clear it from this screen.
              </span>
            </div>

            <div class="form-control">
              <div class="flex items-center justify-between mb-1">
                <span class="label-text font-semibold">
                  Share #{shareIndex}
                </span>
                <div class="flex items-center gap-2">
                  <button
                    class="btn btn-ghost btn-xs gap-1"
                    onclick={() => { shareRevealed = !shareRevealed; }}
                    aria-label={shareRevealed ? "Hide share" : "Reveal share"}
                  >
                    {#if shareRevealed}
                      <EyeOff class="h-3 w-3" />
                      Hide
                    {:else}
                      <Eye class="h-3 w-3" />
                      Reveal
                    {/if}
                  </button>
                  <button
                    class="btn btn-ghost btn-xs gap-1"
                    onclick={() => void copyShare()}
                    aria-label="Copy share to clipboard"
                  >
                    {#if copiedShare}
                      <Check class="h-3 w-3 text-success" />
                      Copied
                    {:else}
                      <Copy class="h-3 w-3" />
                      Copy
                    {/if}
                  </button>
                </div>
              </div>
              <div class="bg-base-200 rounded-lg p-3">
                {#if shareRevealed}
                  <span
                    data-testid="share-bytes"
                    class="break-all font-mono text-xs select-all"
                  >{shareBytes}</span>
                {:else}
                  <span class="font-mono text-xs text-base-content/40">
                    ••••••••••••••••••••••••••••••••••••••••••••••••
                    <em class="ml-2 not-italic">(click Reveal to show)</em>
                  </span>
                {/if}
              </div>
            </div>

            <label class="label cursor-pointer justify-start gap-3 mt-2">
              <input
                type="checkbox"
                class="checkbox checkbox-success"
                bind:checked={shareAcknowledged}
                data-testid="share-acknowledged-checkbox"
              />
              <span class="label-text">
                I have securely saved my share in an offline, encrypted location.
                I understand it cannot be retrieved again.
              </span>
            </label>

            <button
              class="btn btn-success btn-block"
              disabled={!shareAcknowledged}
              data-testid="acknowledge-btn"
              onclick={handleAcknowledge}
            >
              I Have Saved My Share
            </button>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Done — share wiped                                                  -->
        <!-- ------------------------------------------------------------------ -->
        {:else if step === "done"}
          <div class="flex flex-col items-center gap-6 py-8">
            <ShieldCheck class="text-success h-16 w-16" strokeWidth={1.5} />
            <div class="text-center">
              <h2 class="text-2xl font-bold">Share Claim Complete</h2>
              <p class="text-base-content/60 mt-2 text-sm max-w-md">
                Your share has been cleared from this session. When the
                unseal ceremony begins, use your saved share to contribute
                to the quorum via the unseal page.
              </p>
            </div>
          </div>
        {/if}

      </div>
    </div>
  </div>
</div>
