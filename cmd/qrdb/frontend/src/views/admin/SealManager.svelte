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
  import type { Client, UnsealProgress, SealMode, OfficerStatus, EnableEncryptionRequest, HolderRole, ClusterSeal } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { AlertTriangle, Info, RefreshCw, Lock, UserPlus, CheckCircle, Clock, AlertCircle, Copy, Download } from "lucide-svelte";
  import { sealStore, updateSealStatus, updateOfficers, setOfficersLoading } from "$stores/seal.svelte.ts";
  import { showToast } from "$stores/toast.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import HolderRekeyPanel from "../../components/HolderRekeyPanel.svelte";
  import HolderEnrolmentForms from "../../components/wizard/HolderEnrolmentForms.svelte";

  let { client, onReconfigure }: {
    client: Client;
    onReconfigure?: () => void;
  } = $props();

  let passphraseInput: string = $state("");
  let unsealLoading: boolean = $state(false);
  let sealLoading: boolean = $state(false);
  let statusLoading: boolean = $state(true);
  let showSealConfirm: boolean = $state(false);
  // The node barrier's cluster seal state; null from a server that reports none.
  let clusterSeal: ClusterSeal | null = $state(null);
  let clusterUnsealLoading: boolean = $state(false);

  let pollTimer: ReturnType<typeof setInterval> | null = null;

  // ---------------------------------------------------------------------------
  // Share-holder administration state
  //
  // Holders — Security Officers and key custodians — are enrolled, listed,
  // rekeyed and revoked here. Claiming an envelope and submitting a share need
  // the holder's own keys and happen in the qrdb CLI or the desktop app.
  // ---------------------------------------------------------------------------

  let showOfficersPanel: boolean = $state(false);

  // Register form state
  let officerRegUsername: string = $state("");
  let officerRegRole: HolderRole = $state("so");
  let officerRegCsrPem: string = $state("");
  let officerRegLoading: boolean = $state(false);
  let officerRegError: string = $state("");

  /** Result of the last successful registration: signed cert PEM. */
  let officerRegCertPem: string = $state("");
  let officerRegCertCopied: boolean = $state(false);

  // Rekey / revocation state. Opening a round needs RBAC and the SO PIN; a
  // quorum of current holders completes it from the CLI or the desktop app.
  let showRekeyPanel: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Recovery holders panel state
  //
  // Recovery holders hold shares of a distinct recovery key, separate from the
  // unseal holders above. They are enrolled here; they claim and submit from
  // the CLI or the desktop app.
  // ---------------------------------------------------------------------------

  let showRecoveryOfficersPanel: boolean = $state(false);

  // Register form state
  let recoveryRegUsername: string = $state("");
  let recoveryRegRole: HolderRole = $state("so");
  let recoveryRegCsrPem: string = $state("");
  let recoveryRegLoading: boolean = $state(false);
  let recoveryRegError: string = $state("");

  /** Result of the last successful registration: signed cert PEM. */
  let recoveryRegCertPem: string = $state("");
  let recoveryRegCertCopied: boolean = $state(false);

  // Re-initialization state
  let showReinit: boolean = $state(false);
  let reinitMode = $state<SealMode>("passphrase");
  let reinitPassphrase: string = $state("");
  let reinitPassphraseConfirm: string = $state("");
  let reinitShamirThreshold: number = $state(3);
  let reinitShamirShares: number = $state(5);
  /** Unseal holders enrolled for a Shamir configuration, bound from the forms. */
  let reinitHoldersRegistered: number = $state(0);
  /**
   * Whether the configuration can be applied: Shamir initialization splits
   * the unseal key to exactly the enrolled holders, so every one must be
   * enrolled first.
   */
  let reinitReady = $derived(reinitMode !== "shamir" || reinitHoldersRegistered === reinitShamirShares);
  let reinitLoading: boolean = $state(false);
  let reinitConfirm: boolean = $state(false);

  /**
   * Whether the active barrier uses Shamir secret sharing. A Shamir barrier
   * unseals and seals through a quorum of its holders, not from this page.
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
      clusterSeal = status.clusterSeal ?? null;
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

  /** Unseals a passphrase-held barrier. */
  async function handleSubmitPassphrase(): Promise<void> {
    const secret = passphraseInput;
    if (secret === "") {
      showToast("warning", "The passphrase is required");
      return;
    }

    try {
      unsealLoading = true;
      const progress: UnsealProgress = await client.seal.unseal(secret);
      passphraseInput = "";
      if (!progress.sealed) {
        showToast("success", "Server unsealed successfully");
      }
      await fetchSealStatus();
    } catch (err: unknown) {
      const raw = err instanceof Error ? err.message : "Failed to unseal";
      // A wrong passphrase surfaces as the server's 403 decryption-failure
      // signature. Show a clean message rather than leaking the raw transport
      // string; preserve the raw message for genuine transport failures.
      showToast("error", isCredentialFailure(raw) ? "Incorrect passphrase. Please try again." : raw);
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
   * Clears the node barrier's cluster seal record. Each node unseals with its
   * own credential first; one clear then lets every held node serve again.
   */
  async function handleClusterUnseal(): Promise<void> {
    try {
      clusterUnsealLoading = true;
      clusterSeal = await client.seal.clusterUnseal();
      showToast("success", "Cluster seal cleared");
      await fetchSealStatus();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to clear the cluster seal";
      showToast("error", msg);
    } finally {
      clusterUnsealLoading = false;
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
        // The unseal key is split to the enrolled unseal holders; their
        // number must equal the share count.
        await client.seal.initializeShamir({
          threshold: reinitShamirThreshold,
          totalShares: reinitShamirShares,
        });
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
  // Share-holder actions
  // ---------------------------------------------------------------------------

  /** Fetches the unseal holders and updates the store. */
  async function fetchOfficers(): Promise<void> {
    setOfficersLoading(true);
    try {
      const officers: OfficerStatus[] = await client.officers.listOfficers();
      updateOfficers(officers);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch share holders";
      showToast("error", msg);
    } finally {
      setOfficersLoading(false);
    }
  }

  /** Enrols a new unseal holder. */
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
      const resp = await client.officers.registerOfficer({ username, role: officerRegRole, csrPem });
      officerRegCertPem = resp.certPem;
      officerRegUsername = "";
      officerRegCsrPem = "";
      showToast("success", `Holder ${username} enrolled as ${resp.role}`);
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
      const resp = await client.officers.registerRecoveryOfficer({ username, role: recoveryRegRole, csrPem });
      recoveryRegCertPem = resp.certPem;
      recoveryRegUsername = "";
      recoveryRegCsrPem = "";
      showToast("success", `Recovery holder ${username} enrolled as ${resp.role}`);
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
      <div class="stat-title">Threshold</div>
      <div class="stat-value text-xl font-mono">{sealStore.sharesRequired}</div>
      <div class="stat-desc">Holders needed to unseal (M)</div>
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
        {#if isShamir}
          <!-- A Shamir barrier opens through a quorum of its holders. Their
               keys never enter this page. -->
          <p class="text-base-content/70 text-sm" data-testid="unseal-quorum-note">
            This barrier is held by its share holders: {sealStore.sharesRequired}
            of {sealStore.sharesTotal} must submit. Each holder submits from the
            qrdb CLI (<span class="font-mono">qrdb barrier quorum submit --op unseal</span>)
            or the desktop app; this page updates when the quorum is met.
          </p>
        {:else}
          <p class="text-base-content/70 text-sm">
            Enter the barrier passphrase to unseal the encryption barrier.
          </p>
          <div class="form-control mt-3 gap-5">
            <input
              type="password"
              class="input input-bordered input-sm"
              placeholder="Enter passphrase"
              bind:value={passphraseInput}
              onkeydown={(e: KeyboardEvent) => {
                if (e.key === "Enter") void handleSubmitPassphrase();
              }}
            />
            <button
              class="btn btn-primary btn-sm"
              onclick={() => void handleSubmitPassphrase()}
              disabled={unsealLoading || passphraseInput === ""}
            >
              {#if unsealLoading}
                <span class="loading loading-spinner loading-xs"></span>
              {/if}
              Unseal
            </button>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Seal section (only when unsealed and encryption is configured) -->
  {#if !sealStore.sealed && sealStore.initialized && encryptionConfigured}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <h2 class="card-title text-lg">Seal</h2>
        {#if isShamir}
          <!-- A single admin cannot seal a Shamir barrier: the server refuses
               it. A quorum of holders seals it from the CLI or the desktop app. -->
          <p class="text-base-content/70 text-sm" data-testid="seal-quorum-note">
            Sealing a Shamir barrier needs a quorum of its share holders:
            {sealStore.sharesRequired} of {sealStore.sharesTotal} must submit
            (<span class="font-mono">qrdb barrier quorum submit --op seal</span>,
            or the desktop app).
          </p>
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

        {#if clusterSeal?.active}
          <div class="divider my-2"></div>
          <div data-testid="cluster-seal-status">
            <p class="text-sm">
              Cluster seal: record {clusterSeal.sealed ? "sealed" : "clear"}
              (epoch {clusterSeal.epoch}); this node is {clusterSeal.local ?? "unknown"}.
            </p>
            {#if clusterSeal.sealed}
              <div class="alert alert-info mt-2">
                <Info class="h-5 w-5 shrink-0" />
                <span>
                  The node barrier is sealed cluster-wide. Unseal each node with its own
                  credential, then clear the seal once from any unsealed node.
                </span>
              </div>
              <button
                class="btn btn-primary btn-sm mt-3"
                data-testid="cluster-unseal-btn"
                onclick={() => void handleClusterUnseal()}
                disabled={clusterUnsealLoading}
              >
                {#if clusterUnsealLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                Clear Cluster Seal
              </button>
            {/if}
          </div>
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
      <div class="border-t border-base-300 pt-3" data-testid="shamir-holder-enrolment">
        <p class="text-base-content/70 mb-3 text-sm">
          Enrol the {reinitShamirShares} share holders the unseal key is split to. Each
          claims its envelope afterwards from the qrdb CLI or the desktop app.
        </p>
        <HolderEnrolmentForms {client} count={reinitShamirShares} bind:registeredCount={reinitHoldersRegistered} />
      </div>
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
                disabled={reinitLoading || !reinitReady}
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
                  disabled={reinitLoading || !reinitReady}
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

  <!-- Share holders panel (Shamir barriers only) -->
  {#if sealStore.strategy === "shamir" || sealStore.sharesTotal > 0}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Share Holders</h2>
          <div class="flex gap-2">
            <button
              class="btn btn-ghost btn-sm gap-1"
              onclick={() => void fetchOfficers()}
              disabled={sealStore.officersLoading}
              aria-label="Refresh share holders"
              data-testid="refresh-officers-btn"
            >
              <RefreshCw class={`h-4 w-4 ${sealStore.officersLoading ? 'animate-spin' : ''}`} />
              Refresh
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showRekeyPanel = !showRekeyPanel; }}
              data-testid="toggle-rekey-btn"
            >
              {showRekeyPanel ? "Cancel" : "Rekey / Revoke"}
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showOfficersPanel = !showOfficersPanel; officerRegError = ""; officerRegCertPem = ""; }}
              data-testid="toggle-register-officer-btn"
            >
              <UserPlus class="h-4 w-4" />
              {showOfficersPanel ? "Cancel" : "Enrol Holder"}
            </button>
          </div>
        </div>

        <p class="text-base-content/70 text-sm">
          Share holders — Security Officers and key custodians — each hold one
          share of the unseal key. Enrol holders here; each claims its sealed
          envelope and submits to quorum rounds from the qrdb CLI or the
          desktop app, with keys that never enter this page.
        </p>

        <!-- Officer claim status table -->
        {#if sealStore.officersLoading}
          <div class="flex justify-center py-4">
            <span class="loading loading-spinner loading-sm text-primary" data-testid="officers-loading"></span>
          </div>
        {:else if sealStore.officers.length === 0}
          <div class="bg-base-300 rounded-lg p-4 text-center text-sm text-base-content/60" data-testid="no-officers-msg">
            No share holders enrolled.
          </div>
        {:else}
          <div class="overflow-x-auto">
            <table class="table table-sm" data-testid="seal-manager-officers-table">
              <thead>
                <tr>
                  <th>Holder</th>
                  <th>Role</th>
                  <th>Share #</th>
                  <th>Factors</th>
                  <th>Enrolled</th>
                  <th>Claim Status</th>
                </tr>
              </thead>
              <tbody>
                {#each sealStore.officers as officer (officer.username)}
                  <tr>
                    <td class="font-mono text-sm">{officer.username}</td>
                    <td class="text-xs">{officer.role}</td>
                    <td class="text-center">{officer.shareIndex}</td>
                    <td class="text-xs">
                      {(officer.factors ?? []).join(", ")}{officer.designated ? ` (designated: ${officer.designated})` : ""}
                    </td>
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
            <h3 class="font-semibold text-sm">Enrol a Holder</h3>

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
              <label class="label" for="so-role">
                <span class="label-text text-xs">Role</span>
              </label>
              <select
                id="so-role"
                class="select select-bordered select-sm"
                bind:value={officerRegRole}
                disabled={officerRegLoading}
                data-testid="so-role-select"
              >
                <option value="so">Security Officer</option>
                <option value="custodian">Key custodian</option>
              </select>
            </div>

            <div class="form-control">
              <label class="label" for="so-csr">
                <span class="label-text text-xs">CSR PEM (the holder's public key, generated offline)</span>
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
                  Give this certificate to the holder out of band. The holder uses it to claim its envelope from the qrdb CLI or the desktop app.
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

        <!-- Rekey / revoke -->
        {#if showRekeyPanel}
          <div class="mt-4 border-t border-base-300 pt-4">
            <HolderRekeyPanel
              systemScope
              submitCommand="qrdb barrier quorum submit --op rekey"
              onStartRekey={(r) => client.officers.startRekey({
                domain: r.domain,
                threshold: r.threshold,
                policy: r.policy,
                ...(r.soPin === "" ? {} : { soPin: r.soPin }),
              })}
              onRevoke={(r) => client.officers.revokeOfficer({
                domain: r.domain,
                username: r.username,
                ...(r.threshold > 0 ? { threshold: r.threshold } : {}),
                ...(r.soPin === "" ? {} : { soPin: r.soPin }),
              })}
            />
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Recovery holders panel (Shamir barriers only) -->
  {#if sealStore.strategy === "shamir" || sealStore.sharesTotal > 0}
    <div class="card bg-base-200 shadow">
      <div class="card-body">
        <div class="flex items-center justify-between">
          <h2 class="card-title text-lg">Recovery Holders</h2>
          <div class="flex gap-2">
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => { showRecoveryOfficersPanel = !showRecoveryOfficersPanel; recoveryRegError = ""; recoveryRegCertPem = ""; }}
              data-testid="toggle-register-recovery-officer-btn"
            >
              <UserPlus class="h-4 w-4" />
              {showRecoveryOfficersPanel ? "Cancel" : "Enrol Recovery Holder"}
            </button>
          </div>
        </div>

        <p class="text-base-content/70 text-sm">
          Recovery holders hold shares of a distinct recovery key, separate from the unseal
          holders above. Enrol them here; each claims its envelope from the qrdb CLI or the
          desktop app.
        </p>

        <!-- Register new recovery officer form -->
        {#if showRecoveryOfficersPanel}
          <div class="mt-4 space-y-3 border-t border-base-300 pt-4">
            <h3 class="font-semibold text-sm">Enrol a Recovery Holder</h3>

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
              <label class="label" for="ro-role">
                <span class="label-text text-xs">Role</span>
              </label>
              <select
                id="ro-role"
                class="select select-bordered select-sm"
                bind:value={recoveryRegRole}
                disabled={recoveryRegLoading}
                data-testid="ro-role-select"
              >
                <option value="so">Security Officer</option>
                <option value="custodian">Key custodian</option>
              </select>
            </div>

            <div class="form-control">
              <label class="label" for="ro-csr">
                <span class="label-text text-xs">CSR PEM (the holder's public key, generated offline)</span>
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
                  Give this certificate to the holder out of band. The holder uses it to claim its recovery envelope from the qrdb CLI or the desktop app.
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
