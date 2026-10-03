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
  import { onDestroy } from "svelte";
  import type { Client } from "@qrdb/sdk";
  import {
    clearBarrierPassphrase,
    dbWizardStore,
    activeSteps,
    nextStep,
    prevStep,
    resetDatabaseWizard,
  } from "$stores/database-wizard.svelte.ts";
  import type { DatabaseWizardStep } from "$stores/database-wizard.svelte.ts";

  import StepIndicator from "../StepIndicator.svelte";
  import StepDatabaseName from "./StepDatabaseName.svelte";
  import StepDatabaseStorage from "./StepDatabaseStorage.svelte";
  import StepDatabaseRaft from "./StepDatabaseRaft.svelte";
  import StepDatabaseConnection from "./StepDatabaseConnection.svelte";
  import StepDatabaseSQL from "./StepDatabaseSQL.svelte";
  import StepDatabaseBarrier from "./StepDatabaseBarrier.svelte";
  import StepDatabaseSummary from "./StepDatabaseSummary.svelte";
  import StepDatabaseCreating from "./StepDatabaseCreating.svelte";
  import StepDatabaseHolders from "./StepDatabaseHolders.svelte";

  let {
    client,
    onComplete,
    onCancel,
  }: {
    client: Client;
    onComplete: () => void;
    onCancel?: () => void;
  } = $props();

  // -------------------------------------------------------------------------
  // Step metadata for the indicator
  // -------------------------------------------------------------------------

  /** Human-readable labels for each step, keyed by step ID. */
  const stepLabels: Readonly<Record<DatabaseWizardStep, string>> = {
    name: "Name",
    mode: "Mode", // retained for type compatibility, not shown in steps
    storage: "Storage",
    raft: "Raft",
    connection: "Connection",
    sql: "Protocols",
    barrier: "Barrier",
    summary: "Summary",
    creating: "Create",
    holders: "Holders",
    complete: "Complete",
  } as const;

  /** The visible step sequence (dynamic based on mode). */
  let visibleSteps = $derived<readonly DatabaseWizardStep[]>(activeSteps());

  /** Labels for the visible steps. */
  let indicatorLabels = $derived(visibleSteps.map((s) => stepLabels[s]));

  /** Current index within the visible step sequence. */
  let currentIndex = $derived(
    visibleSteps.indexOf(dbWizardStore.currentStep),
  );

  // -------------------------------------------------------------------------
  // Navigation handlers
  // -------------------------------------------------------------------------

  function handleNext(): void {
    nextStep();
  }

  function handleBack(): void {
    prevStep();
  }

  // Leaving the wizard by any route -- navigating away, closing the view --
  // drops a passphrase the barrier step stored and no create took.
  onDestroy(clearBarrierPassphrase);

  function handleCancel(): void {
    resetDatabaseWizard();
    onCancel?.();
  }

  function handleCreatingComplete(): void {
    nextStep();
    // A Shamir database goes on to enrol its holders; any other is done.
    if (dbWizardStore.currentStep !== "holders") {
      onComplete();
    }
  }

  function handleHoldersDone(): void {
    nextStep();
    onComplete();
  }

  // The database exists either way: closing before Initialize leaves it
  // sealed, and the caller refreshes its databases and opens it as for a
  // completed create.
  function handleHoldersLeave(): void {
    onComplete();
  }
</script>

<div class="flex flex-col items-center px-4 py-8">
  <div class="w-full max-w-3xl">
    <!-- Header -->
    <div class="mb-8 text-center">
      <h1 class="text-3xl font-bold">Create Database</h1>
      <p class="text-base-content/60 mt-1 text-sm">
        Configure and create a new QRDB database
      </p>
    </div>

    <!-- Step indicator -->
    {#if dbWizardStore.currentStep !== "complete"}
      <div class="mb-8">
        <StepIndicator steps={indicatorLabels} currentIndex={currentIndex} />
      </div>
    {/if}

    <!-- Step content -->
    <div class="card bg-base-100 shadow-xl">
      <div class="card-body">
        {#if dbWizardStore.currentStep === "name"}
          <StepDatabaseName onNext={handleNext} onCancel={handleCancel} />
        {:else if dbWizardStore.currentStep === "storage"}
          <StepDatabaseStorage onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "raft"}
          <StepDatabaseRaft onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "connection"}
          <StepDatabaseConnection onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "sql"}
          <StepDatabaseSQL onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "barrier"}
          <StepDatabaseBarrier onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "summary"}
          <StepDatabaseSummary onNext={handleNext} onBack={handleBack} />
        {:else if dbWizardStore.currentStep === "creating"}
          <StepDatabaseCreating {client} onComplete={handleCreatingComplete} />
        {:else if dbWizardStore.currentStep === "holders"}
          <StepDatabaseHolders {client} onDone={handleHoldersDone} onLeave={handleHoldersLeave} />
        {:else if dbWizardStore.currentStep === "complete"}
          <div class="flex flex-col items-center gap-4 py-8">
            <svg
              xmlns="http://www.w3.org/2000/svg"
              class="h-16 w-16 text-success"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
            <h2 class="text-2xl font-bold">Database Created</h2>
            <p class="text-base-content/70">
              Your database <span class="font-mono font-semibold">{dbWizardStore.name}</span> is ready.
            </p>
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>
