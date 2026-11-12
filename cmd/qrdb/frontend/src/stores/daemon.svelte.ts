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
 * Daemon state store.
 *
 * Tracks the qrdbd daemon process lifecycle: running status, PID, log file
 * path, recent log output, and operation-in-progress / error state.
 */

import type { DaemonStatus } from "@qrdb/sdk";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Shape of the reactive daemon state. */
export interface DaemonState {
  /** Whether the daemon is running. */
  running: boolean;
  /** Daemon process ID when running. */
  pid: number | null;
  /** Path to the daemon log file. */
  logFile: string | null;
  /** Recent daemon logs. */
  logs: string;
  /** Whether a daemon operation is in progress. */
  loading: boolean;
  /** Most recent error message, if any. */
  error: string | null;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive daemon state. */
export const daemonStore: DaemonState = $state({
  running: false,
  pid: null,
  logFile: null,
  logs: "",
  loading: false,
  error: null,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Updates the daemon store from a server-provided DaemonStatus response.
 *
 * @param status - The status returned by `DaemonClient.daemonStatus()`.
 */
export function updateStatus(status: DaemonStatus): void {
  daemonStore.running = status.running;
  daemonStore.pid = status.pid ?? null;
  daemonStore.logFile = status.logFile ?? null;
}

/**
 * Replaces the current log content in the store.
 *
 * @param logs - Raw log text from `DaemonClient.daemonLogs()`.
 */
export function setLogs(logs: string): void {
  daemonStore.logs = logs;
}

/**
 * Sets the loading flag, indicating an operation is in progress.
 *
 * @param loading - Whether an operation is currently running.
 */
export function setLoading(loading: boolean): void {
  daemonStore.loading = loading;
}

/**
 * Sets the most recent error message.
 *
 * @param error - Error message string, or null to clear.
 */
export function setError(error: string | null): void {
  daemonStore.error = error;
}

/** Clears the current error message. */
export function clearError(): void {
  daemonStore.error = null;
}
