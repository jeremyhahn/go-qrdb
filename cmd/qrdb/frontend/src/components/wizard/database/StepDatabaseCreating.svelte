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
  import type { Client } from "@qrdb/sdk";
  import {
    dbWizardStore,
    takeBarrierPassphrase,
    unavailableSealModes,
    SealModeUnavailableError,
  } from "$stores/database-wizard.svelte.ts";

  let {
    client,
    onComplete,
  }: {
    client: Client;
    onComplete: () => void;
  } = $props();

  interface CreatePhase {
    readonly label: string;
    readonly status: "pending" | "active" | "done" | "error";
  }

  let phases: CreatePhase[] = $state([]);
  let currentPhaseIndex: number = $state(-1);
  let progressPercent: number = $state(0);
  let errorMessage: string = $state("");

  let currentLabel = $derived(
    currentPhaseIndex >= 0 && currentPhaseIndex < phases.length
      ? phases[currentPhaseIndex]?.label ?? "Creating..."
      : errorMessage
        ? "Database creation failed"
        : "Preparing...",
  );

  function buildPhases(): CreatePhase[] {
    // The active server (embedded or external) is already established by
    // the connection manager — the wizard does NOT start a new server.
    const steps: CreatePhase[] = [
      { label: "Saving configuration...", status: "pending" },
      { label: "Creating database...", status: "pending" },
    ];
    if (dbWizardStore.barrierEnabled) {
      steps.push({
        label: "Configuring encryption barrier...",
        status: "pending",
      });
    }
    steps.push({ label: "Verifying database...", status: "pending" });
    steps.push({ label: "Complete", status: "pending" });
    return steps;
  }

  function advancePhase(index: number): void {
    console.debug(
      "[StepDatabaseCreating] advancePhase",
      index,
      phases[index]?.label,
    );
    currentPhaseIndex = index;
    if (phases[index] !== undefined) {
      phases[index] = { ...phases[index], status: "active" };
    }
    progressPercent = Math.round(((index + 1) / phases.length) * 100);
  }

  function completePhase(index: number): void {
    console.debug(
      "[StepDatabaseCreating] completePhase",
      index,
      phases[index]?.label,
    );
    if (phases[index] !== undefined) {
      phases[index] = { ...phases[index], status: "done" };
    }
  }

  function failPhase(index: number): void {
    console.debug(
      "[StepDatabaseCreating] failPhase",
      index,
      phases[index]?.label,
    );
    if (phases[index] !== undefined) {
      phases[index] = { ...phases[index], status: "error" };
    }
  }

  /**
   * Assembles the CreateDatabaseRequest from the wizard store state and
   * calls the SDK to create the database on the active server.
   */
  async function runCreate(): Promise<void> {
    console.debug("[StepDatabaseCreating] runCreate started", {
      name: dbWizardStore.name,
      engine: dbWizardStore.engine,
      mode: dbWizardStore.mode,
    });
    let phaseIdx = 0;

    // Phase 1: Save configuration.
    advancePhase(phaseIdx);
    // Taken (and cleared from the store) before anything can fail, so the
    // passphrase outlives neither a successful nor a failed request.
    const barrierSecret = takeBarrierPassphrase();
    // Backstop for the barrier step: a seal mode the wizard cannot offer is
    // refused here, before any request is built or sent.
    const unavailable = dbWizardStore.barrierEnabled
      ? unavailableSealModes[dbWizardStore.sealMode]
      : undefined;
    if (unavailable !== undefined) {
      throw new SealModeUnavailableError(dbWizardStore.sealMode, unavailable);
    }
    const req = {
      name: dbWizardStore.name,
      engine: dbWizardStore.engine,
      mode: dbWizardStore.mode,
      backend: dbWizardStore.backend || "pebble",
      ...(dbWizardStore.mode === "embedded" && dbWizardStore.dataDir
        ? { dataDir: dbWizardStore.dataDir }
        : {}),
      // Replication factor and dedicated WAL for a dedicated-raft database.
      // Both are only meaningful on a Raft-mode server; the backend ignores
      // them for non-raft databases.
      ...(dbWizardStore.serverMode === "raft"
        ? {
            replicas: Math.max(1, dbWizardStore.replicas || 1),
            ...(dbWizardStore.dedicatedWal ? { dedicatedWal: true } : {}),
          }
        : {}),
      ...(dbWizardStore.mode === "embedded" && dbWizardStore.raftConfig !== null
        ? {
            raft: {
              clusterMode: dbWizardStore.raftConfig.clusterMode,
              replicaId: dbWizardStore.raftConfig.replicaId,
              raftAddress: dbWizardStore.raftConfig.raftAddress,
              peers: dbWizardStore.raftConfig.peers,
              localNodeCount: dbWizardStore.raftConfig.localNodeCount,
            },
          }
        : {}),
      ...(dbWizardStore.mode === "remote" && dbWizardStore.connectionConfig !== null
        ? {
            connection: {
              transport: dbWizardStore.connectionConfig.transport,
              address: dbWizardStore.connectionConfig.address,
              ...(dbWizardStore.connectionConfig.caFile ? { caFile: dbWizardStore.connectionConfig.caFile } : {}),
              ...(dbWizardStore.connectionConfig.certFile ? { certFile: dbWizardStore.connectionConfig.certFile } : {}),
              ...(dbWizardStore.connectionConfig.keyFile ? { keyFile: dbWizardStore.connectionConfig.keyFile } : {}),
            },
          }
        : {}),
      ...(dbWizardStore.sqlMySQL || dbWizardStore.sqlPostgreSQL
        ? {
            protocol: {
              mysql: dbWizardStore.sqlMySQL,
              postgresql: dbWizardStore.sqlPostgreSQL,
            },
          }
        : {}),
      // The barrier is enabled at create and initialised with the passphrase
      // collected in the barrier step.
      ...(dbWizardStore.barrierEnabled
        ? {
            barrier: {
              enabled: true,
              mode: dbWizardStore.sealMode,
              ...(barrierSecret !== "" ? { secret: barrierSecret } : {}),
              ...(dbWizardStore.sealMode === "shamir"
                ? {
                    sharesRequired: dbWizardStore.sealConfig.sharesRequired,
                    sharesTotal: dbWizardStore.sealConfig.sharesTotal,
                  }
                : {}),
            },
          }
        : {}),
    };
    // Never log the request itself: it carries the barrier passphrase.
    console.debug("[StepDatabaseCreating] assembled request", {
      name: req.name,
      engine: req.engine,
      backend: req.backend,
      barrier: dbWizardStore.barrierEnabled ? dbWizardStore.sealMode : "none",
    });
    completePhase(phaseIdx);
    phaseIdx++;

    // Phase 2: Create the database via the SDK on the already-active server.
    advancePhase(phaseIdx);
    await client.database.create(req);
    console.debug("[StepDatabaseCreating] database created");
    completePhase(phaseIdx);
    phaseIdx++;

    // Phase 4 (conditional): Configure encryption barrier.
    if (dbWizardStore.barrierEnabled) {
      advancePhase(phaseIdx);
      console.debug("[StepDatabaseCreating] configuring barrier");
      completePhase(phaseIdx);
      phaseIdx++;
    }

    // Phase 5: Verify database.
    advancePhase(phaseIdx);
    console.debug("[StepDatabaseCreating] verifying database");
    completePhase(phaseIdx);
    phaseIdx++;

    // Phase 6: Complete.
    advancePhase(phaseIdx);
    completePhase(phaseIdx);

    console.debug("[StepDatabaseCreating] creation complete");
    progressPercent = 100;
    onComplete();
  }

  onMount(() => {
    console.debug("[StepDatabaseCreating] onMount", {
      name: dbWizardStore.name,
      engine: dbWizardStore.engine,
    });
    phases = buildPhases();

    async function run(): Promise<void> {
      try {
        await runCreate();
      } catch (err: unknown) {
        console.error("[StepDatabaseCreating] creation failed", err);
        if (currentPhaseIndex >= 0) {
          failPhase(currentPhaseIndex);
        }
        // Wails errors arrive as strings, not Error instances. Coerce
        // anything non-null to its string representation so the user
        // sees the actual cause instead of "An unknown error occurred".
        if (err instanceof Error) {
          errorMessage = err.message;
        } else if (typeof err === "string" && err.length > 0) {
          errorMessage = err;
        } else if (err !== null && err !== undefined) {
          errorMessage = String(err);
        } else {
          errorMessage = "An unknown error occurred";
        }
      }
    }

    run();
  });
</script>

<div class="flex flex-col items-center gap-8">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Creating Database</h2>
    <p class="text-base-content/70 mt-2">
      Setting up
      <span class="font-mono font-semibold">{dbWizardStore.name}</span>...
    </p>
  </div>

  <div class="w-full max-w-md space-y-6">
    <!-- Progress bar -->
    <div class="w-full">
      <progress
        class="progress {errorMessage ? 'progress-error' : 'progress-primary'} w-full"
        value={progressPercent}
        max="100"
      ></progress>
      <div class="mt-2 flex justify-between text-sm">
        <span class="text-base-content/60">{currentLabel}</span>
        <span class="text-base-content/50">{progressPercent}%</span>
      </div>
    </div>

    <!-- Spinner (hidden on error) -->
    {#if !errorMessage}
      <div class="flex justify-center">
        <span class="loading loading-spinner loading-lg text-primary"></span>
      </div>
    {/if}

    <!-- Phase indicators -->
    <div class="space-y-2">
      {#each phases as phase (phase.label)}
        <div class="flex items-center gap-3">
          {#if phase.status === "done"}
            <svg
              xmlns="http://www.w3.org/2000/svg"
              class="h-5 w-5 text-success"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M5 13l4 4L19 7"
              />
            </svg>
          {:else if phase.status === "active"}
            <span
              class="loading loading-spinner loading-sm text-primary"
            ></span>
          {:else if phase.status === "error"}
            <svg
              xmlns="http://www.w3.org/2000/svg"
              class="h-5 w-5 text-error"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          {:else}
            <div
              class="h-5 w-5 rounded-full border-2 border-base-content/20"
            ></div>
          {/if}
          <span
            class="text-sm {phase.status === 'done' || phase.status === 'active'
              ? 'text-base-content'
              : phase.status === 'error'
                ? 'text-error'
                : 'text-base-content/40'}"
          >
            {phase.label}
          </span>
        </div>
      {/each}
    </div>

    <!-- Error message -->
    {#if errorMessage}
      <div role="alert" class="alert alert-error">
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-6 w-6 shrink-0 stroke-current"
          fill="none"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
          />
        </svg>
        <span>{errorMessage}</span>
      </div>
    {/if}
  </div>
</div>
