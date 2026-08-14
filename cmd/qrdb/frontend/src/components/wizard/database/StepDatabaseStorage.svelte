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
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";
  import type { StorageBackend } from "$stores/database-wizard.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  interface BackendOption {
    readonly id: StorageBackend;
    readonly label: string;
    readonly description: string;
    readonly badge: string | null;
  }

  const backends: readonly BackendOption[] = [
    {
      id: "memory",
      label: "Memory",
      description:
        "Fast, non-persistent storage. Data is lost on restart. Suitable for testing only.",
      badge: "Testing",
    },
    {
      id: "file",
      label: "File",
      description:
        "Simple file-based storage with fsync durability. Good for small datasets.",
      badge: null,
    },
    {
      id: "pebble",
      label: "Pebble",
      description:
        "Production-grade LSM storage engine. Optimized for write-heavy workloads with automatic compaction.",
      badge: "Recommended",
    },
  ] as const;

  function selectBackend(backend: StorageBackend): void {
    dbWizardStore.backend = backend;
  }

  function handleNext(): void {
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Storage Backend</h2>
    <p class="text-base-content/70 mt-2">
      Select the storage engine for your embedded database.
    </p>
  </div>

  <div class="grid w-full max-w-2xl grid-cols-1 gap-3">
    {#each backends as option (option.id)}
      <label
        class="card bg-base-200 hover:bg-base-300 cursor-pointer border-2 shadow transition-all"
        class:border-primary={dbWizardStore.backend === option.id}
        class:border-transparent={dbWizardStore.backend !== option.id}
      >
        <div class="card-body flex-row items-center gap-4 py-4">
          <input
            type="radio"
            name="db-storage-backend"
            class="radio radio-primary"
            value={option.id}
            checked={dbWizardStore.backend === option.id}
            onchange={() => selectBackend(option.id)}
          />
          <div class="flex-1">
            <div class="flex items-center gap-2">
              <span class="font-semibold">{option.label}</span>
              {#if option.badge !== null}
                <span
                  class="badge badge-sm"
                  class:badge-success={option.badge === "Recommended"}
                  class:badge-warning={option.badge === "Testing"}
                >
                  {option.badge}
                </span>
              {/if}
            </div>
            <p class="text-base-content/60 mt-1 text-sm">
              {option.description}
            </p>
          </div>
        </div>
      </label>
    {/each}
  </div>

  <!-- Data directory (file / pebble only) -->
  {#if dbWizardStore.backend !== "memory"}
    <div class="w-full max-w-2xl">
      <label class="label" for="data-dir-input">
        <span class="label-text font-semibold">Data Directory</span>
      </label>
      <input
        id="data-dir-input"
        type="text"
        class="input input-bordered w-full"
        placeholder="~/.config/qrdb/data"
        bind:value={dbWizardStore.dataDir}
      />
      <label class="label" for="data-dir-input">
        <span class="label-text-alt text-base-content/50">
          Leave empty for default location
        </span>
      </label>
    </div>
  {/if}

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" onclick={handleNext}>Next</button>
  </div>
</div>
