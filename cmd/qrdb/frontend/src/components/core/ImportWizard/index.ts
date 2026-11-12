// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * ImportWizard component exports.
 *
 * A comprehensive batch data import wizard for the QRDB GUI.
 * Supports CSV, JSON, and archive file imports with:
 * - Auto-detection of CSV delimiters
 * - Multiple JSON format variants (object, array, NDJSON)
 * - Field mapping configuration
 * - Transactional and best-effort import modes
 * - Progress tracking with error logging
 *
 * @example
 * ```svelte
 * <script lang="ts">
 *   import { ImportWizard, type ImportConfig, type ParsedPreview } from '$components/core/ImportWizard';
 *
 *   let showWizard = $state(false);
 *
 *   async function handleImport(config: ImportConfig, data: ParsedPreview): Promise<void> {
 *     // Implement import logic here
 *     console.log('Importing:', config, data);
 *   }
 * </script>
 *
 * <button class="btn" onclick={() => { showWizard = true; }}>
 *   Import Data
 * </button>
 *
 * <ImportWizard
 *   open={showWizard}
 *   onClose={() => { showWizard = false; }}
 *   onImport={handleImport}
 * />
 * ```
 */

// Main component
export { default as ImportWizard } from "./ImportWizard.svelte";

// Sub-components (for advanced customization)
export { default as FormatSelect } from "./FormatSelect.svelte";
export { default as FileUpload } from "./FileUpload.svelte";
export { default as FieldMapping } from "./FieldMapping.svelte";
export { default as ImportReview } from "./ImportReview.svelte";
export { default as ImportProgress } from "./ImportProgress.svelte";

// Parsers
export {
  parseCSV,
  parseCSVWithDelimiter,
  detectCSVDelimiter,
  getDelimiterName,
} from "./parsers/csv.ts";
export {
  parseJSON,
  detectJSONFormat,
  getJSONFormatDescription,
  getJSONFormatExample,
} from "./parsers/json.ts";
export {
  parseArchive,
  isArchiveFile,
  getArchiveImportDescription,
  getSupportedArchiveFormats,
  ARCHIVE_EXTENSIONS,
  ARCHIVE_MIME_TYPES,
  type ArchiveEntry,
  type ArchiveAnalysis,
} from "./parsers/archive.ts";

// Types
export type {
  // Format types
  ImportFormat,
  ValueMode,
  JsonFormat,
  WizardStep,

  // Configuration types
  ImportConfig,
  ParsedPreview,
  CsvDelimiter,
  DelimiterDetection,

  // Progress types
  ImportStatus,
  ImportProgress as ImportProgressType,
  ImportError,

  // Component props
  ImportWizardProps,
  FormatSelectProps,
  FileUploadProps,
  FieldMappingProps,
  FieldMappingUpdate,
  ImportReviewProps,
  ImportProgressProps,

  // Parser types
  ParseResult,
  ParserFunction,
} from "./types.ts";

// Constants
export {
  MAX_FILE_SIZE,
  PREVIEW_ROWS,
  LARGE_IMPORT_THRESHOLD,
  STEP_LABELS,
  WIZARD_STEPS,
} from "./types.ts";
