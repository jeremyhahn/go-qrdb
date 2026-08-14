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
 * Archive file parser for the ImportWizard.
 *
 * MVP Implementation:
 * - Currently provides placeholder functionality
 * - Shows message that archive import processes all files
 *
 * Future Implementation:
 * - Extract and list files from ZIP and .tar.gz archives
 * - Let users select which files to import
 * - Parse each file according to its format
 */

import type { ParsedPreview, ParseResult } from '../types.ts';

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Supported archive extensions. */
export const ARCHIVE_EXTENSIONS = ['.zip', '.tar.gz', '.tgz'] as const;

/** Archive MIME types. */
export const ARCHIVE_MIME_TYPES = [
  'application/zip',
  'application/x-zip-compressed',
  'application/gzip',
  'application/x-gzip',
  'application/x-tar',
] as const;

// ---------------------------------------------------------------------------
// Type Definitions
// ---------------------------------------------------------------------------

/** Archive file entry (for future implementation). */
export interface ArchiveEntry {
  /** File name within the archive. */
  readonly name: string;
  /** File path within the archive. */
  readonly path: string;
  /** File size in bytes. */
  readonly size: number;
  /** Whether this is a directory. */
  readonly isDirectory: boolean;
  /** Detected file format (csv, json, etc.). */
  readonly format: 'csv' | 'json' | 'unknown';
}

/** Archive analysis result (for future implementation). */
export interface ArchiveAnalysis {
  /** Total number of files in the archive. */
  readonly totalFiles: number;
  /** Files that can be imported. */
  readonly importableFiles: ArchiveEntry[];
  /** Files that cannot be imported. */
  readonly skippedFiles: ArchiveEntry[];
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Parses an archive file and returns a preview.
 *
 * MVP Implementation:
 * This is a placeholder that returns a preview indicating archive import
 * will process all files in the archive. Full extraction and file listing
 * will be implemented in a future version.
 *
 * @param file - Archive file to parse.
 * @returns Promise resolving to parse result.
 */
export async function parseArchive(file: File): Promise<ParseResult> {
  // Validate file extension
  const fileName = file.name.toLowerCase();
  const isValidExtension = ARCHIVE_EXTENSIONS.some((ext) =>
    fileName.endsWith(ext)
  );

  if (!isValidExtension) {
    return {
      success: false,
      error: `Unsupported archive format. Expected: ${ARCHIVE_EXTENSIONS.join(', ')}`,
    };
  }

  // Validate file size
  if (file.size === 0) {
    return {
      success: false,
      error: 'Archive file is empty',
    };
  }

  // MVP: Return placeholder preview
  // In a full implementation, this would extract and list files

  const archiveType = fileName.endsWith('.zip') ? 'ZIP' : 'TAR.GZ';

  const preview: ParsedPreview = {
    headers: ['file', 'type', 'status'],
    rows: [
      {
        file: file.name,
        type: archiveType,
        status: 'Ready for import',
      },
    ],
    totalRows: 1,
    format: 'archive',
  };

  return {
    success: true,
    data: preview,
  };
}

/**
 * Checks if a file is a supported archive format.
 *
 * @param file - File to check.
 * @returns True if the file is a supported archive.
 */
export function isArchiveFile(file: File): boolean {
  const fileName = file.name.toLowerCase();

  // Check by extension
  const hasValidExtension = ARCHIVE_EXTENSIONS.some((ext) =>
    fileName.endsWith(ext)
  );

  if (hasValidExtension) {
    return true;
  }

  // Check by MIME type
  if (file.type) {
    return ARCHIVE_MIME_TYPES.some((mime) => file.type === mime);
  }

  return false;
}

/**
 * Gets a human-readable description of archive import behavior.
 *
 * @returns Description string for UI display.
 */
export function getArchiveImportDescription(): string {
  return (
    'Archive import will process all supported files (CSV, JSON) within ' +
    'the archive. Each file will be imported using its detected format. ' +
    'Nested directories within the archive are supported.'
  );
}

/**
 * Gets the supported archive formats for display.
 *
 * @returns Array of format descriptions.
 */
export function getSupportedArchiveFormats(): string[] {
  return [
    'ZIP (.zip)',
    'Gzipped TAR (.tar.gz, .tgz)',
  ];
}

// ---------------------------------------------------------------------------
// Future Implementation Stubs
// ---------------------------------------------------------------------------

/**
 * Lists files in an archive without extracting.
 *
 * NOTE: This is a stub for future implementation.
 *
 * @param _file - Archive file to analyze.
 * @returns Promise resolving to archive analysis.
 */
export async function listArchiveContents(
  _file: File
): Promise<ArchiveAnalysis> {
  // TODO: Implement actual archive listing using JSZip for ZIP files
  // and pako/tar-stream for TAR.GZ files

  return {
    totalFiles: 0,
    importableFiles: [],
    skippedFiles: [],
  };
}

/**
 * Extracts a specific file from an archive.
 *
 * NOTE: This is a stub for future implementation.
 *
 * @param _archive - Archive file.
 * @param _path - Path of file to extract.
 * @returns Promise resolving to extracted file content.
 */
export async function extractArchiveFile(
  _archive: File,
  _path: string
): Promise<string | null> {
  // TODO: Implement actual file extraction

  return null;
}
