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
  import type {
    ImportFormat,
    JsonFormat,
    ValueMode,
    WizardStep,
    ImportConfig,
    ParsedPreview,
    ImportProgress as ImportProgressType,
    FieldMappingUpdate,
  } from './types.ts';
  import { WIZARD_STEPS, STEP_LABELS, MAX_FILE_SIZE } from './types.ts';
  import { parseCSV } from './parsers/csv.ts';
  import { parseJSON } from './parsers/json.ts';
  import { parseArchive } from './parsers/archive.ts';
  import FormatSelect from './FormatSelect.svelte';
  import FileUpload from './FileUpload.svelte';
  import FieldMapping from './FieldMapping.svelte';
  import ImportReview from './ImportReview.svelte';
  import ImportProgressComponent from './ImportProgress.svelte';

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  let {
    open,
    onClose,
    onImport,
  }: {
    open: boolean;
    onClose: () => void;
    onImport: (config: ImportConfig, data: ParsedPreview) => Promise<void>;
  } = $props();

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let currentStep = $state<WizardStep>('format');
  let showCancelConfirm = $state(false);

  // Format step state
  let selectedFormat = $state<ImportFormat | null>(null);
  let selectedJsonFormat = $state<JsonFormat>('array');

  // Upload step state
  let selectedFile = $state<File | null>(null);
  let parseLoading = $state(false);
  let parseError = $state<string | null>(null);
  let parsedPreview = $state<ParsedPreview | null>(null);

  // Mapping step state
  let keyField = $state('');
  let valueMode = $state<ValueMode>('row');
  let valueField = $state('');
  let skipHeader = $state(true);
  let createMissing = $state(true);
  let updateExisting = $state(true);

  // Review step state
  let transactional = $state(false);

  // Progress step state
  let importProgress = $state<ImportProgressType>({
    total: 0,
    processed: 0,
    succeeded: 0,
    failed: 0,
    errors: [],
    status: 'pending',
  });

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  const currentStepIndex = $derived(
    WIZARD_STEPS.indexOf(currentStep)
  );

  const canGoBack = $derived(
    currentStepIndex > 0 && currentStep !== 'progress'
  );

  const canGoNext = $derived.by(() => {
    switch (currentStep) {
      case 'format':
        return selectedFormat !== null;
      case 'upload':
        return selectedFile !== null && parsedPreview !== null && parseError === null;
      case 'mapping':
        return keyField !== '';
      case 'review':
        return true;
      case 'progress':
        return false;
      default:
        return false;
    }
  });

  const stepLabels = $derived(
    WIZARD_STEPS.filter((s) => s !== 'progress').map((s) => STEP_LABELS[s])
  );

  const displayStepIndex = $derived(
    currentStep === 'progress' ? 4 : currentStepIndex
  );

  const importConfig = $derived<ImportConfig>({
    format: selectedFormat ?? 'csv',
    jsonFormat: selectedFormat === 'json' ? selectedJsonFormat : undefined,
    file: selectedFile,
    keyField,
    valueMode,
    valueField: valueMode === 'field' ? valueField : undefined,
    skipHeader,
    createMissing,
    updateExisting,
    transactional,
  });

  // ---------------------------------------------------------------------------
  // Effects
  // ---------------------------------------------------------------------------

  // Reset state when dialog opens
  $effect(() => {
    if (open) {
      resetWizard();
    }
  });

  // ---------------------------------------------------------------------------
  // Methods
  // ---------------------------------------------------------------------------

  function resetWizard(): void {
    currentStep = 'format';
    showCancelConfirm = false;
    selectedFormat = null;
    selectedJsonFormat = 'array';
    selectedFile = null;
    parseLoading = false;
    parseError = null;
    parsedPreview = null;
    keyField = '';
    valueMode = 'row';
    valueField = '';
    skipHeader = true;
    createMissing = true;
    updateExisting = true;
    transactional = false;
    importProgress = {
      total: 0,
      processed: 0,
      succeeded: 0,
      failed: 0,
      errors: [],
      status: 'pending',
    };
  }

  async function parseFile(file: File): Promise<void> {
    parseLoading = true;
    parseError = null;
    parsedPreview = null;

    // Validate file size
    if (file.size > MAX_FILE_SIZE) {
      parseError = `File size exceeds maximum allowed (${Math.round(MAX_FILE_SIZE / 1024 / 1024)}MB)`;
      parseLoading = false;
      return;
    }

    try {
      let result;

      switch (selectedFormat) {
        case 'csv':
          result = await parseCSV(file);
          break;
        case 'json':
          result = await parseJSON(file, selectedJsonFormat);
          break;
        case 'archive':
          result = await parseArchive(file);
          break;
        default:
          parseError = 'Unknown format selected';
          parseLoading = false;
          return;
      }

      if (result.success) {
        parsedPreview = result.data;
        // Auto-select first header as key field if not set
        const firstHeader = result.data.headers[0];
        if (firstHeader !== undefined && keyField === '') {
          keyField = firstHeader;
        }
        // Auto-select second header as value field if available
        const secondHeader = result.data.headers[1];
        if (secondHeader !== undefined && valueField === '') {
          valueField = secondHeader;
        }
      } else {
        parseError = result.error;
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Unknown error';
      parseError = `Failed to parse file: ${message}`;
    } finally {
      parseLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Event Handlers
  // ---------------------------------------------------------------------------

  function handleFormatSelect(format: ImportFormat, jsonFormat?: JsonFormat): void {
    selectedFormat = format;
    if (jsonFormat !== undefined) {
      selectedJsonFormat = jsonFormat;
    }
  }

  function handleFileSelect(file: File): void {
    selectedFile = file;
    parseFile(file);
  }

  function handleFileRemove(): void {
    selectedFile = null;
    parsedPreview = null;
    parseError = null;
    keyField = '';
    valueField = '';
  }

  function handleMappingChange(update: FieldMappingUpdate): void {
    if (update.keyField !== undefined) {
      keyField = update.keyField;
    }
    if (update.valueMode !== undefined) {
      valueMode = update.valueMode;
    }
    if (update.valueField !== undefined) {
      valueField = update.valueField;
    }
    if (update.skipHeader !== undefined) {
      skipHeader = update.skipHeader;
    }
    if (update.createMissing !== undefined) {
      createMissing = update.createMissing;
    }
    if (update.updateExisting !== undefined) {
      updateExisting = update.updateExisting;
    }
  }

  function handleTransactionalChange(value: boolean): void {
    transactional = value;
  }

  async function handleStartImport(): Promise<void> {
    if (parsedPreview === null) {
      return;
    }

    currentStep = 'progress';
    importProgress = {
      total: parsedPreview.totalRows,
      processed: 0,
      succeeded: 0,
      failed: 0,
      errors: [],
      status: 'importing',
    };

    try {
      await onImport(importConfig, parsedPreview);
      importProgress.status = 'completed';
      importProgress.processed = importProgress.total;
      importProgress.succeeded = importProgress.total - importProgress.failed;
    } catch (error) {
      importProgress.status = 'error';
      if (error instanceof Error) {
        importProgress.errors.push({
          row: 0,
          key: 'Import',
          message: error.message,
        });
      }
    }
  }

  function handleCancelImport(): void {
    importProgress.status = 'cancelled';
  }

  function handleProgressClose(): void {
    onClose();
  }

  function handleBack(): void {
    const prevIndex = currentStepIndex - 1;
    if (prevIndex >= 0) {
      const prevStep = WIZARD_STEPS[prevIndex];
      if (prevStep !== undefined) {
        currentStep = prevStep;
      }
    }
  }

  function handleNext(): void {
    if (!canGoNext) {
      return;
    }

    if (currentStep === 'review') {
      handleStartImport();
      return;
    }

    const nextIndex = currentStepIndex + 1;
    if (nextIndex < WIZARD_STEPS.length - 1) {
      const nextStep = WIZARD_STEPS[nextIndex];
      if (nextStep !== undefined) {
        currentStep = nextStep;
      }
    }
  }

  function handleCancelClick(): void {
    if (currentStep === 'progress' && importProgress.status === 'importing') {
      showCancelConfirm = true;
    } else if (currentStep !== 'format' || selectedFile !== null) {
      showCancelConfirm = true;
    } else {
      onClose();
    }
  }

  function handleConfirmCancel(): void {
    showCancelConfirm = false;
    if (importProgress.status === 'importing') {
      handleCancelImport();
    }
    onClose();
  }

  function handleCancelCancel(): void {
    showCancelConfirm = false;
  }
</script>

<!-- Main Modal -->
<dialog class="modal" class:modal-open={open}>
  <div class="modal-box max-w-4xl">
    <!-- Header -->
    <div class="mb-6 flex items-center justify-between">
      <h2 class="text-xl font-bold">Import Data</h2>
      <button
        type="button"
        class="btn btn-ghost btn-sm btn-circle"
        onclick={handleCancelClick}
        aria-label="Close import wizard"
      >
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
      </button>
    </div>

    <!-- Step Indicator -->
    {#if currentStep !== 'progress'}
      <ul class="steps mb-8 w-full">
        {#each stepLabels as label, i (label)}
          <li
            class="step"
            class:step-primary={i <= displayStepIndex}
          >
            {label}
          </li>
        {/each}
      </ul>
    {/if}

    <!-- Step Content -->
    <div class="min-h-[400px]">
      {#if currentStep === 'format'}
        <FormatSelect
          {selectedFormat}
          {selectedJsonFormat}
          onSelect={handleFormatSelect}
        />
      {:else if currentStep === 'upload'}
        <FileUpload
          format={selectedFormat ?? 'csv'}
          jsonFormat={selectedJsonFormat}
          file={selectedFile}
          loading={parseLoading}
          error={parseError}
          onFileSelect={handleFileSelect}
          onFileRemove={handleFileRemove}
        />
      {:else if currentStep === 'mapping' && parsedPreview !== null}
        <FieldMapping
          preview={parsedPreview}
          {keyField}
          {valueMode}
          {valueField}
          {skipHeader}
          {createMissing}
          {updateExisting}
          format={selectedFormat ?? 'csv'}
          onChange={handleMappingChange}
        />
      {:else if currentStep === 'review' && parsedPreview !== null}
        <ImportReview
          config={importConfig}
          preview={parsedPreview}
          {transactional}
          onTransactionalChange={handleTransactionalChange}
          onStartImport={handleStartImport}
        />
      {:else if currentStep === 'progress'}
        <ImportProgressComponent
          progress={importProgress}
          onCancel={handleCancelImport}
          onClose={handleProgressClose}
        />
      {/if}
    </div>

    <!-- Footer Navigation -->
    {#if currentStep !== 'progress'}
      <div class="modal-action mt-6 flex justify-between">
        <button
          type="button"
          class="btn btn-ghost"
          onclick={handleCancelClick}
        >
          Cancel
        </button>

        <div class="flex gap-2">
          {#if canGoBack}
            <button
              type="button"
              class="btn btn-outline"
              onclick={handleBack}
            >
              Back
            </button>
          {/if}

          {#if currentStep !== 'review'}
            <button
              type="button"
              class="btn btn-primary"
              disabled={!canGoNext}
              onclick={handleNext}
            >
              Next
            </button>
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <form method="dialog" class="modal-backdrop">
    <button onclick={handleCancelClick}>close</button>
  </form>
</dialog>

<!-- Cancel Confirmation Modal -->
<dialog class="modal" class:modal-open={showCancelConfirm}>
  <div class="modal-box">
    <h3 class="text-lg font-bold">Cancel Import?</h3>
    <p class="py-4">
      {#if importProgress.status === 'importing'}
        The import is still in progress. Are you sure you want to cancel?
        Any partially imported data will remain.
      {:else}
        Are you sure you want to cancel? Your progress will be lost.
      {/if}
    </p>
    <div class="modal-action">
      <button class="btn btn-ghost" onclick={handleCancelCancel}>
        Continue
      </button>
      <button class="btn btn-error" onclick={handleConfirmCancel}>
        Cancel Import
      </button>
    </div>
  </div>
  <form method="dialog" class="modal-backdrop">
    <button onclick={handleCancelCancel}>close</button>
  </form>
</dialog>
