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
 * ImportWizard type definitions.
 *
 * Provides comprehensive TypeScript interfaces for the batch data import wizard
 * including format configuration, parsing, field mapping, and progress tracking.
 */

// ---------------------------------------------------------------------------
// Format Configuration
// ---------------------------------------------------------------------------

/** Supported import file formats. */
export type ImportFormat = 'csv' | 'json' | 'archive';

/** Value extraction mode. */
export type ValueMode = 'field' | 'row';

/** JSON format variants. */
export type JsonFormat = 'object' | 'array' | 'ndjson';

/** Import wizard steps. */
export type WizardStep = 'format' | 'upload' | 'mapping' | 'review' | 'progress';

// ---------------------------------------------------------------------------
// Import Configuration
// ---------------------------------------------------------------------------

/**
 * Complete configuration for a data import operation.
 */
export interface ImportConfig {
  /** Selected file format. */
  readonly format: ImportFormat;
  /** JSON format variant (when format is 'json'). */
  readonly jsonFormat?: JsonFormat | undefined;
  /** File to import. */
  readonly file: File | null;
  /** Field to use as the key. */
  readonly keyField: string;
  /** How to extract the value. */
  readonly valueMode: ValueMode;
  /** Field to use as the value (when valueMode is 'field'). */
  readonly valueField?: string | undefined;
  /** Skip the first row (header) in CSV files. */
  readonly skipHeader: boolean;
  /** Create keys that don't exist. */
  readonly createMissing: boolean;
  /** Update keys that already exist. */
  readonly updateExisting: boolean;
  /** Execute as a single transaction (all-or-nothing). */
  readonly transactional: boolean;
}

// ---------------------------------------------------------------------------
// Parsing Types
// ---------------------------------------------------------------------------

/**
 * Preview of parsed file data.
 */
export interface ParsedPreview {
  /** Column headers or field names. */
  readonly headers: string[];
  /** Sample rows for preview (first 5-10 rows). */
  readonly rows: Record<string, string>[];
  /** Total number of rows in the file. */
  readonly totalRows: number;
  /** Detected format. */
  readonly format: ImportFormat;
}

/** CSV delimiter characters. */
export type CsvDelimiter = ',' | ';' | '\t' | '|';

/**
 * Result of CSV delimiter detection.
 */
export interface DelimiterDetection {
  /** Detected delimiter. */
  readonly delimiter: CsvDelimiter;
  /** Confidence score (0-1). */
  readonly confidence: number;
}

// ---------------------------------------------------------------------------
// Import Progress Types
// ---------------------------------------------------------------------------

/** Import operation status. */
export type ImportStatus = 'pending' | 'importing' | 'completed' | 'cancelled' | 'error';

/**
 * Tracks progress of an import operation.
 */
export interface ImportProgress {
  /** Total number of rows to import. */
  readonly total: number;
  /** Number of rows processed. */
  processed: number;
  /** Number of successful imports. */
  succeeded: number;
  /** Number of failed imports. */
  failed: number;
  /** Error details for failed rows. */
  readonly errors: ImportError[];
  /** Current import status. */
  status: ImportStatus;
}

/**
 * Details of a single import error.
 */
export interface ImportError {
  /** Row number where the error occurred. */
  readonly row: number;
  /** Key that failed to import. */
  readonly key: string;
  /** Error message. */
  readonly message: string;
}

// ---------------------------------------------------------------------------
// Component Props and Events
// ---------------------------------------------------------------------------

/**
 * Props for the main ImportWizard component.
 */
export interface ImportWizardProps {
  /** Whether the wizard modal is open. */
  open: boolean;
  /** Callback when the wizard is closed. */
  onClose: () => void;
  /** Callback when import is initiated. */
  onImport: (config: ImportConfig, data: ParsedPreview) => Promise<void>;
}

/**
 * Props for FormatSelect step.
 */
export interface FormatSelectProps {
  /** Currently selected format. */
  selectedFormat: ImportFormat | null;
  /** Currently selected JSON format variant. */
  selectedJsonFormat: JsonFormat;
  /** Callback when format is selected. */
  onSelect: (format: ImportFormat, jsonFormat?: JsonFormat) => void;
}

/**
 * Props for FileUpload step.
 */
export interface FileUploadProps {
  /** Expected file format. */
  format: ImportFormat;
  /** JSON format variant. */
  jsonFormat?: JsonFormat;
  /** Currently selected file. */
  file: File | null;
  /** Whether file is being parsed. */
  loading: boolean;
  /** Parse error message. */
  error: string | null;
  /** Callback when file is selected. */
  onFileSelect: (file: File) => void;
  /** Callback when file is removed. */
  onFileRemove: () => void;
}

/**
 * Props for FieldMapping step.
 */
export interface FieldMappingProps {
  /** Parsed file preview data. */
  preview: ParsedPreview;
  /** Currently selected key field. */
  keyField: string;
  /** Value extraction mode. */
  valueMode: ValueMode;
  /** Currently selected value field. */
  valueField: string;
  /** Whether to skip header row. */
  skipHeader: boolean;
  /** Whether to create missing keys. */
  createMissing: boolean;
  /** Whether to update existing keys. */
  updateExisting: boolean;
  /** File format (affects available options). */
  format: ImportFormat;
  /** Callback when mapping changes. */
  onChange: (mapping: FieldMappingUpdate) => void;
}

/**
 * Partial update for field mapping.
 */
export interface FieldMappingUpdate {
  readonly keyField?: string;
  readonly valueMode?: ValueMode;
  readonly valueField?: string;
  readonly skipHeader?: boolean;
  readonly createMissing?: boolean;
  readonly updateExisting?: boolean;
}

/**
 * Props for ImportReview step.
 */
export interface ImportReviewProps {
  /** Import configuration. */
  config: ImportConfig;
  /** Parsed preview data. */
  preview: ParsedPreview;
  /** Whether import is transactional. */
  transactional: boolean;
  /** Callback when transactional mode changes. */
  onTransactionalChange: (transactional: boolean) => void;
  /** Callback when import is started. */
  onStartImport: () => void;
}

/**
 * Props for ImportProgress step.
 */
export interface ImportProgressProps {
  /** Current progress state. */
  progress: ImportProgress;
  /** Callback to cancel import. */
  onCancel: () => void;
  /** Callback to close wizard. */
  onClose: () => void;
}

// ---------------------------------------------------------------------------
// Parser Return Types
// ---------------------------------------------------------------------------

/**
 * Result of a parse operation.
 */
export type ParseResult =
  | { readonly success: true; readonly data: ParsedPreview }
  | { readonly success: false; readonly error: string };

/**
 * Parser function signature.
 */
export type ParserFunction = (file: File) => Promise<ParseResult>;

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Maximum file size in bytes (100MB). */
export const MAX_FILE_SIZE = 100 * 1024 * 1024;

/** Number of preview rows to display. */
export const PREVIEW_ROWS = 5;

/** Large import threshold for warnings. */
export const LARGE_IMPORT_THRESHOLD = 1000;

/** Wizard step labels. */
export const STEP_LABELS: Record<WizardStep, string> = {
  format: 'Format',
  upload: 'Upload',
  mapping: 'Mapping',
  review: 'Review',
  progress: 'Import',
} as const;

/** Wizard steps in order. */
export const WIZARD_STEPS: readonly WizardStep[] = [
  'format',
  'upload',
  'mapping',
  'review',
  'progress',
] as const;
