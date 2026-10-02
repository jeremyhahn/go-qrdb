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
  import { isDesktop } from "@qrdb/sdk";
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";
  import type { DatabaseMode } from "$stores/database-wizard.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  interface ModeOption {
    readonly id: DatabaseMode;
    readonly label: string;
    readonly description: string;
    readonly badge: string;
  }

  const allModes: readonly ModeOption[] = [
    {
      id: "embedded",
      label: "Embedded",
      description:
        "Store data locally using an embedded storage engine. Best for development, single-node deployments, or when running qrdbd on the same host.",
      badge: "Local",
    },
    {
      id: "remote",
      label: "Server",
      description:
        "Create the database on the connected qrdbd server. Best for production deployments with dedicated server infrastructure.",
      badge: "Network",
    },
  ] as const;

  // In web mode only the "remote" (server) option is available.
  const modes: readonly ModeOption[] = $derived(
    isDesktop() ? allModes : allModes.filter((m) => m.id !== "embedded"),
  );

  onMount(() => {
    // In web mode, "embedded" is never valid — force "remote" so downstream
    // steps and the creation phase never attempt desktop-only bindings.
    if (!isDesktop() && dbWizardStore.mode === "embedded") {
      selectMode("remote");
    }
  });

  function selectMode(mode: DatabaseMode): void {
    dbWizardStore.mode = mode;
    if (mode === "remote") {
      dbWizardStore.raftConfig = null;
      if (dbWizardStore.connectionConfig === null) {
        dbWizardStore.connectionConfig = {
          transport: "unix",
          address: "/var/run/qrdb.sock",
          caFile: "",
          certFile: "",
          keyFile: "",
        };
      }
    } else {
      dbWizardStore.connectionConfig = null;
      if (dbWizardStore.raftConfig === null) {
        dbWizardStore.raftConfig = {
          clusterMode: "none",
          replicaId: 1,
          raftAddress: "localhost:63001",
          peers: [],
          localNodeCount: 3,
          raftGroups: null,
        };
      }
    }
  }

  function handleNext(): void {
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Database Mode</h2>
    <p class="text-base-content/70 mt-2">
      Choose how this database will store and access data.
    </p>
  </div>

  <div class="grid w-full max-w-2xl grid-cols-1 gap-4 sm:grid-cols-2">
    {#each modes as mode (mode.id)}
      <button
        type="button"
        class="card cursor-pointer border-2 p-6 text-left transition-colors
          {dbWizardStore.mode === mode.id
            ? 'border-primary bg-primary/5'
            : 'border-base-300 hover:border-primary/50'}"
        onclick={() => selectMode(mode.id)}
      >
        <div class="flex items-start gap-3">
          <input
            type="radio"
            name="db-mode"
            class="radio radio-primary mt-1"
            value={mode.id}
            checked={dbWizardStore.mode === mode.id}
            onchange={() => selectMode(mode.id)}
          />
          <div>
            <div class="flex items-center gap-2">
              <span class="text-lg font-semibold">{mode.label}</span>
              <span class="badge badge-sm badge-outline">{mode.badge}</span>
            </div>
            <p class="text-base-content/60 mt-2 text-sm">
              {mode.description}
            </p>
          </div>
        </div>
      </button>
    {/each}
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" onclick={handleNext}>Next</button>
  </div>
</div>
