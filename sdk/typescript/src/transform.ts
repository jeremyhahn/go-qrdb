// Copyright 2026 Jeremy Hahn
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

// ---------------------------------------------------------------------------
// Wire-format key transformers
// ---------------------------------------------------------------------------

/**
 * Converts a snake_case key to camelCase.
 * Examples: "is_leader" → "isLeader", "node_id" → "nodeId".
 * Keys without underscores are returned unchanged, preserving existing
 * camelCase fields (e.g. those from Go types with explicit camelCase json tags).
 */
export function snakeToCamelKey(key: string): string {
    return key.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase());
}

/**
 * Recursively transforms all object keys from snake_case to camelCase.
 * Arrays are traversed element-by-element; primitives and null are returned
 * as-is without copying.
 *
 * Applied to every JSON response received from the QRDB REST API and Wails
 * IPC so that SDK consumers always see the camelCase field names declared in
 * types.ts, regardless of the server's snake_case JSON serialization.
 */
export function deepSnakeToCamel(value: unknown): unknown {
    if (Array.isArray(value)) {
        return value.map(deepSnakeToCamel);
    }
    if (value !== null && typeof value === 'object') {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
            out[snakeToCamelKey(k)] = deepSnakeToCamel(v);
        }
        return out;
    }
    return value;
}

/**
 * Converts a camelCase key to snake_case.
 * Examples: "isLeader" → "is_leader", "nodeId" → "node_id".
 */
export function camelToSnakeKey(key: string): string {
    return key.replace(/[A-Z]/g, (ch) => '_' + ch.toLowerCase());
}

/**
 * Recursively transforms all object keys from camelCase to snake_case.
 * Used to serialize SDK request objects for the Go REST API and Wails IPC,
 * both of which use snake_case JSON tags on Go structs.
 */
export function deepCamelToSnake(value: unknown): unknown {
    if (Array.isArray(value)) {
        return value.map(deepCamelToSnake);
    }
    if (value !== null && typeof value === 'object') {
        const out: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
            out[camelToSnakeKey(k)] = deepCamelToSnake(v);
        }
        return out;
    }
    return value;
}
