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
  import type { EngineType } from "$stores/database-wizard.svelte.ts";

  let { onNext, onCancel }: { onNext: () => void; onCancel?: () => void } =
    $props();

  /** Regex for valid database names: lowercase alphanumeric and hyphens. */
  const namePattern = /^[a-z0-9][a-z0-9-]*$/;

  interface EngineOption {
    readonly id: EngineType;
    readonly label: string;
    readonly description: string;
    readonly badge: string | null;
  }

  const engines: readonly EngineOption[] = [
    {
      id: "kv",
      label: "Key-Value",
      description:
        "General-purpose key-value store with MVCC versioning, transactions, and range queries.",
      badge: "General Purpose",
    },
    {
      id: "timeseries",
      label: "TimeSeries",
      description:
        "Optimized for time-stamped data with automatic partitioning and retention policies.",
      badge: "Time Data",
    },
    {
      id: "vector",
      label: "Vector",
      description:
        "Vector similarity search with HNSW indexing for embeddings and AI workloads.",
      badge: "AI/ML",
    },
  ] as const;

  /** Validation: name must be non-empty and match the pattern. */
  let nameValid = $derived(
    dbWizardStore.name.length > 0 && namePattern.test(dbWizardStore.name),
  );

  /** Validation error message for the name field. */
  let nameError = $derived<string | null>(
    dbWizardStore.name.length === 0
      ? null
      : !namePattern.test(dbWizardStore.name)
        ? "Name must be lowercase alphanumeric with hyphens only, starting with a letter or number."
        : null,
  );

  function selectEngine(engine: EngineType): void {
    dbWizardStore.engine = engine;
  }

  function handleNext(): void {
    if (!nameValid) return;
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Database Name & Engine</h2>
    <p class="text-base-content/70 mt-2">
      Choose a name and engine type for your new database.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    <!-- Database name -->
    <div class="form-control">
      <label class="label" for="db-name">
        <span class="label-text font-semibold">Database Name</span>
      </label>
      <input
        id="db-name"
        type="text"
        class="input input-bordered w-full"
        class:input-error={nameError !== null}
        placeholder="my-database"
        bind:value={dbWizardStore.name}
      />
      {#if nameError !== null}
        <label class="label" for="db-name">
          <span class="label-text-alt text-error">{nameError}</span>
        </label>
      {:else}
        <label class="label" for="db-name">
          <span class="label-text-alt text-base-content/60">
            Lowercase letters, numbers, and hyphens. Must start with a letter or number.
          </span>
        </label>
      {/if}
    </div>

    <!-- Engine type -->
    <fieldset>
      <legend class="label">
        <span class="label-text font-semibold">Engine Type</span>
      </legend>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {#each engines as engine (engine.id)}
          <button
            type="button"
            class="card cursor-pointer border-2 p-4 text-left transition-colors
              {dbWizardStore.engine === engine.id
                ? 'border-primary bg-primary/5'
                : 'border-base-300 hover:border-primary/50'}"
            onclick={() => selectEngine(engine.id)}
          >
            <div class="flex flex-col gap-2">
              <div class="flex items-center gap-2">
                <input
                  type="radio"
                  name="engine-type"
                  class="radio radio-primary"
                  value={engine.id}
                  checked={dbWizardStore.engine === engine.id}
                  onchange={() => selectEngine(engine.id)}
                />
                <span class="font-semibold">{engine.label}</span>
              </div>
              {#if engine.badge !== null}
                <span class="badge badge-sm badge-info">{engine.badge}</span>
              {/if}
              <p class="text-base-content/60 text-sm">
                {engine.description}
              </p>
            </div>
          </button>
        {/each}
      </div>
    </fieldset>
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    {#if onCancel}
      <button class="btn btn-ghost" onclick={onCancel}>Cancel</button>
    {:else}
      <div></div>
    {/if}
    <button class="btn btn-primary" disabled={!nameValid} onclick={handleNext}>
      Next
    </button>
  </div>
</div>
