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
 * File parser exports for the ImportWizard.
 *
 * Provides parsing capabilities for:
 * - CSV files with auto-detection of delimiters
 * - JSON files in multiple formats (object, array, NDJSON)
 * - Archive files (ZIP, TAR.GZ) - placeholder for future implementation
 */

// CSV parser
export {
  parseCSV,
  parseCSVWithDelimiter,
  detectCSVDelimiter,
  getDelimiterName,
} from './csv.ts';

// JSON parser
export {
  parseJSON,
  detectJSONFormat,
  getJSONFormatDescription,
  getJSONFormatExample,
} from './json.ts';

// Archive parser
export {
  parseArchive,
  isArchiveFile,
  getArchiveImportDescription,
  getSupportedArchiveFormats,
  listArchiveContents,
  extractArchiveFile,
  ARCHIVE_EXTENSIONS,
  ARCHIVE_MIME_TYPES,
  type ArchiveEntry,
  type ArchiveAnalysis,
} from './archive.ts';
