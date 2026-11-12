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
 * JSON file parser for the ImportWizard.
 *
 * Supports three JSON format variants:
 * - Single Object: A single JSON object with key-value pairs
 * - Array: An array of objects
 * - NDJSON: Newline-delimited JSON (one object per line)
 */

import type { ParsedPreview, ParseResult, JsonFormat } from "../types.ts";
import { PREVIEW_ROWS } from "../types.ts";

// ---------------------------------------------------------------------------
// Type Guards
// ---------------------------------------------------------------------------

/**
 * Checks if a value is a plain object.
 */
function isPlainObject(value: unknown): value is Record<string, unknown> {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.prototype.toString.call(value) === "[object Object]"
  );
}

/**
 * Checks if a value is an array of objects.
 */
function isObjectArray(value: unknown): value is Record<string, unknown>[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.every((item) => isPlainObject(item))
  );
}

// ---------------------------------------------------------------------------
// Object Normalization
// ---------------------------------------------------------------------------

/**
 * Converts an object's values to strings for consistent handling.
 */
function normalizeObjectValues(
  obj: Record<string, unknown>,
): Record<string, string> {
  const result: Record<string, string> = {};

  for (const key of Object.keys(obj)) {
    const value = obj[key];

    if (value === null || value === undefined) {
      result[key] = "";
    } else if (typeof value === "string") {
      result[key] = value;
    } else if (typeof value === "object") {
      result[key] = JSON.stringify(value);
    } else {
      result[key] = String(value);
    }
  }

  return result;
}

/**
 * Extracts all unique field names from an array of objects.
 */
function extractHeaders(objects: Record<string, unknown>[]): string[] {
  const headerSet = new Set<string>();

  for (const obj of objects) {
    for (const key of Object.keys(obj)) {
      headerSet.add(key);
    }
  }

  return Array.from(headerSet);
}

// ---------------------------------------------------------------------------
// Format Parsers
// ---------------------------------------------------------------------------

/**
 * Parses a single JSON object format.
 *
 * Expects a file containing a single JSON object where each property
 * represents a key-value pair to import.
 *
 * Example: { "key1": "value1", "key2": "value2" }
 */
async function parseSingleObject(content: string): Promise<ParseResult> {
  try {
    const parsed = JSON.parse(content);

    if (!isPlainObject(parsed)) {
      return {
        success: false,
        error: "Expected a JSON object, but found a different type",
      };
    }

    // Convert object to array of key-value pairs
    const entries = Object.entries(parsed);

    if (entries.length === 0) {
      return {
        success: false,
        error: "JSON object is empty",
      };
    }

    // Create rows with key and value columns
    const rows: Record<string, string>[] = entries.map(([key, value]) => ({
      key,
      value: typeof value === "string" ? value : JSON.stringify(value),
    }));

    const previewRows = rows.slice(0, PREVIEW_ROWS);

    const preview: ParsedPreview = {
      headers: ["key", "value"],
      rows: previewRows,
      totalRows: entries.length,
      format: "json",
    };

    return {
      success: true,
      data: preview,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : "Invalid JSON";
    return {
      success: false,
      error: `Failed to parse JSON object: ${message}`,
    };
  }
}

/**
 * Parses a JSON array format.
 *
 * Expects a file containing an array of objects.
 *
 * Example: [{ "id": "1", "name": "Alice" }, { "id": "2", "name": "Bob" }]
 */
async function parseArray(content: string): Promise<ParseResult> {
  try {
    const parsed = JSON.parse(content);

    if (!Array.isArray(parsed)) {
      return {
        success: false,
        error: "Expected a JSON array, but found a different type",
      };
    }

    if (parsed.length === 0) {
      return {
        success: false,
        error: "JSON array is empty",
      };
    }

    if (!isObjectArray(parsed)) {
      return {
        success: false,
        error: "Expected an array of objects, but found non-object elements",
      };
    }

    const headers = extractHeaders(parsed);
    const previewObjects = parsed.slice(0, PREVIEW_ROWS);
    const rows = previewObjects.map((obj) => normalizeObjectValues(obj));

    const preview: ParsedPreview = {
      headers,
      rows,
      totalRows: parsed.length,
      format: "json",
    };

    return {
      success: true,
      data: preview,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : "Invalid JSON";
    return {
      success: false,
      error: `Failed to parse JSON array: ${message}`,
    };
  }
}

/**
 * Parses NDJSON (Newline-Delimited JSON) format.
 *
 * Each line contains a separate JSON object.
 *
 * Example:
 * {"id": "1", "name": "Alice"}
 * {"id": "2", "name": "Bob"}
 */
async function parseNDJSON(content: string): Promise<ParseResult> {
  try {
    const lines = content.split("\n").filter((line) => line.trim().length > 0);

    if (lines.length === 0) {
      return {
        success: false,
        error: "File is empty",
      };
    }

    const objects: Record<string, unknown>[] = [];
    const errors: string[] = [];

    for (let i = 0; i < lines.length; i++) {
      const rawLine = lines[i];
      if (rawLine === undefined) {
        continue;
      }
      const line = rawLine.trim();
      if (line.length === 0) {
        continue;
      }

      try {
        const parsed = JSON.parse(line);

        if (!isPlainObject(parsed)) {
          errors.push(`Line ${i + 1}: Expected an object`);
          continue;
        }

        objects.push(parsed);
      } catch {
        errors.push(`Line ${i + 1}: Invalid JSON`);
      }
    }

    if (objects.length === 0) {
      const errorSummary = errors.slice(0, 3).join("; ");
      return {
        success: false,
        error: `No valid objects found. ${errorSummary}`,
      };
    }

    const headers = extractHeaders(objects);
    const previewObjects = objects.slice(0, PREVIEW_ROWS);
    const rows = previewObjects.map((obj) => normalizeObjectValues(obj));

    const preview: ParsedPreview = {
      headers,
      rows,
      totalRows: objects.length,
      format: "json",
    };

    return {
      success: true,
      data: preview,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : "Unknown error";
    return {
      success: false,
      error: `Failed to parse NDJSON: ${message}`,
    };
  }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Parses a JSON file according to the specified format.
 *
 * @param file - JSON file to parse.
 * @param format - Expected JSON format variant.
 * @returns Promise resolving to parse result.
 */
export async function parseJSON(
  file: File,
  format: JsonFormat,
): Promise<ParseResult> {
  try {
    const content = await file.text();

    if (content.trim().length === 0) {
      return {
        success: false,
        error: "File is empty",
      };
    }

    switch (format) {
      case "object":
        return await parseSingleObject(content);

      case "array":
        return await parseArray(content);

      case "ndjson":
        return await parseNDJSON(content);

      default: {
        // Exhaustive check
        const exhaustiveCheck: never = format;
        return {
          success: false,
          error: `Unknown JSON format: ${exhaustiveCheck}`,
        };
      }
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : "Unknown error";
    return {
      success: false,
      error: `Failed to read file: ${message}`,
    };
  }
}

/**
 * Attempts to auto-detect the JSON format.
 *
 * Analyzes the file content to determine if it's:
 * - A single object
 * - An array of objects
 * - NDJSON
 *
 * @param file - JSON file to analyze.
 * @returns Promise resolving to detected format or null if unknown.
 */
export async function detectJSONFormat(file: File): Promise<JsonFormat | null> {
  try {
    const content = await file.text();
    const trimmed = content.trim();

    if (trimmed.length === 0) {
      return null;
    }

    // Check for NDJSON (multiple lines, each starting with {)
    const lines = trimmed.split("\n").filter((line) => line.trim().length > 0);
    if (lines.length > 1) {
      const allLinesStartWithBrace = lines.every((line) =>
        line.trim().startsWith("{"),
      );
      if (allLinesStartWithBrace) {
        return "ndjson";
      }
    }

    // Try parsing as JSON
    try {
      const parsed = JSON.parse(trimmed);

      if (Array.isArray(parsed)) {
        return "array";
      }

      if (isPlainObject(parsed)) {
        return "object";
      }

      return null;
    } catch {
      // If single JSON parse fails but we have multiple lines, likely NDJSON
      if (lines.length > 1) {
        return "ndjson";
      }
      return null;
    }
  } catch {
    return null;
  }
}

/**
 * Gets a human-readable description for a JSON format.
 */
export function getJSONFormatDescription(format: JsonFormat): string {
  const descriptions: Record<JsonFormat, string> = {
    object: "Single JSON object with key-value pairs",
    array: "Array of JSON objects",
    ndjson: "Newline-delimited JSON (one object per line)",
  };
  return descriptions[format];
}

/**
 * Gets an example for a JSON format.
 */
export function getJSONFormatExample(format: JsonFormat): string {
  const examples: Record<JsonFormat, string> = {
    object: '{ "key1": "value1", "key2": "value2" }',
    array: '[{ "id": "1", "name": "Alice" }, { "id": "2", "name": "Bob" }]',
    ndjson: '{"id": "1", "name": "Alice"}\n{"id": "2", "name": "Bob"}',
  };
  return examples[format];
}
