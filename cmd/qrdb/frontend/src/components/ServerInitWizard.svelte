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
  import type { Client, CeremonyHolderCertificate, CeremonyInitializeRequest, CeremonyOfficerRequest, OfficerStatus } from "@qrdb/sdk";
  import { ShieldCheck, AlertCircle, AlertTriangle, RefreshCw, CheckCircle, Clock, Copy, Download } from "lucide-svelte";
  import StepIndicator from "./wizard/StepIndicator.svelte";
  import StepGlobalBarrier from "./wizard/setup/StepGlobalBarrier.svelte";
  import StepAdminCredential from "./wizard/setup/StepAdminCredential.svelte";
  import type { AdminCredentialMode } from "./wizard/setup/StepAdminCredential.svelte";
  import CeremonyOfficerForms from "./wizard/CeremonyOfficerForms.svelte";
  import { setupWizardStore } from "$stores/setup-wizard.svelte.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    client,
    onComplete,
  }: {
    client: Client;
    onComplete: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // Wizard state
  // ---------------------------------------------------------------------------

  // "officers" only appears for shamir mode. "admin" always appears: the
  // same ceremony.initialize call that brings up the barrier also issues
  // the bootstrap admin's certificate (go-qrdb#4578 review item 2).
  type InitStep = "barrier" | "officers" | "admin" | "applying" | "complete";

  const allStepLabels: Readonly<Record<InitStep, string>> = {
    barrier: "Barrier",
    officers: "Officers",
    admin: "Admin",
    applying: "Apply",
    complete: "Done",
  } as const;

  // Step sequences differ by barrier mode.
  const shamirStepOrder: readonly InitStep[] = ["barrier", "officers", "admin", "applying", "complete"] as const;
  const simpleStepOrder: readonly InitStep[] = ["barrier", "admin", "applying", "complete"] as const;

  let currentStep: InitStep = $state("barrier");
  let applyError: string = $state("");

  // Derive the active step sequence based on the chosen barrier mode.
  let stepOrder = $derived(
    setupWizardStore.barrierConfig.mode === "shamir" ? shamirStepOrder : simpleStepOrder,
  );
  let currentIndex = $derived(stepOrder.indexOf(currentStep));
  let indicatorLabels = $derived(stepOrder.map((s) => allStepLabels[s]));

  // ceremony.initialize's result (share-free, admin-key-free -- see below).
  let shareCount: number = $state(0);
  let issuedAdminCertPem: string = $state("");
  let adminKeyPath: string = $state("");
  let adminCredentialSource: string = $state("");
  let issuedHolders: readonly CeremonyHolderCertificate[] = $state([]);

  // Holder claim status (polled after shamir init).
  let officerStatuses: OfficerStatus[] = $state([]);
  let claimStatusLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Holder-collection step state
  // ---------------------------------------------------------------------------

  /** Every filled-in holder, bound from CeremonyOfficerForms. Collected
   *  locally: `qrdb init` is the ONLY system bootstrap, so no holder can
   *  be registered before this wizard's single ceremony.initialize call. */
  let officers: CeremonyOfficerRequest[] = $state([]);

  /** Holders needed: the Shamir share count (N). */
  let totalOfficers = $derived(setupWizardStore.barrierConfig.shamirShares);

  /** Whether every holder form is filled in, gating the Officers step's Next. */
  let allOfficersFilled = $derived(officers.length === totalOfficers && totalOfficers > 0);

  // ---------------------------------------------------------------------------
  // Admin-credential step state
  // ---------------------------------------------------------------------------

  let adminMode: AdminCredentialMode = $state("csr");
  let adminCsrPem: string = $state("");
  let adminCertPem: string = $state("");
  let adminCertPoolPem: string = $state("");

  // ---------------------------------------------------------------------------
  // PIN state (collected on the admin step)
  // ---------------------------------------------------------------------------

  /** Minimum PIN length, mirroring the server's pkg/pin minPINLength. */
  const MIN_PIN_LENGTH = 6;

  // ceremony.initialize requires both PINs: the server seals the Security
  // Officer PIN and the user PIN at initialization, exactly as `qrdb init`
  // prompts for them.
  let soPin: string = $state("");
  let soPinConfirm: string = $state("");
  let userPin: string = $state("");
  let userPinConfirm: string = $state("");

  /** The first PIN validation failure, or "" when both PINs are valid. */
  function pinError(label: string, value: string, confirm: string): string {
    if (value.length < MIN_PIN_LENGTH) return `${label} must be at least ${MIN_PIN_LENGTH} characters.`;
    if (value !== confirm) return `${label}s do not match.`;
    return "";
  }

  let soPinError = $derived(pinError("SO PIN", soPin, soPinConfirm));
  let userPinError = $derived(pinError("User PIN", userPin, userPinConfirm));
  let pinsValid = $derived(soPinError === "" && userPinError === "");

  // ---------------------------------------------------------------------------
  // Navigation
  // ---------------------------------------------------------------------------

  function handleBarrierNext(): void {
    applyError = "";
    currentStep = setupWizardStore.barrierConfig.mode === "shamir" ? "officers" : "admin";
  }

  function handleBarrierBack(): void {
    // No previous step from barrier — this is the entry point.
  }

  function handleOfficersBack(): void {
    currentStep = "barrier";
    applyError = "";
  }

  function handleOfficersNext(): void {
    if (!allOfficersFilled) return;
    applyError = "";
    currentStep = "admin";
  }

  function handleAdminBack(): void {
    currentStep = setupWizardStore.barrierConfig.mode === "shamir" ? "officers" : "barrier";
    applyError = "";
  }

  async function handleAdminNext(): Promise<void> {
    if (!pinsValid) {
      applyError = soPinError || userPinError;
      return;
    }
    applyError = "";
    currentStep = "applying";
    await initializeCeremony();
  }

  /**
   * Runs the initialization ceremony: brings up the barrier (passphrase or
   * Shamir custody over `officers`) and resolves the bootstrap admin's
   * credential, all in the one call `qrdb init` itself makes
   * (client.ceremony.initialize). Replaces the former two-call
   * seal.initialize/initializeShamir + officers.registerOfficer flow,
   * which `qrdb init` being the ONLY system bootstrap retired
   * (go-qrdb#4578 review item 2): no holder can be registered before this
   * call, so every holder is collected locally (CeremonyOfficerForms) and
   * sent together with it.
   */
  async function initializeCeremony(): Promise<void> {
    try {
      const cfg = setupWizardStore.barrierConfig;

      // CeremonyInitializeRequest's fields are readonly, so the request is
      // built in one literal from each step's own fields rather than
      // assigned onto incrementally.
      const barrierFields: Partial<CeremonyInitializeRequest> =
        cfg.mode === "passphrase" ? { secret: cfg.passphrase } :
        cfg.mode === "shamir" ? { threshold: cfg.shamirThreshold, officers } :
        {}; // mode === "none": no secret, no officers.

      const adminFields: Partial<CeremonyInitializeRequest> =
        adminMode === "csr" ? { adminCsrPem: adminCsrPem.trim() } :
        adminMode === "byoc" ? {
          adminCertPem: adminCertPem.trim(),
          ...(adminCertPoolPem.trim() !== "" ? { adminCertPoolPem: adminCertPoolPem.trim() } : {}),
        } :
        { adminGenerate: true };

      const req: CeremonyInitializeRequest = { ...barrierFields, ...adminFields, soPin, userPin };

      const result = await client.ceremony.initialize(req);
      shareCount = result.shareCount;
      issuedAdminCertPem = result.adminCertPem ?? "";
      // adminKeyPem is intentionally never read here: on the desktop
      // (Wails) client it is never populated in the first place -- the
      // generated key is saved to a local file by Go and reported only
      // through adminKeyPath, never through this page.
      adminKeyPath = result.adminKeyPath ?? "";
      adminCredentialSource = result.adminCredentialSource ?? "";
      issuedHolders = result.holders ?? [];

      if (cfg.mode === "shamir") {
        await pollClaimStatus();
      }

      currentStep = "complete";
    } catch (err: unknown) {
      applyError = err instanceof Error ? err.message : "Failed to initialize the server";
      currentStep = "admin";
    }
  }

  async function refreshClaimStatus(): Promise<void> {
    claimStatusLoading = true;
    try {
      officerStatuses = await client.officers.listOfficers();
    } catch {
      // Non-fatal: status polling failure should not block wizard progress.
    } finally {
      claimStatusLoading = false;
    }
  }

  /**
   * Polls officer claim status with a bounded retry. After Shamir
   * initialization the server tears down its sealed-window listener and
   * reboots into full mode (auto-unseal), so the first requests can fail
   * with a connection error. Retries until the status list is populated
   * or the deadline elapses; never throws.
   */
  async function pollClaimStatus(): Promise<void> {
    const deadline = Date.now() + 20_000;
    claimStatusLoading = true;
    try {
      for (;;) {
        try {
          const statuses = await client.officers.listOfficers();
          if (statuses.length > 0) {
            officerStatuses = statuses;
            return;
          }
        } catch {
          // Server mid-reboot: fall through to the retry delay.
        }
        if (Date.now() >= deadline) return;
        await new Promise((resolve) => setTimeout(resolve, 750));
      }
    } finally {
      claimStatusLoading = false;
    }
  }

  function copyText(text: string): void {
    void navigator.clipboard.writeText(text);
  }

  function downloadText(text: string, filename: string): void {
    const blob = new Blob([text], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }

</script>

<div class="flex min-h-screen flex-col items-center justify-center px-4 py-8">
  <div class="w-full max-w-3xl">
    <!-- Header -->
    <div class="mb-8 text-center">
      <div class="flex items-center justify-center gap-3 mb-2">
        <ShieldCheck class="text-primary h-8 w-8" strokeWidth={2} />
        <h1 class="text-3xl font-bold">Server Initialization</h1>
      </div>
      <p class="text-base-content/60 mt-1 text-sm">
        Configure encryption for your newly connected QRDB server.
      </p>
    </div>

    <!-- Step indicator -->
    {#if currentStep !== "complete"}
      <div class="mb-8">
        <StepIndicator steps={indicatorLabels} currentIndex={currentIndex} />
      </div>
    {/if}

    <!-- Step content -->
    <div class="card bg-base-100 shadow-xl">
      <div class="card-body">

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: barrier                                                       -->
        <!-- ------------------------------------------------------------------ -->
        {#if currentStep === "barrier"}
          {#if applyError}
            <div role="alert" class="alert alert-error mb-4 text-sm">
              <AlertCircle class="h-4 w-4 shrink-0" />
              <span>{applyError}</span>
            </div>
          {/if}
          <StepGlobalBarrier onNext={handleBarrierNext} onBack={handleBarrierBack} />

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: officers (shamir only)                                        -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "officers"}
          <div class="flex flex-col gap-6">
            <div class="text-center">
              <h2 class="text-2xl font-bold">Enrol Share Holders</h2>
              <p class="text-base-content/70 mt-2 text-sm">
                Each holder — a Security Officer or a key custodian — provides a
                CSR generated offline. The server's CA issues the holder
                certificate once initialization runs (next step), which you
                hand back to the holder out of band. No share exists yet:
                initialization seals one to each holder, who claims it with
                its own keys from the qrdb CLI or the desktop app.
              </p>
            </div>

            <CeremonyOfficerForms count={totalOfficers} bind:officers />

            <div class="flex justify-between mt-2">
              <button class="btn btn-ghost" onclick={handleOfficersBack} data-testid="officers-back-btn">
                Back
              </button>
              <button
                class="btn btn-primary"
                onclick={handleOfficersNext}
                disabled={!allOfficersFilled}
                data-testid="officers-next-btn"
              >
                Next
                {#if !allOfficersFilled}
                  <span class="text-xs opacity-70">({officers.length}/{totalOfficers})</span>
                {/if}
              </button>
            </div>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: admin credential                                             -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "admin"}
          {#if applyError}
            <div role="alert" class="alert alert-error mb-4 text-sm" data-testid="admin-apply-error">
              <AlertCircle class="h-4 w-4 shrink-0" />
              <span>{applyError}</span>
            </div>
          {/if}
          <div class="flex flex-col gap-3 mb-6" data-testid="init-pins">
            <div>
              <h2 class="text-xl font-bold">PINs</h2>
              <p class="text-base-content/70 mt-1 text-sm">
                Initialization seals a Security Officer PIN and a user PIN,
                each at least {MIN_PIN_LENGTH} characters. The SO PIN
                authorizes sealing and other officer operations.
              </p>
            </div>
            <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label class="form-control">
                <span class="label-text">SO PIN</span>
                <input class="input input-bordered" type="password" autocomplete="new-password" required
                  placeholder="SO PIN" bind:value={soPin} data-testid="so-pin-input" />
              </label>
              <label class="form-control">
                <span class="label-text">Confirm SO PIN</span>
                <input class="input input-bordered" type="password" autocomplete="new-password" required
                  placeholder="Confirm SO PIN" bind:value={soPinConfirm} data-testid="so-pin-confirm-input" />
              </label>
              <label class="form-control">
                <span class="label-text">User PIN</span>
                <input class="input input-bordered" type="password" autocomplete="new-password" required
                  placeholder="User PIN" bind:value={userPin} data-testid="user-pin-input" />
              </label>
              <label class="form-control">
                <span class="label-text">Confirm user PIN</span>
                <input class="input input-bordered" type="password" autocomplete="new-password" required
                  placeholder="Confirm user PIN" bind:value={userPinConfirm} data-testid="user-pin-confirm-input" />
              </label>
            </div>
            {#if (soPin !== "" || soPinConfirm !== "") && soPinError}
              <p class="text-error text-sm" data-testid="so-pin-error">{soPinError}</p>
            {/if}
            {#if (userPin !== "" || userPinConfirm !== "") && userPinError}
              <p class="text-error text-sm" data-testid="user-pin-error">{userPinError}</p>
            {/if}
          </div>
          <StepAdminCredential
            bind:mode={adminMode}
            bind:csrPem={adminCsrPem}
            bind:certPem={adminCertPem}
            bind:certPoolPem={adminCertPoolPem}
            onNext={() => void handleAdminNext()}
            onBack={handleAdminBack}
          />

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: applying                                                      -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "applying"}
          <div class="flex flex-col items-center gap-6 py-8">
            <span class="loading loading-spinner loading-lg text-primary"></span>
            <div class="text-center">
              <h2 class="text-xl font-semibold">Initializing Server</h2>
              <p class="text-base-content/60 mt-1 text-sm">
                Applying encryption barrier configuration and issuing the
                admin certificate...
              </p>
            </div>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: complete                                                      -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "complete"}
          <div class="flex flex-col gap-5 py-2">
            <div class="flex flex-col items-center gap-2 text-center">
              <ShieldCheck class="text-success h-12 w-12" strokeWidth={1.5} />
              <h2 class="text-2xl font-bold">Server Initialized</h2>
            </div>

            <!-- Bootstrap admin certificate -->
            <div class="form-control">
              <div class="flex items-center justify-between mb-1">
                <span class="label-text font-semibold">Admin Certificate (source: {adminCredentialSource})</span>
                <div class="flex gap-1">
                  <button class="btn btn-ghost btn-xs gap-1" onclick={() => copyText(issuedAdminCertPem)} aria-label="Copy admin certificate" data-testid="copy-admin-cert-btn">
                    <Copy class="h-3 w-3" />
                    Copy
                  </button>
                  <button class="btn btn-ghost btn-xs gap-1" onclick={() => downloadText(issuedAdminCertPem, "admin-cert.pem")} aria-label="Download admin certificate" data-testid="download-admin-cert-btn">
                    <Download class="h-3 w-3" />
                    Download
                  </button>
                </div>
              </div>
              <textarea
                class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
                rows={4}
                readonly
                value={issuedAdminCertPem}
                data-testid="admin-cert-pem-issued"
              ></textarea>
            </div>

            {#if adminKeyPath}
              <div role="alert" class="alert alert-warning text-sm" data-testid="admin-key-saved-notice">
                <AlertTriangle class="h-5 w-5 shrink-0" />
                <span>
                  The generated admin private key was saved to
                  <span class="font-mono" data-testid="admin-key-path">{adminKeyPath}</span>
                  on this machine. It is returned exactly once and is never
                  shown on this page; this is dev/test/evaluation ONLY and
                  does not meet FedRAMP, HIPAA or PCI DSS 4.0.
                </span>
              </div>
            {/if}

            {#if shareCount > 0}
              <!-- -----------------------------------------------------------
                Shamir initialization: holder certificates and claim status.
                No share is ever shown here. Each holder claims its sealed
                envelope with its own keys from the qrdb CLI or the desktop app.
              ----------------------------------------------------------- -->
              <div role="alert" class="alert alert-info text-sm">
                <AlertTriangle class="h-5 w-5 shrink-0" />
                <span>
                  The barrier has been initialized with a
                  <strong>{setupWizardStore.barrierConfig.shamirThreshold}-of-{shareCount}</strong>
                  Shamir scheme. Each holder's share is sealed to that holder.
                  Every holder now claims its envelope, exactly once, over its
                  <strong>own holder certificate</strong> from the qrdb CLI
                  (<span class="font-mono">qrdb barrier officer claim</span>) or
                  the desktop app. No share is ever displayed here.
                </span>
              </div>

              {#if issuedHolders.length > 0}
                <div class="form-control">
                  <span class="label-text font-semibold mb-2">Holder Certificates</span>
                  <div class="flex flex-col gap-2">
                    {#each issuedHolders as holder (holder.username)}
                      <div class="card border border-base-300 bg-base-200">
                        <div class="card-body gap-2 p-3">
                          <div class="flex items-center justify-between">
                            <span class="font-mono text-sm">{holder.username} ({holder.role})</span>
                            <div class="flex gap-1">
                              <button class="btn btn-ghost btn-xs gap-1" onclick={() => copyText(holder.certPem)} aria-label={`Copy ${holder.username}'s certificate`} data-testid={`copy-holder-cert-${holder.username}`}>
                                <Copy class="h-3 w-3" />
                              </button>
                              <button class="btn btn-ghost btn-xs gap-1" onclick={() => downloadText(holder.certPem, `holder-${holder.username}-cert.pem`)} aria-label={`Download ${holder.username}'s certificate`} data-testid={`download-holder-cert-${holder.username}`}>
                                <Download class="h-3 w-3" />
                              </button>
                            </div>
                          </div>
                          <textarea
                            class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
                            rows={3}
                            readonly
                            value={holder.certPem}
                            data-testid={`holder-cert-${holder.username}`}
                          ></textarea>
                        </div>
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}

              <!-- Officer claim status table -->
              <div class="form-control">
                <div class="flex items-center justify-between mb-2">
                  <span class="label-text font-semibold">
                    Holder Claim Status ({officerStatuses.filter((o) => o.claimedAt).length}/{shareCount} claimed)
                  </span>
                  <button
                    class="btn btn-ghost btn-xs gap-1"
                    onclick={() => void refreshClaimStatus()}
                    disabled={claimStatusLoading}
                    aria-label="Refresh claim status"
                  >
                    <RefreshCw class={`h-3 w-3 ${claimStatusLoading ? 'animate-spin' : ''}`} />
                    Refresh
                  </button>
                </div>

                {#if officerStatuses.length === 0}
                  <div class="bg-base-200 rounded-lg p-4 text-center text-sm text-base-content/60">
                    {claimStatusLoading ? "Loading..." : "No holder status available. Ensure the holders are enrolled and refresh."}
                  </div>
                {:else}
                  <div class="overflow-x-auto">
                    <table class="table table-sm" data-testid="officer-claim-status-table">
                      <thead>
                        <tr>
                          <th>Holder</th>
                          <th>Role</th>
                          <th>Share #</th>
                          <th>Enrolled</th>
                          <th>Status</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each officerStatuses as officer (officer.shareIndex)}
                          <tr>
                            <td class="font-mono text-sm">{officer.username}</td>
                            <td class="text-xs">{officer.role}</td>
                            <td class="text-center">{officer.shareIndex}</td>
                            <td class="text-xs text-base-content/60">
                              {new Date(officer.enrolledAt).toLocaleDateString()}
                            </td>
                            <td>
                              {#if officer.claimedAt}
                                <span class="badge badge-success gap-1">
                                  <CheckCircle class="h-3 w-3" />
                                  Claimed
                                </span>
                              {:else}
                                <span class="badge badge-warning gap-1">
                                  <Clock class="h-3 w-3" />
                                  Pending
                                </span>
                              {/if}
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
              </div>

              <div class="flex flex-col gap-2 text-sm text-base-content/70">
                <p>
                  <strong>Next steps:</strong> Give each holder its issued
                  certificate and have it claim its envelope from the qrdb CLI
                  or the desktop app.
                </p>
                <p>
                  An unclaimed envelope is destroyed when its time-to-live
                  expires (24 hours by default); a holder that misses the
                  window needs a rekey.
                </p>
              </div>
            {:else}
              <p class="text-base-content/60 text-center text-sm">
                The encryption barrier has been configured successfully.
                Your server is ready to use.
              </p>
            {/if}

            <button
              class="btn btn-primary btn-block"
              onclick={onComplete}
              data-testid="open-database-btn"
            >
              Open Database
            </button>
          </div>
        {/if}

      </div>
    </div>
  </div>
</div>
