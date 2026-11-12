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
 * Toast notification state store.
 *
 * Provides a queue-based toast notification system with automatic dismissal.
 * Each toast has a configurable duration after which it is removed from the
 * queue. Components render the `toasts` array to display active notifications.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Visual severity of a toast notification. */
export type ToastType = "success" | "error" | "info" | "warning";

/** A single toast notification. */
export interface Toast {
  /** Unique identifier for this toast instance. */
  readonly id: number;
  /** Visual severity level. */
  readonly type: ToastType;
  /** Human-readable notification message. */
  readonly message: string;
  /** Duration in milliseconds before automatic dismissal. */
  readonly duration: number;
}

/** Shape of the reactive toast state. */
export interface ToastState {
  /** Active toast notifications, ordered by creation time. */
  toasts: Toast[];
}

// ---------------------------------------------------------------------------
// Internal state
// ---------------------------------------------------------------------------

/** Monotonically increasing counter for unique toast IDs. */
let nextId = 0;

/**
 * Active auto-dismiss timers keyed by toast ID. Tracked separately so
 * that manual dismissals can cancel pending timers without leaking.
 */
const timers = new Map<number, ReturnType<typeof setTimeout>>();

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive toast state. */
export const toastStore: ToastState = $state({
  toasts: [] as Toast[],
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/** Default toast display duration in milliseconds. */
const DEFAULT_DURATION_MS = 5000;

/**
 * Displays a new toast notification and schedules its automatic dismissal.
 *
 * @param type     - Visual severity level.
 * @param message  - Notification message.
 * @param duration - Time in milliseconds before auto-dismiss (defaults to 5000).
 */
export function showToast(
  type: ToastType,
  message: string,
  duration?: number,
): void {
  const id = nextId++;
  const resolvedDuration = duration ?? DEFAULT_DURATION_MS;

  const toast: Toast = {
    id,
    type,
    message,
    duration: resolvedDuration,
  };

  toastStore.toasts = [...toastStore.toasts, toast];

  // Schedule automatic dismissal.
  const timer = setTimeout(() => {
    dismissToast(id);
  }, resolvedDuration);
  timers.set(id, timer);
}

/**
 * Removes a toast by its unique identifier. If an auto-dismiss timer is
 * pending for the toast, it is cleared to prevent double removal.
 *
 * @param id - Toast identifier to dismiss.
 */
export function dismissToast(id: number): void {
  const timer = timers.get(id);
  if (timer !== undefined) {
    clearTimeout(timer);
    timers.delete(id);
  }
  toastStore.toasts = toastStore.toasts.filter((t) => t.id !== id);
}
