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
  import type { Client, UnsealProgress, SealMode, OfficerStatus, EnableEncryptionRequest } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { AlertTriangle, Info, RefreshCw, Lock, UserPlus, CheckCircle, Clock, AlertCircle, Copy, Download } from "lucide-svelte";
  import { sealStore, updateSealStatus, addShare, updateOfficers, setOfficersLoading } from "$stores/seal.svelte.ts";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";

  let { client, onReconfigure }: {
    client: Client;
    onReconfigure?: () => void;
  } = $props();

  let shareInput: string = $state("");
  let unsealLoading: boolean = $state(false);
  let sealLoading: boolean = $state(false);
  let statusLoading: boolean = $state(true);
  let showSealConfirm: boolean = $state(false);

  // Shamir quorum seal-vote state. Sealing a Shamir-protected barrier is
  // blocked for a single admin; a threshold of Security Officers must each
  // submit their share as a seal vote (server: BarrierShamirSealVote).
  let sealVoteInput: string = $state("");
  let sealVoteLoading: boolean = $state(false);
  let sealVotesSubmitted: number = $state(0);
  let sealVotesRequired: number = $state(0);
  let pollTimer: ReturnType<typeof setInterval> | null = null;

  // ---------------------------------------------------------------------------
  // Security Officers panel state
  // ---------------------------------------------------------------------------

  let showOfficersPanel: boolean = $state(false);

  // Register form state
  let officerRegUsername: string = $state("");
  let officerRegCsrPem: string = $state("");
  let officerRegLoading: boolean = $state(false);
  let officerRegError: string = $state("");

  /** Result of the last successful registration: signed cert PEM. */
  let officerRegCertPem: string = $state("");
  let officerRegCertCopied: boolean = $state(false);

  /** Claim status for the officer panel (distinct from the loading state used by the store). */
  let officerClaimStatusLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Recovery Officers panel state
  //
  // Recovery officers hold shares of a distinct recovery key set (separate
  // from the unseal officer set above). The SDK exposes registration and the
  // begin/complete claim challenge-response flow, but no list/status query —
  // claim status for the recovery set is tracked out of band by the officers
  // themselves.
  // ---------------------------------------------------------------------------

  let showRecoveryOfficersPanel: boolean = $state(false);

  // Register form state
  let recoveryRegUsername: string = $state("");
  let recoveryRegCsrPem: string = $state("");
  let recoveryRegLoading: boolean = $state(false);
  let recoveryRegError: string = $state("");

  /** Result of the last successful registration: signed cert PEM. */
  let recoveryRegCertPem: string = $state("");
  let recoveryRegCertCopied: boolean = $state(false);

  // Claim challenge-response tester state
  let showRecoveryClaimPanel: boolean = $state(false);
  let recoveryClaimUsername: string = $state("");
  let recoveryClaimBeginLoading: boolean = $state(false);
  let recoveryClaimNonceHex: string = $state("");
  let recoveryClaimExpiresAt: string = $state("");
  let recoveryClaimSignature: string = $state("");
  let recoveryClaimCompleteLoading: boolean = $state(false);
  let recoveryClaimError: string = $state("");
  /** The officer's share bytes, returned exactly once by claim completion. */
  let recoveryClaimShareBytes: string = $state("");
  let recoveryClaimShareIndex: number | null = $state(null);
  let recoveryClaimShareCopied: boolean = $state(false);

  // Re-initialization state
  let showReinit: boolean = $state(false);
  let reinitMode: SealMode = $state("passphrase");
  let reinitPassphrase: string = $state("");
  let reinitPassphraseConfirm: string = $state("");
  let reinitShamirThreshold: number = $state(3);
  let reinitShamirShares: number = $state(5);
  let reinitLoading: boolean = $state(false);
  let reinitConfirm: boolean = $state(false);

  /** Percentage of shares provided toward the unseal threshold. */
  let unsealProgress = $derived(
    sealStore.sharesRequired > 0
      ? Math.round((sealStore.sharesProvided / sealStore.sharesRequired) * 100)
      : 0,
  );

  /** Human-readable share progress string. */
  let shareProgressLabel = $derived(
    `${sealStore.sharesProvided} / ${sealStore.sharesRequired}`,
  );

  /**
   * Whether the active barrier uses Shamir secret sharing. Drives whether the
   * Unseal panel prompts for key shares (M-of-N) or a single passphrase.
   * Falls back to the share count for transports that don't report strategy.
   */
  let isShamir = $derived(
    sealStore.strategy === "shamir" || sealStore.sharesRequired > 0,
  );

  /**
   * Whether an encryption barrier is actually configured. Only "passphrase"
   * and "shamir" strategies encrypt data at rest and have a meaningful sealed
   * state. The "none" strategy (and the empty/uninitialized string) performs no
   * encryption, so there is nothing to seal, unseal, or re-key — the seal
   * controls are hidden and a "not configured" panel is shown instead.
   *
   * The Shamir share counts are an additional positive signal: some transports
   * report the M-of-N scheme without echoing the strategy string, so a barrier
   * advertising shares is treated as configured regardless of the strategy
   * field (matching the isShamir fallback above).
   */
  let encryptionConfigured = $derived(
    sealStore.strategy === "passphrase" ||
      sealStore.strategy === "shamir" ||
      sealStore.sharesRequired > 0 ||
      sealStore.sharesTotal > 0,
  );

  /**
   * Whether the barrier subsystem is present but not yet bootstrapped. In this
   * state encryption can still be configured in place (Initialize is valid),
   * because the sealing strategy is chosen at initialization. Once a "none"
   * barrier has been initialized the strategy is fixed and enabling encryption
   * becomes a planned data migration rather than an in-place toggle.
   */
  let canConfigureInPlace = $derived(
    !encryptionConfigured && !sealStore.initialized,
  );

  // Configure-encryption (fresh barrier) form state. Reuses the same fields as
  // the re-initialize form below but drives the one-time Initialize path.
  let showConfigure: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Enable-encryption migration (committed "none" barrier → keyed strategy)
  //
  // For a barrier that was already initialized with strategy "none", encryption
  // is enabled by an online in-process migration that re-encrypts all at-rest
  // data. This view drives the SYSTEM barrier, so the request omits tenant_id
  // (admin scope). The migration is asynchronous: after "migration_started" the
  // status is polled until the strategy changes away from "none".
  // ---------------------------------------------------------------------------
  let enableStrategy: "passphrase" | "shamir" = $state("passphrase");
  let enablePassphrase: string = $state("");
  let enableThreshold: number = $state(3);
  let enableTotal: number = $state(5);
  let enableMechanism: "swap" | "in-place" = $state("swap");
  let enableAvailability: "quiesced" | "online" = $state("quiesced");
  let enableLoading: boolean = $state(false);
  let enableInProgress: boolean = $state(false);
  let enableSuccess: boolean = $state(false);
  let enableSealedAfter: boolean = $state(false);
  let enablePollTimer: ReturnType<typeof setInterval> | null = null;

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the current seal status from the server. */
  async function fetchSealStatus(): Promise<void> {
    try {
      const status = await client.seal.status();
      updateSealStatus(status);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch seal status";
      showToast("error", msg);
    } finally {
      statusLoading = false;
    }
  }

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

  /** Submits a single Shamir share toward the unseal ceremony. */
  async function handleSubmitShare(): Promise<void> {
    const share = shareInput.trim();
    if (share === "") {
      showToast("warning", isShamir ? "A key share is required" : "The passphrase is required");
      return;
    }

    try {
      unsealLoading = true;
      addShare();
      const progress: UnsealProgress = await client.seal.unseal(share);
      shareInput = "";

      if (!progress.sealed) {
        showToast("success", "Server unsealed successfully");
        await fetchSealStatus();
      } else {
        showToast(
          "info",
          `Share accepted (${progress.sharesProvided}/${progress.sharesRequired})`,
        );
      }
    } catch (err: unknown) {
      const raw = err instanceof Error ? err.message : "Failed to submit share";
      // A wrong passphrase surfaces as the server's 403 decryption-failure
      // signature. Show a clean message in passphrase mode rather than leaking
      // the raw transport string; preserve the raw message for Shamir mode and
      // for genuine (non-credential) transport failures.
      const msg =
        !isShamir && isCredentialFailure(raw)
          ? "Incorrect passphrase. Please try again."
          : raw;
      showToast("error", msg);
      // Re-fetch to correct optimistic counter on failure.
      await fetchSealStatus();
    } finally {
      unsealLoading = false;
    }
  }

  /** Seals the server after user confirmation. */
  async function handleSeal(): Promise<void> {
    try {
      sealLoading = true;
      await client.seal.seal();
      showToast("success", "Server sealed successfully");
      showSealConfirm = false;
      await fetchSealStatus();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to seal server";
      showToast("error", msg);
    } finally {
      sealLoading = false;
    }
  }

  /**
   * Submits a Shamir share as a seal vote toward the quorum-seal ceremony.
   * When the threshold of distinct votes is reached the server seals the
   * barrier and destroys all vote enclaves.
   */
  async function handleSubmitSealVote(): Promise<void> {
    const share = sealVoteInput.trim();
    if (share === "") {
      showToast("warning", "A key share is required");
      return;
    }

    try {
      sealVoteLoading = true;
      const progress = await client.officers.submitSealVote({ share });
      sealVoteInput = "";
      sealVotesSubmitted = progress.submitted;
      sealVotesRequired = progress.required;

      if (progress.complete) {
        showToast("success", "Quorum reached — server sealed");
        sealVotesSubmitted = 0;
        sealVotesRequired = 0;
        await fetchSealStatus();
      } else {
        showToast(
          "info",
          `Seal vote accepted (${progress.submitted}/${progress.required})`,
        );
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to submit seal vote";
      showToast("error", msg);
    } finally {
      sealVoteLoading = false;
    }
  }

  /** Re-initializes the barrier with a new configuration. */
  async function handleReinitialize(): Promise<void> {
    try {
      reinitLoading = true;

      if (reinitMode === "passphrase") {
        if (reinitPassphrase !== reinitPassphraseConfirm) {
          showToast("error", "Passphrases do not match");
          return;
        }
        if (reinitPassphrase.length < 8) {
          showToast("error", "Passphrase must be at least 8 characters");
          return;
        }
        await client.seal.initialize(reinitPassphrase);
      } else if (reinitMode === "shamir") {
        await client.seal.initializeShamir(
          reinitPassphrase || "auto-generated",
          reinitShamirThreshold,
          reinitShamirShares,
        );
      }

      showToast("success", "Barrier re-initialized successfully");
      showReinit = false;
      showConfigure = false;
      reinitConfirm = false;
      reinitPassphrase = "";
      reinitPassphraseConfirm = "";
      await fetchSealStatus();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to re-initialize barrier";
      showToast("error", msg);
    } finally {
      reinitLoading = false;
    }
  }

  /**
   * Starts the enable-encryption migration on the system barrier and begins
   * polling for completion. The source barrier must currently be "none"; the
   * server rejects an already-keyed source (409), a shared-barrier tenant (403),
   * or invalid options (400) — those surface as error toasts.
   */
  async function handleEnableEncryption(): Promise<void> {
    if (enableStrategy === "passphrase" && enablePassphrase.trim() === "") {
      showToast("warning", "A passphrase is required");
      return;
    }
    try {
      enableLoading = true;
      // Build the request conditionally so optional fields are omitted (not set
      // to undefined) — required under exactOptionalPropertyTypes. tenant_id is
      // always omitted: SealManager drives the SYSTEM barrier.
      const req: EnableEncryptionRequest = {
        strategy: enableStrategy,
        mechanism: enableMechanism,
        availability: enableAvailability,
        ...(enablePassphrase.trim() === "" ? {} : { passphrase: enablePassphrase }),
        ...(enableStrategy === "shamir"
          ? { shamirThreshold: enableThreshold, shamirTotal: enableTotal }
          : {}),
      };
      const resp = await client.seal.enableEncryption(req);
      if (resp.status === "migration_started") {
        enableInProgress = true;
        showToast("info", "Encryption migration started");
        startEnablePolling();
      } else {
        showToast("info", resp.status);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to enable encryption";
      showToast("error", msg);
    } finally {
      enableLoading = false;
    }
  }

  /**
   * Polls the seal status until it reports a keyed strategy, signalling that
   * the migration has completed. A system-scope migration recycles the data
   * plane in-process and comes back SEALED under the new keyed strategy (the
   * barrier self-describes its persisted strategy while sealed), so
   * {sealed: true, strategy: "passphrase"|"shamir"} is the real completion
   * signal — the operator must then unseal with the new credential. A
   * tenant-scope migration does not seal, so a keyed strategy with
   * sealed: false is equally terminal. Transient status errors during the
   * recycle are ignored.
   */
  function startEnablePolling(): void {
    stopEnablePolling();
    enablePollTimer = setInterval(() => {
      void (async () => {
        try {
          const status = await client.seal.status();
          updateSealStatus(status);
          if (status.strategy !== "none" && status.strategy !== "") {
            stopEnablePolling();
            enableInProgress = false;
            enableSuccess = true;
            enableSealedAfter = status.sealed;
            showToast("success", "Encryption enabled successfully");
          }
        } catch {
          // Transient failure while the data plane recycles — keep polling.
        }
      })();
    }, 1500);
  }

  function stopEnablePolling(): void {
    if (enablePollTimer !== null) {
      clearInterval(enablePollTimer);
      enablePollTimer = null;
    }
  }

  // ---------------------------------------------------------------------------
  // Security Officers actions
  // ---------------------------------------------------------------------------

  /** Fetches the officer list and updates the store. */
  async function fetchOfficers(): Promise<void> {
    setOfficersLoading(true);
    try {
      const officers: OfficerStatus[] = await client.officers.listOfficers();
      updateOfficers(officers);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch officers";
      showToast("error", msg);
    } finally {
      setOfficersLoading(false);
    }
  }

  /** Fetches the current officer claim status and updates the store. */
  async function fetchOfficerClaimStatus(): Promise<void> {
    officerClaimStatusLoading = true;
    try {
      const officers: OfficerStatus[] = await client.officers.officerClaimStatus();
      updateOfficers(officers);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch claim status";
      showToast("error", msg);
    } finally {
      officerClaimStatusLoading = false;
    }
  }

  /** Submits a new officer registration. */
  async function handleRegisterOfficer(): Promise<void> {
    const username = officerRegUsername.trim();
    const csrPem = officerRegCsrPem.trim();
    if (username === "") {
      officerRegError = "Username is required.";
      return;
    }
    if (csrPem === "") {
      officerRegError = "CSR PEM is required.";
      return;
    }
    officerRegLoading = true;
    officerRegError = "";
    officerRegCertPem = "";
    officerRegCertCopied = false;
    try {
      const resp = await client.officers.registerOfficer({ username, csrPem });
      officerRegCertPem = resp.certPem;
      officerRegUsername = "";
      officerRegCsrPem = "";
      showToast("success", `Officer ${username} registered (share #${resp.shareIndex})`);
      // Refresh the list after a successful registration.
      await fetchOfficers();
    } catch (err: unknown) {
      officerRegError = err instanceof Error ? err.message : "Registration failed";
    } finally {
      officerRegLoading = false;
    }
  }

  function copyRegCert(): void {
    void navigator.clipboard.writeText(officerRegCertPem).then(() => {
      officerRegCertCopied = true;
    });
  }

  function downloadRegCert(): void {
    const blob = new Blob([officerRegCertPem], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "officer-cert.pem";
    a.click();
    URL.revokeObjectURL(url);
  }

  // ---------------------------------------------------------------------------
  // Recovery Officers actions
  // ---------------------------------------------------------------------------

  /** Submits a new recovery-officer registration. */
  async function handleRegisterRecoveryOfficer(): Promise<void> {
    const username = recoveryRegUsername.trim();
    const csrPem = recoveryRegCsrPem.trim();
    if (username === "") {
      recoveryRegError = "Username is required.";
      return;
    }
    if (csrPem === "") {
      recoveryRegError = "CSR PEM is required.";
      return;
    }
    recoveryRegLoading = true;
    recoveryRegError = "";
    recoveryRegCertPem = "";
    recoveryRegCertCopied = false;
    try {
      const resp = await client.officers.registerRecoveryOfficer({ username, csrPem });
      recoveryRegCertPem = resp.certPem;
      recoveryRegUsername = "";
      recoveryRegCsrPem = "";
      showToast("success", `Recovery officer ${username} registered (share #${resp.shareIndex})`);
    } catch (err: unknown) {
      recoveryRegError = err instanceof Error ? err.message : "Registration failed";
    } finally {
      recoveryRegLoading = false;
    }
  }

  function copyRecoveryRegCert(): void {
    void navigator.clipboard.writeText(recoveryRegCertPem).then(() => {
      recoveryRegCertCopied = true;
    });
  }

  function downloadRecoveryRegCert(): void {
    const blob = new Blob([recoveryRegCertPem], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "recovery-officer-cert.pem";
    a.click();
    URL.revokeObjectURL(url);
  }

  /** Begins a recovery share claim by issuing a challenge nonce. */
  async function handleRecoveryClaimBegin(): Promise<void> {
    const username = recoveryClaimUsername.trim();
    if (username === "") {
      recoveryClaimError = "Username is required.";
      return;
    }
    recoveryClaimBeginLoading = true;
    recoveryClaimError = "";
    recoveryClaimShareBytes = "";
    recoveryClaimShareIndex = null;
    recoveryClaimShareCopied = false;
    try {
      const resp = await client.officers.recoveryClaimBegin({ username });
      recoveryClaimNonceHex = resp.nonceHex;
      recoveryClaimExpiresAt = resp.expiresAt;
      showToast("success", `Challenge issued for ${username}. Sign the nonce and complete the claim.`);
    } catch (err: unknown) {
      recoveryClaimError = err instanceof Error ? err.message : "Claim begin failed";
    } finally {
      recoveryClaimBeginLoading = false;
    }
  }

  /** Completes a recovery share claim by verifying the officer's signature. */
  async function handleRecoveryClaimComplete(): Promise<void> {
    const username = recoveryClaimUsername.trim();
    const signature = recoveryClaimSignature.trim();
    if (username === "" || recoveryClaimNonceHex === "" || signature === "") {
      recoveryClaimError = "Username, nonce, and signature are all required.";
      return;
    }
    recoveryClaimCompleteLoading = true;
    recoveryClaimError = "";
    try {
      const resp = await client.officers.recoveryClaimComplete({
        username,
        nonceHex: recoveryClaimNonceHex,
        signature,
      });
      recoveryClaimShareBytes = resp.shareBytes;
      recoveryClaimShareIndex = resp.shareIndex;
      showToast("success", `Share #${resp.shareIndex} claimed for ${username}.`);
    } catch (err: unknown) {
      recoveryClaimError = err instanceof Error ? err.message : "Claim completion failed";
    } finally {
      recoveryClaimCompleteLoading = false;
    }
  }

  function copyRecoveryClaimShare(): void {
    void navigator.clipboard.writeText(recoveryClaimShareBytes).then(() => {
      recoveryClaimShareCopied = true;
    });
  }

  // ---------------------------------------------------------------------------
  // Polling
  // ---------------------------------------------------------------------------

  function startPolling(): void {
    stopPolling();
    pollTimer = setInterval(() => {
      void fetchSealStatus();
    }, 5000);
  }

  function stopPolling(): void {
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  onMount(() => {
    void fetchSealStatus().then(() => {
      // Fetch officers after seal status is available; only meaningful for shamir barriers.
      if (sealStore.strategy === "shamir" || sealStore.sharesTotal > 0) {
        void fetchOfficers();
      }
    });
    startPolling();

    return () => {
      stopPolling();
      stopEnablePolling();
    };
  });
</script>

<PageHeader title="Seal Manager" description="Manage the encryption barrier seal state and key shares" />

<div class="flex flex-col gap-6">
  <!-- Seal status display -->
  <div class="stats bg-base-200 shadow">
    <div class="stat">
      <div class="stat-title">Seal Status</div>
      <div class="stat-value text-xl">
        {#if statusLoading}
          <span class="loading loading-spinner loading-sm"></span>
        {:else if !encryptionConfigured}
          <span class="badge badge-lg badge-ghost" data-testid="seal-status-not-configured">
            Not configured
          </span>
        {:else}
          <span
            class="badge badge-lg"
            class:badge-warning={sealStore.sealed}
            class:badge-success={!sealStore.sealed}
          >
            {sealStore.sealed ? "Sealed" : "Unsealed"}
          </span>
        {/if}
      </div>
      <div class="stat-desc">
        {encryptionConfigured ? "Encryption barrier state" : "No encryption at rest"}
      </div>
    </div>

    <div class="stat">
      <div class="stat-title">Initialized</div>
      <div class="stat-value text-xl">
        <span
          class="badge badge-lg"
          class:badge-success={sealStore.initialized}
          class:badge-ghost={!sealStore.initialized}
        >
          {sealStore.initialized ? "Yes" : "No"}
        </span>
      </div>
      <div class="stat-desc">Bootstrap completed</div>
    </div>

    <div class="stat">
      <div class="stat-title">Shares Provided</div>
      <div class="stat-value text-xl font-mono">{shareProgressLabel}</div>
      <div class="stat-desc">Toward unseal threshold</div>
    </div>

    <div class="stat">
      <div class="stat-title">Total Shares</div>
      <div class="stat-value text-xl font-mono">{sealStore.sharesTotal}</div>
      <div class="stat-desc">Generated key shares (N)</div>
    </div>
  </div>

  <!-- Unseal section (only when sealed and encryption is configured) -->
  {#if sealStore.sealed && sealStore.initialized && encryptionConfigured}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Unseal</h2>
        <p class="text-base-content/70 text-sm">
          {#if isShamir}
            Submit Shamir key shares to unseal the encryption barrier.
            {sealStore.sharesRequired} of {sealStore.sharesTotal} shares are required.
          {:else}
            Enter the barrier passphrase to unseal the encryption barrier.
          {/if}
        </p>

        <!-- Progress bar (Shamir only) -->
        {#if isShamir}
          <div class="mt-2 flex items-center gap-3">
            <progress
              class="progress progress-primary w-full"
              value={unsealProgress}
              max="100"
            ></progress>
            <span class="text-sm font-mono whitespace-nowrap">{unsealProgress}%</span>
          </div>
        {/if}

        <!-- Unseal input -->
        <div class="form-control mt-3 gap-5">
          <input
            type="password"
            class="input input-bordered input-sm"
            placeholder={isShamir ? "Enter key share" : "Enter passphrase"}
            bind:value={shareInput}
            onkeydown={(e: KeyboardEvent) => {
              if (e.key === "Enter") void handleSubmitShare();
            }}
          />
          <button
            class="btn btn-primary btn-sm"
            onclick={() => void handleSubmitShare()}
            disabled={unsealLoading || shareInput.trim() === ""}
          >
            {#if unsealLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            {isShamir ? "Submit Share" : "Unseal"}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Seal section (only when unsealed and encryption is configured) -->
  {#if !sealStore.sealed && sealStore.initialized && encryptionConfigured}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Seal</h2>
        {#if isShamir}
          <!-- ---------------------------------------------------------------
            Shamir quorum seal-vote panel. A single admin cannot seal a
            Shamir-protected barrier; the server rejects BarrierSeal with
            ErrShamirBarrierRequiresQuorum. A threshold of Security Officers
            must each submit their claimed share as a seal vote.
          --------------------------------------------------------------- -->
          <p class="text-base-content/70 text-sm">
            Sealing a Shamir-protected barrier requires a quorum of Security
            Officers. {sealStore.sharesRequired} of {sealStore.sharesTotal}
            officers must each submit their key share as a seal vote. When the
            threshold is reached the barrier seals and all vote enclaves are
            destroyed.
          </p>

          {#if sealVotesRequired > 0}
            <div class="mt-2 flex items-center gap-3">
              <progress
                class="progress progress-warning w-full"
                value={sealVotesSubmitted}
                max={sealVotesRequired}
              ></progress>
              <span class="text-sm font-mono whitespace-nowrap" data-testid="seal-vote-progress">
                {sealVotesSubmitted} / {sealVotesRequired}
              </span>
            </div>
          {/if}

          <div class="form-control mt-3 gap-5">
            <input
              type="password"
              class="input input-bordered input-sm"
              placeholder="Enter key share"
              data-testid="seal-vote-input"
              bind:value={sealVoteInput}
              onkeydown={(e: KeyboardEvent) => {
                if (e.key === "Enter") void handleSubmitSealVote();
              }}
            />
            <button
              class="btn btn-warning btn-sm"
              data-testid="submit-seal-vote-btn"
              onclick={() => void handleSubmitSealVote()}
              disabled={sealVoteLoading || sealVoteInput.trim() === ""}
            >
              {#if sealVoteLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              <Lock class="h-4 w-4" />
              Submit Seal Vote
            </button>
          </div>
        {:else}
          <p class="text-base-content/70 text-sm">
            Seal the encryption barrier. All in-flight operations will complete, but
            new operations will be rejected until the server is unsealed again.
          </p>

          {#if showSealConfirm}
            <div class="alert alert-warning mt-3">
              <AlertTriangle class="h-5 w-5 shrink-0" />
              <span>Are you sure? The server will reject all operations until unsealed.</span>
            </div>
            <div class="mt-2 flex gap-2">
              <button
                class="btn btn-error btn-sm"
                onclick={() => void handleSeal()}
                disabled={sealLoading}
              >
                {#if sealLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                Confirm Seal
              </button>
              <button
                class="btn btn-ghost btn-sm"
                onclick={() => { showSealConfirm = false; }}
              >
                Cancel
              </button>
            </div>
          {:else}
            <button
              class="btn btn-warning btn-sm mt-3"
              title="Seals the encryption barrier. All in-flight operations complete, but new operations will be rejected until the server is unsealed again."
              onclick={() => { showSealConfirm = true; }}
            >
              <Lock class="h-4 w-4" />
              Seal
            </button>
          {/if}
        {/if}
      </div>
    </div>
  {/if}

  <!--
    Shared barrier-configuration fields (mode selection + passphrase / Shamir
    parameters). Used by both the "Configure Encryption" panel (fresh barrier)
    and the "Re-initialize Barrier" card (existing barrier), driven through
    handleReinitialize → seal.initialize / seal.initializeShamir.
  -->
  {#snippet barrierConfigFields()}
    <!-- Mode selection -->
    <div class="flex gap-4">
      <label class="label cursor-pointer gap-2">
        <input
          type="radio"
          class="radio radio-primary"
          name="reinit-mode"
          value="passphrase"
          checked={reinitMode === "passphrase"}
          onchange={() => { reinitMode = "passphrase"; }}
        />
        <span class="label-text">Passphrase</span>
      </label>
      <label class="label cursor-pointer gap-2">
        <input
          type="radio"
          class="radio radio-primary"
          name="reinit-mode"
          value="shamir"
          checked={reinitMode === "shamir"}
          onchange={() => { reinitMode = "shamir"; }}
        />
        <span class="label-text">Shamir Secret Sharing</span>
      </label>
    </div>

    {#if reinitMode === "passphrase"}
      <div class="form-control">
        <label class="label" for="reinit-passphrase">
          <span class="label-text">New Passphrase</span>
        </label>
        <input
          id="reinit-passphrase"
          type="password"
          class="input input-bordered input-sm"
          placeholder="Minimum 8 characters"
          bind:value={reinitPassphrase}
        />
      </div>
      <div class="form-control">
        <label class="label" for="reinit-passphrase-confirm">
          <span class="label-text">Confirm Passphrase</span>
        </label>
        <input
          id="reinit-passphrase-confirm"
          type="password"
          class="input input-bordered input-sm"
          placeholder="Re-enter passphrase"
          bind:value={reinitPassphraseConfirm}
        />
      </div>
    {:else}
      <div class="grid grid-cols-2 gap-4">
        <div class="form-control">
          <label class="label" for="reinit-threshold">
            <span class="label-text">Threshold (M)</span>
          </label>
          <input
            id="reinit-threshold"
            type="number"
            class="input input-bordered input-sm"
            min="1"
            max={reinitShamirShares}
            bind:value={reinitShamirThreshold}
          />
        </div>
        <div class="form-control">
          <label class="label" for="reinit-shares">
            <span class="label-text">Total Shares (N)</span>
          </label>
          <input
            id="reinit-shares"
            type="number"
            class="input input-bordered input-sm"
            min={reinitShamirThreshold}
            max="10"
            bind:value={reinitShamirShares}
          />
        </div>
      </div>
      <p class="text-base-content/60 text-xs">
        {reinitShamirThreshold} of {reinitShamirShares} shares will be
        required to unseal.
      </p>
    {/if}
  {/snippet}

  <!--
    Encryption not configured (strategy "none" or uninitialized).
    The card stays mounted through an in-flight or completed enable-encryption
    migration (enableInProgress / enableSuccess) so the progress and success
    states survive the seal-status store flipping to the keyed strategy.
  -->
  {#if (!encryptionConfigured || enableInProgress || enableSuccess) && !statusLoading}
    <div class="card bg-base-200 shadow" data-testid="encryption-not-configured-card">
      <div class="card-body">
        <h2 class="card-title text-lg">
          {enableSuccess ? "Encryption Enabled" : "Encryption Not Configured"}
        </h2>
        {#if !encryptionConfigured}
          <div class="alert alert-warning">
            <AlertTriangle class="h-5 w-5 shrink-0" />
            <span class="text-sm">
              This server has <strong>no encryption barrier</strong>. All data is
              stored unencrypted on disk. There is nothing to seal or unseal —
              sealing only protects an encrypted store. Configuring an encryption
              barrier is strongly recommended for production deployments.
            </span>
          </div>
        {/if}

        {#if canConfigureInPlace}
          <!-- Fresh barrier: encryption can be configured in place. -->
          <div class="flex items-center justify-between">
            <p class="text-base-content/70 text-sm">
              Configure an encryption barrier now. The sealing strategy is fixed
              once initialized, so choose carefully.
            </p>
            <button
              class="btn btn-primary btn-sm"
              data-testid="configure-encryption-btn"
              onclick={() => { showConfigure = !showConfigure; }}
            >
              <Lock class="h-4 w-4" />
              {showConfigure ? "Cancel" : "Configure Encryption"}
            </button>
          </div>

          {#if showConfigure}
            <div class="mt-2 space-y-4" data-testid="configure-encryption-form">
              {@render barrierConfigFields()}
              <button
                class="btn btn-primary btn-sm"
                data-testid="configure-encryption-submit"
                onclick={() => void handleReinitialize()}
                disabled={reinitLoading}
              >
                {#if reinitLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                Configure Encryption
              </button>
            </div>
          {/if}
        {:else}
          <!--
            Committed "none" barrier: enable at-rest encryption with an online
            in-process migration that re-encrypts all data under a newly minted
            keyed strategy. This view drives the SYSTEM barrier (admin scope);
            per-tenant migration lives in the Tenant Manager.
            See docs/seal/enabling-encryption.md.
          -->
          <div class="mt-2 space-y-4" data-testid="enable-encryption-wizard">
            {#if enableSuccess}
              <div class="alert alert-success" data-testid="enable-encryption-success">
                <CheckCircle class="h-5 w-5 shrink-0" />
                <span class="text-sm">
                  Encryption enabled. This barrier now uses the
                  <strong>{sealStore.strategy}</strong> strategy.
                </span>
              </div>
              {#if enableSealedAfter}
                <div class="alert alert-warning" data-testid="enable-encryption-unseal-guidance">
                  <AlertTriangle class="h-5 w-5 shrink-0" />
                  <span class="text-sm">
                    The barrier is now <strong>sealed</strong>. Unseal it with the
                    new {sealStore.strategy === "shamir"
                      ? "Shamir share quorum"
                      : "passphrase"} to resume serving data.
                  </span>
                </div>
              {/if}
            {:else if enableInProgress}
              <div class="alert alert-info" data-testid="enable-encryption-progress">
                <span class="loading loading-spinner loading-sm"></span>
                <span class="text-sm">
                  Migration in progress — re-encrypting at-rest data. This panel
                  updates automatically when the migration completes.
                </span>
              </div>
            {:else}
              <div class="alert alert-info">
                <Info class="h-5 w-5 shrink-0" />
                <div class="text-sm">
                  <p class="font-semibold">Enable encryption with an online migration.</p>
                  <p class="mt-1">
                    All stored data is re-encrypted under a new keyed strategy.
                    The data plane briefly pauses while the migration runs.
                  </p>
                </div>
              </div>

              <div class="form-control">
                <label class="label" for="enable-encryption-strategy-select">
                  <span class="label-text">Target Strategy</span>
                </label>
                <select
                  id="enable-encryption-strategy-select"
                  class="select select-bordered select-sm"
                  data-testid="enable-encryption-strategy"
                  bind:value={enableStrategy}
                >
                  <option value="passphrase">Passphrase</option>
                  <option value="shamir">Shamir Secret Sharing</option>
                </select>
              </div>

              {#if enableStrategy === "passphrase"}
                <div class="form-control">
                  <label class="label" for="enable-encryption-passphrase-input">
                    <span class="label-text">Passphrase</span>
                  </label>
                  <input
                    id="enable-encryption-passphrase-input"
                    type="password"
                    class="input input-bordered input-sm"
                    placeholder="Unseal passphrase"
                    data-testid="enable-encryption-passphrase"
                    bind:value={enablePassphrase}
                  />
                </div>
              {:else}
                <div class="grid grid-cols-2 gap-4">
                  <div class="form-control">
                    <label class="label" for="enable-encryption-threshold">
                      <span class="label-text">Threshold (M)</span>
                    </label>
                    <input
                      id="enable-encryption-threshold"
                      type="number"
                      class="input input-bordered input-sm"
                      min="1"
                      max={enableTotal}
                      bind:value={enableThreshold}
                    />
                  </div>
                  <div class="form-control">
                    <label class="label" for="enable-encryption-total">
                      <span class="label-text">Total Shares (N)</span>
                    </label>
                    <input
                      id="enable-encryption-total"
                      type="number"
                      class="input input-bordered input-sm"
                      min={enableThreshold}
                      max="10"
                      bind:value={enableTotal}
                    />
                  </div>
                </div>
                <div class="form-control">
                  <label class="label" for="enable-encryption-shamir-passphrase">
                    <span class="label-text">Passphrase (optional)</span>
                  </label>
                  <input
                    id="enable-encryption-shamir-passphrase"
                    type="password"
                    class="input input-bordered input-sm"
                    placeholder="Used to derive the master key"
                    bind:value={enablePassphrase}
                  />
                </div>
              {/if}

              <div class="grid grid-cols-2 gap-4">
                <div class="form-control">
                  <label class="label" for="enable-encryption-mechanism">
                    <span class="label-text">Mechanism</span>
                  </label>
                  <select
                    id="enable-encryption-mechanism"
                    class="select select-bordered select-sm"
                    bind:value={enableMechanism}
                  >
                    <option value="swap">Swap (default)</option>
                    <option value="in-place">In-place</option>
                  </select>
                </div>
                <div class="form-control">
                  <label class="label" for="enable-encryption-availability">
                    <span class="label-text">Availability</span>
                  </label>
                  <select
                    id="enable-encryption-availability"
                    class="select select-bordered select-sm"
                    bind:value={enableAvailability}
                  >
                    <option value="quiesced">Quiesced (default)</option>
                    <option value="online">Online</option>
                  </select>
                </div>
              </div>

              <button
                class="btn btn-primary btn-sm"
                data-testid="enable-encryption-submit"
                onclick={() => void handleEnableEncryption()}
                disabled={enableLoading}
              >
                {#if enableLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                <Lock class="h-4 w-4" />
                Enable Encryption
              </button>

              {#if onReconfigure}
                <button
                  class="btn btn-outline btn-sm"
                  data-testid="reconfigure-server-from-not-configured"
                  onclick={onReconfigure}
                >
                  <RefreshCw class="h-4 w-4" />
                  Reconfigure Server
                </button>
              {/if}
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Re-initialize section (only when an encryption barrier is configured and unsealed) -->
  {#if encryptionConfigured && sealStore.initialized && !sealStore.sealed}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Re-initialize Barrier</h2>
          <button
            class="btn btn-ghost btn-sm"
            onclick={() => { showReinit = !showReinit; reinitConfirm = false; }}
          >
            <RefreshCw class="h-4 w-4" />
            {showReinit ? "Cancel" : "Configure"}
          </button>
        </div>
        <p class="text-base-content/70 text-sm">
          Re-initialize the encryption barrier with a new configuration. This
          will generate new key material and require re-distributing shares.
        </p>

        {#if showReinit}
          <div class="mt-4 space-y-4">
            {@render barrierConfigFields()}

            {#if reinitConfirm}
              <div class="alert alert-warning">
                <AlertTriangle class="h-5 w-5 shrink-0" />
                <span>This will replace the existing barrier configuration. Proceed?</span>
              </div>
              <div class="flex gap-2">
                <button
                  class="btn btn-error btn-sm"
                  onclick={() => void handleReinitialize()}
                  disabled={reinitLoading}
                >
                  {#if reinitLoading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {/if}
                  Confirm Re-initialize
                </button>
                <button
                  class="btn btn-ghost btn-sm"
                  onclick={() => { reinitConfirm = false; }}
                >
                  Cancel
                </button>
              </div>
            {:else}
              <button
                class="btn btn-warning btn-sm"
                onclick={() => { reinitConfirm = true; }}
              >
                Re-initialize
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Security Officers panel (Shamir barriers only) -->
  {#if sealStore.strategy === "shamir" || sealStore.sharesTotal > 0}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Security Officers</h2>
          <div class="flex gap-2">
            <button
              class="btn btn-ghost btn-sm gap-1"
              onclick={() => void fetchOfficerClaimStatus()}
              disabled={officerClaimStatusLoading || sealStore.officersLoading}
              aria-label="Refresh officer claim status"
              data-testid="refresh-officers-btn"
            >
              <RefreshCw class={`h-4 w-4 ${(officerClaimStatusLoading || sealStore.officersLoading) ? 'animate-spin' : ''}`} />
              Refresh
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showOfficersPanel = !showOfficersPanel; officerRegError = ""; officerRegCertPem = ""; }}
              data-testid="toggle-register-officer-btn"
            >
              <UserPlus class="h-4 w-4" />
              {showOfficersPanel ? "Cancel" : "Register Officer"}
            </button>
          </div>
        </div>

        <p class="text-base-content/70 text-sm">
          Security Officers hold individual Shamir shares needed to unseal the barrier.
          Register officers here before performing a rekey, or to enroll additional officers.
        </p>

        <!-- Officer claim status table -->
        {#if sealStore.officersLoading}
          <div class="flex justify-center py-4">
            <span class="loading loading-spinner loading-sm text-primary" data-testid="officers-loading"></span>
          </div>
        {:else if sealStore.officers.length === 0}
          <div class="bg-base-300 rounded-lg p-4 text-center text-sm text-base-content/60" data-testid="no-officers-msg">
            No Security Officers registered.
          </div>
        {:else}
          <div class="overflow-x-auto">
            <table class="table table-sm" data-testid="seal-manager-officers-table">
              <thead>
                <tr>
                  <th>Officer</th>
                  <th>Share #</th>
                  <th>Auth Factor</th>
                  <th>Enrolled</th>
                  <th>Claim Status</th>
                </tr>
              </thead>
              <tbody>
                {#each sealStore.officers as officer (officer.username)}
                  <tr>
                    <td class="font-mono text-sm">{officer.username}</td>
                    <td class="text-center">{officer.shareIndex}</td>
                    <td class="text-xs">{officer.authFactor || "software"}</td>
                    <td class="text-xs text-base-content/60">
                      {officer.enrolledAt ? new Date(officer.enrolledAt).toLocaleDateString() : "—"}
                    </td>
                    <td>
                      {#if officer.claimedAt}
                        <span class="badge badge-success gap-1 text-xs">
                          <CheckCircle class="h-3 w-3" />
                          Claimed
                        </span>
                      {:else if officer.sharePending}
                        <span class="badge badge-warning gap-1 text-xs">
                          <Clock class="h-3 w-3" />
                          Pending
                        </span>
                      {:else}
                        <span class="badge badge-ghost text-xs">
                          Not claimed
                        </span>
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}

        <!-- Register new officer form -->
        {#if showOfficersPanel}
          <div class="mt-4 space-y-3 border-t border-base-300 pt-4">
            <h3 class="font-semibold text-sm">Register New Officer</h3>

            <div class="form-control">
              <label class="label" for="so-username">
                <span class="label-text text-xs">Username</span>
              </label>
              <input
                id="so-username"
                type="text"
                class="input input-bordered input-sm"
                placeholder="officer-alice"
                bind:value={officerRegUsername}
                disabled={officerRegLoading}
                data-testid="so-username-input"
              />
            </div>

            <div class="form-control">
              <label class="label" for="so-csr">
                <span class="label-text text-xs">CSR PEM (officer's public key, generated offline)</span>
              </label>
              <textarea
                id="so-csr"
                class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
                rows={5}
                placeholder={"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----"}
                bind:value={officerRegCsrPem}
                disabled={officerRegLoading}
                data-testid="so-csr-input"
              ></textarea>
            </div>

            {#if officerRegError}
              <div role="alert" class="alert alert-error py-2 text-xs" data-testid="so-reg-error">
                <AlertCircle class="h-3 w-3 shrink-0" />
                <span>{officerRegError}</span>
              </div>
            {/if}

            <!-- Returned certificate after successful registration -->
            {#if officerRegCertPem}
              <div class="form-control">
                <div class="flex items-center justify-between mb-1">
                  <span class="label-text text-xs font-semibold text-success">Signed Certificate PEM</span>
                  <div class="flex gap-1">
                    <button
                      class="btn btn-ghost btn-xs gap-1"
                      onclick={copyRegCert}
                      aria-label="Copy certificate PEM"
                      data-testid="so-copy-cert-btn"
                    >
                      <Copy class="h-3 w-3" />
                      {officerRegCertCopied ? "Copied" : "Copy"}
                    </button>
                    <button
                      class="btn btn-ghost btn-xs gap-1"
                      onclick={downloadRegCert}
                      aria-label="Download certificate PEM"
                      data-testid="so-download-cert-btn"
                    >
                      <Download class="h-3 w-3" />
                      Download
                    </button>
                  </div>
                </div>
                <textarea
                  class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
                  rows={4}
                  readonly
                  value={officerRegCertPem}
                  data-testid="so-reg-cert-output"
                ></textarea>
                <p class="text-base-content/50 mt-1 text-xs">
                  Transmit this certificate to the officer out of band. They will use it to authenticate and claim their share.
                </p>
              </div>
            {/if}

            <button
              class="btn btn-primary btn-sm"
              onclick={() => void handleRegisterOfficer()}
              disabled={officerRegLoading || officerRegUsername.trim() === "" || officerRegCsrPem.trim() === ""}
              data-testid="so-register-btn"
            >
              {#if officerRegLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Register
            </button>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Recovery Officers panel (Shamir barriers only) -->
  {#if sealStore.strategy === "shamir" || sealStore.sharesTotal > 0}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Recovery Officers</h2>
          <div class="flex gap-2">
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showRecoveryClaimPanel = !showRecoveryClaimPanel; recoveryClaimError = ""; }}
              data-testid="toggle-recovery-claim-btn"
            >
              {showRecoveryClaimPanel ? "Cancel Claim" : "Claim Share"}
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showRecoveryOfficersPanel = !showRecoveryOfficersPanel; recoveryRegError = ""; recoveryRegCertPem = ""; }}
              data-testid="toggle-register-recovery-officer-btn"
            >
              <UserPlus class="h-4 w-4" />
              {showRecoveryOfficersPanel ? "Cancel" : "Register Recovery Officer"}
            </button>
          </div>
        </div>

        <p class="text-base-content/70 text-sm">
          Recovery Officers hold shares of a distinct recovery key set, separate from the unseal
          officer set above. Register recovery officers here, then have each officer sign a
          challenge nonce out of band and complete their claim below.
        </p>

        <!-- Register new recovery officer form -->
        {#if showRecoveryOfficersPanel}
          <div class="mt-4 space-y-3 border-t border-base-300 pt-4">
            <h3 class="font-semibold text-sm">Register New Recovery Officer</h3>

            <div class="form-control">
              <label class="label" for="ro-username">
                <span class="label-text text-xs">Username</span>
              </label>
              <input
                id="ro-username"
                type="text"
                class="input input-bordered input-sm"
                placeholder="recovery-officer-alice"
                bind:value={recoveryRegUsername}
                disabled={recoveryRegLoading}
                data-testid="ro-username-input"
              />
            </div>

            <div class="form-control">
              <label class="label" for="ro-csr">
                <span class="label-text text-xs">CSR PEM (officer's public key, generated offline)</span>
              </label>
              <textarea
                id="ro-csr"
                class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
                rows={5}
                placeholder={"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----"}
                bind:value={recoveryRegCsrPem}
                disabled={recoveryRegLoading}
                data-testid="ro-csr-input"
              ></textarea>
            </div>

            {#if recoveryRegError}
              <div role="alert" class="alert alert-error py-2 text-xs" data-testid="ro-reg-error">
                <AlertCircle class="h-3 w-3 shrink-0" />
                <span>{recoveryRegError}</span>
              </div>
            {/if}

            <!-- Returned certificate after successful registration -->
            {#if recoveryRegCertPem}
              <div class="form-control">
                <div class="flex items-center justify-between mb-1">
                  <span class="label-text text-xs font-semibold text-success">Signed Certificate PEM</span>
                  <div class="flex gap-1">
                    <button
                      class="btn btn-ghost btn-xs gap-1"
                      onclick={copyRecoveryRegCert}
                      aria-label="Copy certificate PEM"
                      data-testid="ro-copy-cert-btn"
                    >
                      <Copy class="h-3 w-3" />
                      {recoveryRegCertCopied ? "Copied" : "Copy"}
                    </button>
                    <button
                      class="btn btn-ghost btn-xs gap-1"
                      onclick={downloadRecoveryRegCert}
                      aria-label="Download certificate PEM"
                      data-testid="ro-download-cert-btn"
                    >
                      <Download class="h-3 w-3" />
                      Download
                    </button>
                  </div>
                </div>
                <textarea
                  class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
                  rows={4}
                  readonly
                  value={recoveryRegCertPem}
                  data-testid="ro-reg-cert-output"
                ></textarea>
                <p class="text-base-content/50 mt-1 text-xs">
                  Transmit this certificate to the officer out of band. They will use it to authenticate and claim their recovery share.
                </p>
              </div>
            {/if}

            <button
              class="btn btn-primary btn-sm"
              onclick={() => void handleRegisterRecoveryOfficer()}
              disabled={recoveryRegLoading || recoveryRegUsername.trim() === "" || recoveryRegCsrPem.trim() === ""}
              data-testid="ro-register-btn"
            >
              {#if recoveryRegLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Register
            </button>
          </div>
        {/if}

        <!-- Claim challenge-response tester -->
        {#if showRecoveryClaimPanel}
          <div class="mt-4 space-y-3 border-t border-base-300 pt-4">
            <h3 class="font-semibold text-sm">Claim Recovery Share</h3>

            <div class="form-control">
              <label class="label" for="ro-claim-username">
                <span class="label-text text-xs">Username</span>
              </label>
              <input
                id="ro-claim-username"
                type="text"
                class="input input-bordered input-sm"
                placeholder="recovery-officer-alice"
                bind:value={recoveryClaimUsername}
                disabled={recoveryClaimBeginLoading || recoveryClaimCompleteLoading}
                data-testid="ro-claim-username-input"
              />
            </div>

            <button
              class="btn btn-outline btn-sm"
              onclick={() => void handleRecoveryClaimBegin()}
              disabled={recoveryClaimBeginLoading || recoveryClaimUsername.trim() === ""}
              data-testid="ro-claim-begin-btn"
            >
              {#if recoveryClaimBeginLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Begin Claim
            </button>

            {#if recoveryClaimNonceHex !== ""}
              <div class="bg-base-300 rounded p-3 space-y-2">
                <div>
                  <span class="label-text text-xs">Challenge Nonce (hex)</span>
                  <div class="font-mono text-xs break-all">{recoveryClaimNonceHex}</div>
                </div>
                {#if recoveryClaimExpiresAt}
                  <div class="text-xs text-base-content/50">Expires: {recoveryClaimExpiresAt}</div>
                {/if}
                <p class="text-xs text-base-content/60">
                  The officer signs the raw nonce bytes with their private key, out of band. Paste the
                  base64-encoded DER signature below to complete the claim.
                </p>
                <div class="form-control">
                  <label class="label" for="ro-claim-signature">
                    <span class="label-text text-xs">Signature (base64 DER)</span>
                  </label>
                  <input
                    id="ro-claim-signature"
                    type="text"
                    class="input input-bordered input-sm font-mono text-xs"
                    placeholder="base64 signature"
                    bind:value={recoveryClaimSignature}
                    disabled={recoveryClaimCompleteLoading}
                    data-testid="ro-claim-signature-input"
                  />
                </div>
                <button
                  class="btn btn-primary btn-sm"
                  onclick={() => void handleRecoveryClaimComplete()}
                  disabled={recoveryClaimCompleteLoading || recoveryClaimSignature.trim() === ""}
                  data-testid="ro-claim-complete-btn"
                >
                  {#if recoveryClaimCompleteLoading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {/if}
                  Complete Claim
                </button>
              </div>
            {/if}

            {#if recoveryClaimError}
              <div role="alert" class="alert alert-error py-2 text-xs" data-testid="ro-claim-error">
                <AlertCircle class="h-3 w-3 shrink-0" />
                <span>{recoveryClaimError}</span>
              </div>
            {/if}

            <!-- Returned share bytes after a successful claim -->
            {#if recoveryClaimShareBytes !== ""}
              <div class="form-control">
                <div class="flex items-center justify-between mb-1">
                  <span class="label-text text-xs font-semibold text-success">
                    Share #{recoveryClaimShareIndex} (returned exactly once)
                  </span>
                  <button
                    class="btn btn-ghost btn-xs gap-1"
                    onclick={copyRecoveryClaimShare}
                    aria-label="Copy share bytes"
                    data-testid="ro-copy-share-btn"
                  >
                    <Copy class="h-3 w-3" />
                    {recoveryClaimShareCopied ? "Copied" : "Copy"}
                  </button>
                </div>
                <textarea
                  class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
                  rows={3}
                  readonly
                  value={recoveryClaimShareBytes}
                  data-testid="ro-claim-share-output"
                ></textarea>
                <p class="text-base-content/50 mt-1 text-xs">
                  Store this share securely out of band. The server enclave holding it is destroyed
                  immediately after this response — it cannot be retrieved again.
                </p>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Reconfigure server -->
  {#if onReconfigure}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Server Configuration</h2>
        <p class="text-base-content/70 text-sm">
          Re-run the server setup wizard to change cluster topology, discovery
          method, or other server-level settings.
        </p>
        <button
          class="btn btn-outline btn-sm mt-2"
          onclick={onReconfigure}
        >
          <RefreshCw class="h-4 w-4" />
          Reconfigure Server
        </button>
      </div>
    </div>
  {/if}
</div>
