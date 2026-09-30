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
  import type { Client, OfficerStatus } from "@qrdb/sdk";
  import { ShieldCheck, AlertCircle, AlertTriangle, RefreshCw, CheckCircle, Clock } from "lucide-svelte";
  import StepIndicator from "./wizard/StepIndicator.svelte";
  import StepGlobalBarrier from "./wizard/setup/StepGlobalBarrier.svelte";
  import HolderEnrolmentForms from "./wizard/HolderEnrolmentForms.svelte";
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

  // Holder claim status (polled after shamir init).
  let officerStatuses: OfficerStatus[] = $state([]);
  let claimStatusLoading: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Holder-enrolment step state
  // ---------------------------------------------------------------------------

  /** Holders enrolled so far, bound from HolderEnrolmentForms. */
  let registeredCount: number = $state(0);

  /** Holders needed: the Shamir share count (N). */
  let totalOfficers = $derived(setupWizardStore.barrierConfig.shamirShares);

  /** Whether every holder is enrolled, gating the Initialize button. */
  let allOfficersRegistered = $derived(registeredCount === totalOfficers && totalOfficers > 0);

  // ---------------------------------------------------------------------------
  // Navigation
  // ---------------------------------------------------------------------------

  async function handleBarrierNext(): Promise<void> {
    applyError = "";
    const cfg = setupWizardStore.barrierConfig;
    if (cfg.mode === "shamir") {
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
        const result = await client.seal.initializeShamir({
          threshold: cfg.shamirThreshold,
          totalShares: cfg.shamirShares,
        });
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
                certificate, which you hand back to the holder out of band. No
                share exists yet: initialization seals one to each holder, who
                claims it with its own keys from the qrdb CLI or the desktop app.
              </p>
            </div>

            {#if applyError}
              <div role="alert" class="alert alert-error text-sm" data-testid="officer-apply-error">
                <AlertCircle class="h-4 w-4 shrink-0" />
                <span>{applyError}</span>
              </div>
            {/if}

            <HolderEnrolmentForms {client} count={totalOfficers} bind:registeredCount />

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
              Shamir initialization complete: holder claim status panel.
              No share is ever shown here. Each holder claims its sealed
              envelope with its own keys from the qrdb CLI or the desktop app.
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
                  Shamir scheme. Each holder's share is sealed to that holder.
                  Every holder now claims its envelope, exactly once, over its
                  <strong>own holder certificate</strong> from the qrdb CLI
                  (<span class="font-mono">qrdb barrier officer claim</span>) or
                  the desktop app. No share is ever displayed here.
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
                    Holder Claim Status ({officerStatuses.filter((o) => o.claimedAt).length}/{shamirShareCount} claimed)
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
