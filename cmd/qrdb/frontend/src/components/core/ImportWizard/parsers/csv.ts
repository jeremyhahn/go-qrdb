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
 * CSV file parser for the ImportWizard.
 *
 * Provides parsing capabilities with:
 * - Auto-detection of delimiter (comma, semicolon, tab, pipe)
 * - Proper handling of quoted values
 * - Preview extraction (first 5-10 rows)
 * - Total row counting
 */

import type {
  ParsedPreview,
  ParseResult,
  CsvDelimiter,
  DelimiterDetection,
} from "../types.ts";
import { PREVIEW_ROWS } from "../types.ts";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Supported CSV delimiters in order of preference. */
const DELIMITERS: readonly CsvDelimiter[] = [",", ";", "\t", "|"] as const;

/** Minimum sample lines for delimiter detection. */
const MIN_SAMPLE_LINES = 3;

/** Maximum characters to read for delimiter detection. */
const DETECTION_SAMPLE_SIZE = 8192;

// ---------------------------------------------------------------------------
// Delimiter Detection
// ---------------------------------------------------------------------------

/**
 * Detects the delimiter used in a CSV file.
 *
 * Analyzes the first few lines of the file to determine which delimiter
 * produces the most consistent column count across rows.
 *
 * @param content - File content to analyze.
 * @returns Detected delimiter with confidence score.
 */
function detectDelimiter(content: string): DelimiterDetection {
  const lines = content.split("\n").filter((line) => line.trim().length > 0);
  const sampleLines = lines.slice(
    0,
    Math.min(lines.length, MIN_SAMPLE_LINES + 2),
  );

  if (sampleLines.length < 2) {
    return { delimiter: ",", confidence: 0.5 };
  }

  let bestDelimiter: CsvDelimiter = ",";
  let bestScore = 0;

  for (const delimiter of DELIMITERS) {
    const counts = sampleLines.map((line) =>
      countFieldsWithDelimiter(line, delimiter),
    );
    const score = calculateConsistencyScore(counts);

    if (score > bestScore) {
      bestScore = score;
      bestDelimiter = delimiter;
    }
  }

  return {
    delimiter: bestDelimiter,
    confidence: bestScore,
  };
}

/**
 * Counts fields in a line using a specific delimiter, respecting quotes.
 */
function countFieldsWithDelimiter(
  line: string,
  delimiter: CsvDelimiter,
): number {
  const fields = parseCSVLine(line, delimiter);
  return fields.length;
}

/**
 * Calculates a consistency score for field counts.
 *
 * Higher scores indicate more consistent field counts across rows.
 */
function calculateConsistencyScore(counts: number[]): number {
  if (counts.length < 2) {
    return 0;
  }

  const firstCount = counts[0] ?? 0;
  if (firstCount <= 1) {
    return 0;
  }

  const matchingRows = counts.filter((count) => count === firstCount).length;
  const consistency = matchingRows / counts.length;
  const fieldBonus = Math.min(firstCount / 10, 0.3);

  return consistency + fieldBonus;
}

// ---------------------------------------------------------------------------
// CSV Parsing
// ---------------------------------------------------------------------------

/**
 * Parses a single CSV line respecting quoted values.
 *
 * Handles:
 * - Quoted fields with delimiters inside
 * - Escaped quotes (double quotes)
 * - Mixed quoted and unquoted fields
 *
 * @param line - CSV line to parse.
 * @param delimiter - Field delimiter.
 * @returns Array of field values.
 */
function parseCSVLine(line: string, delimiter: CsvDelimiter): string[] {
  const fields: string[] = [];
  let current = "";
  let inQuotes = false;
  let i = 0;

  while (i < line.length) {
    const char = line[i];
    const nextChar = line[i + 1];

    if (inQuotes) {
      if (char === '"') {
        if (nextChar === '"') {
          // Escaped quote
          current += '"';
          i += 2;
          continue;
        } else {
          // End of quoted field
          inQuotes = false;
          i++;
          continue;
        }
      } else {
        current += char;
        i++;
        continue;
      }
    } else {
      if (char === '"' && current.length === 0) {
        // Start of quoted field
        inQuotes = true;
        i++;
        continue;
      } else if (char === delimiter) {
        // End of field
        fields.push(current.trim());
        current = "";
        i++;
        continue;
      } else {
        current += char;
        i++;
        continue;
      }
    }
  }

  // Add the last field
  fields.push(current.trim());

  return fields;
}

/**
 * Parses CSV content into rows.
 *
 * @param content - Full CSV content.
 * @param delimiter - Field delimiter.
 * @returns Array of parsed rows as string arrays.
 */
function parseCSVContent(content: string, delimiter: CsvDelimiter): string[][] {
  const lines = content.split("\n");
  const rows: string[][] = [];

  for (const line of lines) {
    const trimmedLine = line.trim();
    if (trimmedLine.length === 0) {
      continue;
    }

    const fields = parseCSVLine(trimmedLine, delimiter);
    rows.push(fields);
  }

  return rows;
}

/**
 * Converts parsed rows to record format using headers.
 *
 * @param headers - Column headers.
 * @param rows - Parsed row arrays.
 * @returns Array of records with header keys.
 */
function rowsToRecords(
  headers: string[],
  rows: string[][],
): Record<string, string>[] {
  return rows.map((row) => {
    const record: Record<string, string> = {};
    headers.forEach((header, index) => {
      record[header] = row[index] ?? "";
    });
    return record;
  });
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Parses a CSV file and returns a preview.
 *
 * Features:
 * - Auto-detects delimiter (comma, semicolon, tab, pipe)
 * - Handles quoted values with embedded delimiters
 * - Returns first 5-10 rows for preview
 * - Counts total rows in the file
 *
 * @param file - CSV file to parse.
 * @returns Promise resolving to parse result.
 */
export async function parseCSV(file: File): Promise<ParseResult> {
  try {
    const content = await file.text();

    if (content.trim().length === 0) {
      return {
        success: false,
        error: "File is empty",
      };
    }

    // Detect delimiter
    const sampleContent = content.slice(0, DETECTION_SAMPLE_SIZE);
    const { delimiter } = detectDelimiter(sampleContent);

    // Parse all rows
    const allRows = parseCSVContent(content, delimiter);

    if (allRows.length === 0) {
      return {
        success: false,
        error: "No valid rows found in file",
      };
    }

    // Extract headers from first row
    const headers = allRows[0];
    if (headers === undefined || headers.length === 0) {
      return {
        success: false,
        error: "No columns found in file",
      };
    }

    const dataRows = allRows.slice(1);

    // Get preview rows
    const previewRows = dataRows.slice(0, PREVIEW_ROWS);
    const records = rowsToRecords(headers, previewRows);

    const preview: ParsedPreview = {
      headers,
      rows: records,
      totalRows: dataRows.length,
      format: "csv",
    };

    return {
      success: true,
      data: preview,
    };
  } catch (error) {
    const message =
      error instanceof Error ? error.message : "Unknown error parsing CSV";
    return {
      success: false,
      error: `Failed to parse CSV: ${message}`,
    };
  }
}

/**
 * Parses a CSV file with a specific delimiter.
 *
 * Use this when the delimiter is known in advance.
 *
 * @param file - CSV file to parse.
 * @param delimiter - Known delimiter to use.
 * @returns Promise resolving to parse result.
 */
export async function parseCSVWithDelimiter(
  file: File,
  delimiter: CsvDelimiter,
): Promise<ParseResult> {
  try {
    const content = await file.text();

    if (content.trim().length === 0) {
      return {
        success: false,
        error: "File is empty",
      };
    }

    const allRows = parseCSVContent(content, delimiter);

    if (allRows.length === 0) {
      return {
        success: false,
        error: "No valid rows found in file",
      };
    }

    const headers = allRows[0];
    if (headers === undefined || headers.length === 0) {
      return {
        success: false,
        error: "No columns found in file",
      };
    }

    const dataRows = allRows.slice(1);

    const previewRows = dataRows.slice(0, PREVIEW_ROWS);
    const records = rowsToRecords(headers, previewRows);

    const preview: ParsedPreview = {
      headers,
      rows: records,
      totalRows: dataRows.length,
      format: "csv",
    };

    return {
      success: true,
      data: preview,
    };
  } catch (error) {
    const message =
      error instanceof Error ? error.message : "Unknown error parsing CSV";
    return {
      success: false,
      error: `Failed to parse CSV: ${message}`,
    };
  }
}

/**
 * Detects the delimiter in a CSV file without full parsing.
 *
 * Useful for displaying detected delimiter to users.
 *
 * @param file - CSV file to analyze.
 * @returns Promise resolving to delimiter detection result.
 */
export async function detectCSVDelimiter(
  file: File,
): Promise<DelimiterDetection> {
  const reader = file.stream().getReader();
  const decoder = new TextDecoder();
  let sample = "";

  while (sample.length < DETECTION_SAMPLE_SIZE) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    sample += decoder.decode(value, { stream: true });
  }

  reader.cancel();
  return detectDelimiter(sample);
}

/**
 * Gets a human-readable name for a delimiter.
 */
export function getDelimiterName(delimiter: CsvDelimiter): string {
  const names: Record<CsvDelimiter, string> = {
    ",": "Comma",
    ";": "Semicolon",
    "\t": "Tab",
    "|": "Pipe",
  };
  return names[delimiter];
}
