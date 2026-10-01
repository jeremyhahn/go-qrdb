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
  import type { Client, OfficerStatus, OfficerRegisterResponse } from "@qrdb/sdk";
  import { ShieldCheck, AlertCircle, AlertTriangle, RefreshCw, CheckCircle, Clock, UserPlus, Copy, Download } from "lucide-svelte";
  import StepIndicator from "./wizard/StepIndicator.svelte";
  import StepGlobalBarrier from "./wizard/setup/StepGlobalBarrier.svelte";
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

  // "officers" step only appears for shamir mode.
  type InitStep = "barrier" | "officers" | "applying" | "complete";

  const allStepLabels: Readonly<Record<InitStep, string>> = {
    barrier: "Barrier",
    officers: "Officers",
    applying: "Apply",
    complete: "Done",
  } as const;

  // Step sequences differ by barrier mode.
  const shamirStepOrder: readonly InitStep[] = ["barrier", "officers", "applying", "complete"] as const;
  const simpleStepOrder: readonly InitStep[] = ["barrier", "applying", "complete"] as const;

  let currentStep: InitStep = $state("barrier");
  let applyError: string = $state("");

  // Derive the active step sequence based on the chosen barrier mode.
  let stepOrder = $derived(
    setupWizardStore.barrierConfig.mode === "shamir" ? shamirStepOrder : simpleStepOrder,
  );
  let currentIndex = $derived(stepOrder.indexOf(currentStep));
  let indicatorLabels = $derived(stepOrder.map((s) => allStepLabels[s]));

  // Shamir init metadata (share-free). Populated only when barrier mode is shamir.
  let shamirThreshold: number = $state(0);
  let shamirShareCount: number = $state(0);
  let rootToken: string = $state("");

  // Officer claim status (polled after shamir init).
  let officerStatuses: OfficerStatus[] = $state([]);
  let claimStatusLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Officer-registration step state
  // ---------------------------------------------------------------------------

  /** Per-officer registration form state. */
  interface OfficerFormEntry {
    username: string;
    csrPem: string;
    /** null = not yet registered, OfficerRegisterResponse = success, string = error message */
    result: OfficerRegisterResponse | null;
    error: string;
    loading: boolean;
    /** Tracks whether the returned cert was copied by the admin. */
    certCopied: boolean;
  }

  let officerForms: OfficerFormEntry[] = $state([]);

  /** Rebuild the officer forms array when shamir share count changes. */
  function resetOfficerForms(count: number): void {
    officerForms = Array.from({ length: count }, () => ({
      username: "",
      csrPem: "",
      result: null,
      error: "",
      loading: false,
      certCopied: false,
    }));
  }

  /** Number of officers that have been successfully registered. */
  let registeredCount = $derived(officerForms.filter((f) => f.result !== null).length);

  /** Total officers needed (from the barrier config). */
  let totalOfficers = $derived(setupWizardStore.barrierConfig.shamirShares);

  /** Whether all required officers have been registered, gating the Initialize button. */
  let allOfficersRegistered = $derived(registeredCount === totalOfficers && totalOfficers > 0);

  // ---------------------------------------------------------------------------
  // Navigation
  // ---------------------------------------------------------------------------

  async function handleBarrierNext(): Promise<void> {
    applyError = "";
    const cfg = setupWizardStore.barrierConfig;
    if (cfg.mode === "shamir") {
      // Build empty officer forms for the shamir count before navigating.
      resetOfficerForms(cfg.shamirShares);
      currentStep = "officers";
    } else {
      currentStep = "applying";
      await applyBarrier();
    }
  }

  function handleBarrierBack(): void {
    // No previous step from barrier — this is the entry point.
  }

  function handleOfficersBack(): void {
    currentStep = "barrier";
    applyError = "";
  }

  async function handleOfficersNext(): Promise<void> {
    if (!allOfficersRegistered) return;
    applyError = "";
    currentStep = "applying";
    await applyBarrier();
  }

  async function applyBarrier(): Promise<void> {
    try {
      const cfg = setupWizardStore.barrierConfig;

      if (cfg.mode === "passphrase") {
        await client.seal.initialize(cfg.passphrase);
      } else if (cfg.mode === "shamir") {
        const result = await client.seal.initializeShamir(
          "",
          cfg.shamirThreshold,
          cfg.shamirShares,
        );
        shamirThreshold = result.threshold;
        shamirShareCount = result.shareCount;
        rootToken = result.rootToken;
        await pollClaimStatus();
      }
      // mode === "none": no barrier initialization needed.

      currentStep = "complete";
    } catch (err: unknown) {
      applyError = err instanceof Error ? err.message : "Failed to initialize barrier";
      // Return to officers step for shamir (so they can retry), barrier for others.
      currentStep = setupWizardStore.barrierConfig.mode === "shamir" ? "officers" : "barrier";
    }
  }

  async function refreshClaimStatus(): Promise<void> {
    claimStatusLoading = true;
    try {
      officerStatuses = await client.officers.officerClaimStatus();
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
          const statuses = await client.officers.officerClaimStatus();
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

  // ---------------------------------------------------------------------------
  // Per-officer registration
  // ---------------------------------------------------------------------------

  /**
   * The sealed-window listener answers claim status anonymously, so it omits
   * officer usernames. Name each row from this wizard's own registrations by
   * share index, and fall back to the index alone.
   */
  function officerLabel(officer: OfficerStatus): string {
    if (officer.username) return officer.username;
    const registered = officerForms.find((f) => f.result?.shareIndex === officer.shareIndex);
    return registered?.username || `Officer #${officer.shareIndex}`;
  }

  async function registerOfficer(index: number): Promise<void> {
    const form = officerForms[index];
    if (form === undefined) return;

    const username = form.username.trim();
    const csrPem = form.csrPem.trim();

    if (username === "") {
      officerForms[index] = { ...form, error: "Username is required." };
      return;
    }
    if (csrPem === "") {
      officerForms[index] = { ...form, error: "CSR PEM is required." };
      return;
    }

    officerForms[index] = { ...form, loading: true, error: "" };
    try {
      const resp = await client.officers.registerOfficer({ username, csrPem });
      officerForms[index] = { ...officerForms[index], loading: false, result: resp, error: "" };
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Registration failed";
      officerForms[index] = { ...officerForms[index], loading: false, error: msg };
    }
  }

  function copyCert(index: number): void {
    const form = officerForms[index];
    if (form === undefined || form.result === null || form.result === undefined) return;
    const certPem = form.result.certPem;
    void navigator.clipboard.writeText(certPem).then(() => {
      const current = officerForms[index];
      if (current !== undefined) {
        officerForms[index] = {
          username: current.username,
          csrPem: current.csrPem,
          result: current.result,
          error: current.error,
          loading: current.loading,
          certCopied: true,
        };
      }
    });
  }

  function downloadCert(index: number): void {
    const form = officerForms[index];
    if (form === undefined || form.result === null || form.result === undefined) return;
    const blob = new Blob([form.result.certPem], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `officer-${form.username}-cert.pem`;
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
              <h2 class="text-2xl font-bold">Register Security Officers</h2>
              <p class="text-base-content/70 mt-2 text-sm">
                Each Security Officer must provide their CSR PEM (generated
                offline). The server signs it and returns a certificate. The admin
                must hand the certificate back to the officer out of band.
                Share bytes are never generated or shown here — they are issued
                after initialization when each officer claims their share.
              </p>
            </div>

            {#if applyError}
              <div role="alert" class="alert alert-error text-sm" data-testid="officer-apply-error">
                <AlertCircle class="h-4 w-4 shrink-0" />
                <span>{applyError}</span>
              </div>
            {/if}

            <!-- Progress indicator -->
            <div class="flex items-center justify-between text-sm">
              <span class="font-semibold">
                Registered Officers
              </span>
              <span
                class="badge"
                class:badge-success={allOfficersRegistered}
                class:badge-warning={!allOfficersRegistered}
                data-testid="officer-registration-count"
              >
                {registeredCount} / {totalOfficers}
              </span>
            </div>

            <!-- Per-officer forms -->
            {#each officerForms as form, i (i)}
              <div
                class="card border bg-base-200"
                class:border-success={form.result !== null}
                class:border-base-300={form.result === null}
                data-testid={`officer-form-${i}`}
              >
                <div class="card-body gap-3 p-4">
                  <div class="flex items-center gap-2">
                    {#if form.result !== null}
                      <CheckCircle class="text-success h-5 w-5 shrink-0" />
                    {:else}
                      <UserPlus class="text-base-content/50 h-5 w-5 shrink-0" />
                    {/if}
                    <h3 class="font-semibold text-sm">
                      Officer {i + 1}
                      {#if form.result !== null}
                        <span class="text-base-content/60 font-normal">
                          — {form.username} (share #{form.result.shareIndex})
                        </span>
                      {/if}
                    </h3>
                  </div>

                  {#if form.result === null}
                    <!-- Registration form -->
                    <div class="form-control">
                      <label class="label" for={`officer-username-${i}`}>
                        <span class="label-text text-xs">Username</span>
                      </label>
                      <input
                        id={`officer-username-${i}`}
                        type="text"
                        class="input input-bordered input-sm"
                        placeholder="officer-alice"
                        value={form.username}
                        oninput={(e: Event) => { if (officerForms[i]) officerForms[i].username = (e.currentTarget as HTMLInputElement).value; }}
                        disabled={form.loading}
                        data-testid={`officer-username-${i}`}
                      />
                    </div>

                    <div class="form-control">
                      <label class="label" for={`officer-csr-${i}`}>
                        <span class="label-text text-xs">CSR PEM</span>
                      </label>
                      <textarea
                        id={`officer-csr-${i}`}
                        class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
                        rows={5}
                        placeholder={"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----"}
                        value={form.csrPem}
                        oninput={(e: Event) => { if (officerForms[i]) officerForms[i].csrPem = (e.currentTarget as HTMLTextAreaElement).value; }}
                        disabled={form.loading}
                        data-testid={`officer-csr-${i}`}
                      ></textarea>
                    </div>

                    {#if form.error}
                      <div
                        role="alert"
                        class="alert alert-error py-2 text-xs"
                        data-testid={`officer-error-${i}`}
                      >
                        <AlertCircle class="h-3 w-3 shrink-0" />
                        <span>{form.error}</span>
                      </div>
                    {/if}

                    <button
                      class="btn btn-primary btn-sm self-end"
                      onclick={() => void registerOfficer(i)}
                      disabled={form.loading || form.username.trim() === "" || form.csrPem.trim() === ""}
                      data-testid={`register-officer-btn-${i}`}
                    >
                      {#if form.loading}
                        <span class="loading loading-spinner loading-xs"></span>
                      {/if}
                      Register Officer
                    </button>

                  {:else}
                    <!-- Registration complete: show signed certificate -->
                    <div class="form-control">
                      <div class="flex items-center justify-between mb-1">
                        <span class="label-text text-xs font-semibold">Signed Certificate PEM</span>
                        <div class="flex gap-1">
                          <button
                            class="btn btn-ghost btn-xs gap-1"
                            onclick={() => copyCert(i)}
                            aria-label="Copy certificate PEM"
                            data-testid={`copy-cert-btn-${i}`}
                          >
                            <Copy class="h-3 w-3" />
                            {form.certCopied ? "Copied" : "Copy"}
                          </button>
                          <button
                            class="btn btn-ghost btn-xs gap-1"
                            onclick={() => downloadCert(i)}
                            aria-label="Download certificate PEM"
                            data-testid={`download-cert-btn-${i}`}
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
                        value={form.result.certPem}
                        data-testid={`officer-cert-${i}`}
                      ></textarea>
                      <p class="text-base-content/50 mt-1 text-xs">
                        Hand this certificate to the officer. They will need it to authenticate and claim their share.
                      </p>
                    </div>
                  {/if}
                </div>
              </div>
            {/each}

            <div class="flex justify-between mt-2">
              <button class="btn btn-ghost" onclick={handleOfficersBack}>
                Back
              </button>
              <button
                class="btn btn-primary"
                onclick={() => void handleOfficersNext()}
                disabled={!allOfficersRegistered}
                data-testid="initialize-btn"
              >
                Initialize
                {#if !allOfficersRegistered}
                  <span class="text-xs opacity-70">({registeredCount}/{totalOfficers})</span>
                {/if}
              </button>
            </div>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: applying                                                      -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "applying"}
          <div class="flex flex-col items-center gap-6 py-8">
            <span class="loading loading-spinner loading-lg text-primary"></span>
            <div class="text-center">
              <h2 class="text-xl font-semibold">Initializing Server</h2>
              <p class="text-base-content/60 mt-1 text-sm">
                Applying encryption barrier configuration...
              </p>
            </div>
          </div>

        <!-- ------------------------------------------------------------------ -->
        <!-- Step: complete                                                      -->
        <!-- ------------------------------------------------------------------ -->
        {:else if currentStep === "complete"}
          {#if shamirShareCount > 0}
            <!-- -----------------------------------------------------------
              Shamir initialization complete: officer enrollment status panel.
              Share bytes are NEVER shown here. Each SO must authenticate with
              their mTLS certificate and claim their share independently by
              navigating to their OfficerClaimPage.
            ----------------------------------------------------------- -->
            <div class="flex flex-col gap-5 py-2">
              <div class="flex flex-col items-center gap-2 text-center">
                <ShieldCheck class="text-success h-12 w-12" strokeWidth={1.5} />
                <h2 class="text-2xl font-bold">Shamir Barrier Initialized</h2>
              </div>

              <div role="alert" class="alert alert-info text-sm">
                <AlertTriangle class="h-5 w-5 shrink-0" />
                <span>
                  The barrier has been initialized with a <strong>{shamirThreshold}-of-{shamirShareCount}</strong>
                  Shamir scheme. Each Security Officer must now log in with
                  their <strong>own mTLS certificate</strong> and navigate to
                  <strong>/officer/claim</strong> to retrieve their share.
                  Share bytes are NEVER displayed here — they are returned
                  exactly once to the authenticated officer at claim time.
                </span>
              </div>

              {#if rootToken}
                <div class="form-control">
                  <span class="label-text font-semibold">Root Token</span>
                  <div class="bg-base-200 mt-1 flex items-center gap-2 rounded-lg p-3">
                    <span data-testid="root-token" class="flex-1 break-all font-mono text-sm">{rootToken}</span>
                  </div>
                  <p class="text-base-content/50 mt-1 text-xs">
                    Store this token securely. It can be regenerated from a share quorum if lost.
                  </p>
                </div>
              {/if}

              <!-- Officer claim status table -->
              <div class="form-control">
                <div class="flex items-center justify-between mb-2">
                  <span class="label-text font-semibold">
                    Officer Share Claim Status ({officerStatuses.filter((o) => o.claimedAt).length}/{shamirShareCount} claimed)
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
                    {claimStatusLoading ? "Loading..." : "No officer claim status available. Ensure officers are registered and refresh."}
                  </div>
                {:else}
                  <div class="overflow-x-auto">
                    <table class="table table-sm" data-testid="officer-claim-status-table">
                      <thead>
                        <tr>
                          <th>Officer</th>
                          <th>Share #</th>
                          <th>Enrolled</th>
                          <th>Status</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each officerStatuses as officer (officer.shareIndex)}
                          <tr>
                            <td class="font-mono text-sm">{officerLabel(officer)}</td>
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
                  <strong>Next steps:</strong> Direct each Security Officer to
                  authenticate using their newly-issued mTLS certificate and
                  navigate to the <span class="font-mono">/officer/claim</span> page
                  to complete their share claim.
                </p>
                <p>
                  The barrier remains fully operational. Officers can claim their
                  shares at any time — there is no deadline on the claim window.
                </p>
              </div>

              <button
                class="btn btn-primary btn-block"
                onclick={onComplete}
                data-testid="open-database-btn"
              >
                Open Database
              </button>
            </div>
          {:else}
            <!-- Passphrase / none: simple success -->
            <div class="flex flex-col items-center gap-6 py-8">
              <ShieldCheck class="text-success h-16 w-16" strokeWidth={1.5} />
              <div class="text-center">
                <h2 class="text-2xl font-bold">Server Initialized</h2>
                <p class="text-base-content/60 mt-2 text-sm">
                  The encryption barrier has been configured successfully.
                  Your server is ready to use.
                </p>
              </div>
              <button class="btn btn-primary btn-wide" onclick={onComplete}>
                Open Database
              </button>
            </div>
          {/if}
        {/if}

      </div>
    </div>
  </div>
</div>
