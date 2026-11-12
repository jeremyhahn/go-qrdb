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
  import type { ImportProgress as ImportProgressType } from "./types.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    progress,
    onCancel,
    onClose,
  }: {
    progress: ImportProgressType;
    onCancel: () => void;
    onClose: () => void;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let errorsExpanded = $state(false);

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const percentage = $derived.by(() => {
    if (progress.total === 0) {
      return 0;
    }
    return Math.round((progress.processed / progress.total) * 100);
  });

  const isComplete = $derived(
    progress.status === "completed" ||
      progress.status === "cancelled" ||
      progress.status === "error",
  );

  const isImporting = $derived(progress.status === "importing");

  const statusMessage = $derived.by(() => {
    switch (progress.status) {
      case "pending":
        return "Preparing import...";
      case "importing":
        return `Importing records... (${progress.processed.toLocaleString()} of ${progress.total.toLocaleString()})`;
      case "completed":
        return "Import completed successfully";
      case "cancelled":
        return "Import was cancelled";
      case "error":
        return "Import failed with errors";
      default:
        return "";
    }
  });

  const statusIcon = $derived.by(() => {
    switch (progress.status) {
      case "completed":
        return {
          icon: "M5 13l4 4L19 7",
          class: "text-success",
        };
      case "cancelled":
        return {
          icon: "M6 18L18 6M6 6l12 12",
          class: "text-warning",
        };
      case "error":
        return {
          icon: "M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z",
          class: "text-error",
        };
      default:
        return null;
    }
  });

  const progressBarClass = $derived.by(() => {
    switch (progress.status) {
      case "completed":
        return "progress-success";
      case "error":
        return "progress-error";
      case "cancelled":
        return "progress-warning";
      default:
        return "progress-primary";
    }
  });

  const displayedErrors = $derived.by(() => {
    if (errorsExpanded) {
      return progress.errors;
    }
    return progress.errors.slice(0, 5);
  });
</script>

<div class="space-y-6">
  <div>
    <h3 class="mb-2 text-lg font-semibold">Import Progress</h3>
    <p class="text-base-content/70 text-sm">
      {statusMessage}
    </p>
  </div>

  <!-- Progress Bar -->
  <div class="space-y-3">
    <div class="flex items-center justify-between">
      <span class="text-sm font-medium">{percentage}%</span>
      {#if isComplete && statusIcon}
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-5 w-5 {statusIcon.class}"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d={statusIcon.icon}
          />
        </svg>
      {:else if isImporting}
        <span class="loading loading-spinner loading-sm"></span>
      {/if}
    </div>

    <progress
      class="progress {progressBarClass} h-3 w-full"
      value={progress.processed}
      max={progress.total}
    ></progress>
  </div>

  <!-- Stats Grid -->
  <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
    <div class="bg-base-200/50 rounded-lg p-3 text-center">
      <p class="text-base-content/60 text-xs uppercase">Total</p>
      <p class="text-xl font-bold">{progress.total.toLocaleString()}</p>
    </div>

    <div class="bg-base-200/50 rounded-lg p-3 text-center">
      <p class="text-base-content/60 text-xs uppercase">Processed</p>
      <p class="text-xl font-bold">{progress.processed.toLocaleString()}</p>
    </div>

    <div class="bg-success/10 rounded-lg p-3 text-center">
      <p class="text-success/70 text-xs uppercase">Succeeded</p>
      <p class="text-success text-xl font-bold">
        {progress.succeeded.toLocaleString()}
      </p>
    </div>

    <div class="bg-error/10 rounded-lg p-3 text-center">
      <p class="text-error/70 text-xs uppercase">Failed</p>
      <p class="text-error text-xl font-bold">
        {progress.failed.toLocaleString()}
      </p>
    </div>
  </div>

  <!-- Error Log -->
  {#if progress.errors.length > 0}
    <div class="card bg-error/5 border-error/20 border">
      <div class="card-body p-4">
        <button
          type="button"
          class="flex w-full items-center justify-between text-left"
          onclick={() => {
            errorsExpanded = !errorsExpanded;
          }}
        >
          <h4 class="text-error flex items-center gap-2 text-sm font-semibold">
            <svg
              xmlns="http://www.w3.org/2000/svg"
              class="h-5 w-5"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
              />
            </svg>
            Error Log ({progress.errors.length} errors)
          </h4>

          <svg
            xmlns="http://www.w3.org/2000/svg"
            class="h-5 w-5 transition-transform {errorsExpanded
              ? 'rotate-180'
              : ''}"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M19 9l-7 7-7-7"
            />
          </svg>
        </button>

        {#if errorsExpanded || displayedErrors.length <= 5}
          <div class="mt-4 space-y-2">
            {#each displayedErrors as error (error.row)}
              <div class="bg-base-100 flex items-start gap-3 rounded-lg p-3">
                <span class="badge badge-error badge-sm shrink-0"
                  >Row {error.row}</span
                >
                <div class="min-w-0 flex-1">
                  <p
                    class="text-base-content truncate text-sm font-medium"
                    title={error.key}
                  >
                    Key: {error.key}
                  </p>
                  <p class="text-base-content/70 text-sm">{error.message}</p>
                </div>
              </div>
            {/each}

            {#if !errorsExpanded && progress.errors.length > 5}
              <button
                type="button"
                class="btn btn-ghost btn-sm w-full"
                onclick={() => {
                  errorsExpanded = true;
                }}
              >
                Show {progress.errors.length - 5} more errors
              </button>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Status-Specific Messages -->
  {#if progress.status === "completed" && progress.failed === 0}
    <div class="alert alert-success">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5 shrink-0"
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
      <span
        >All {progress.succeeded.toLocaleString()} records imported successfully.</span
      >
    </div>
  {:else if progress.status === "completed" && progress.failed > 0}
    <div class="alert alert-warning">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5 shrink-0"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
        />
      </svg>
      <span>
        Import completed with {progress.failed.toLocaleString()} failures.
        {progress.succeeded.toLocaleString()} records imported successfully.
      </span>
    </div>
  {:else if progress.status === "cancelled"}
    <div class="alert alert-warning">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5 shrink-0"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <span>
        Import was cancelled. {progress.succeeded.toLocaleString()} of {progress.total.toLocaleString()}
        records were imported before cancellation.
      </span>
    </div>
  {:else if progress.status === "error"}
    <div class="alert alert-error">
      <svg
        xmlns="http://www.w3.org/2000/svg"
        class="h-5 w-5 shrink-0"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <span>Import failed. Please check the error log for details.</span>
    </div>
  {/if}

  <!-- Action Buttons -->
  <div class="flex justify-end gap-3">
    {#if isImporting}
      <button type="button" class="btn btn-warning gap-2" onclick={onCancel}>
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-5 w-5"
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
        Cancel Import
      </button>
    {:else}
      <button type="button" class="btn btn-primary gap-2" onclick={onClose}>
        <svg
          xmlns="http://www.w3.org/2000/svg"
          class="h-5 w-5"
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
        Done
      </button>
    {/if}
  </div>
</div>
