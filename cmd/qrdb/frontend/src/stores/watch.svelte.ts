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
 * Watch subscriptions state store.
 *
 * Manages active watch subscriptions and buffers incoming events for
 * display in the watch event log. A configurable maximum event count
 * prevents unbounded memory growth.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Represents a single active watch subscription. */
export interface ActiveWatch {
  /** Server-assigned watch identifier. */
  readonly id: number;
  /** Key or key prefix being watched. */
  readonly key: string;
  /** Whether this watch matches keys by prefix. */
  readonly isPrefix: boolean;
}

/** A buffered watch event for UI display. */
export interface BufferedWatchEvent {
  /** Watch subscription that received this event. */
  readonly watchId: number;
  /** Unix timestamp in milliseconds when the event was received. */
  readonly timestamp: number;
  /** Event type string (e.g. "PUT", "DELETE"). */
  readonly type: string;
  /** Key affected by the event. */
  readonly key: string;
  /** New value after the event, if applicable. */
  readonly value?: string | undefined;
}

/** Shape of the reactive watch state. */
export interface WatchState {
  /** Currently active watch subscriptions. */
  watches: ActiveWatch[];
  /** Buffered events received across all active watches. */
  events: BufferedWatchEvent[];
  /** Maximum number of events to retain in the buffer. */
  maxEvents: number;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive watch state. */
export const watchStore: WatchState = $state({
  watches: [] as ActiveWatch[],
  events: [] as BufferedWatchEvent[],
  maxEvents: 1000,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Registers a new active watch subscription.
 *
 * @param watch - The watch subscription to track.
 */
export function addWatch(watch: ActiveWatch): void {
  watchStore.watches = [...watchStore.watches, watch];
}

/**
 * Removes an active watch subscription by its server-assigned ID.
 *
 * @param id - Watch identifier to remove.
 */
export function removeWatch(id: number): void {
  watchStore.watches = watchStore.watches.filter((w) => w.id !== id);
}

/**
 * Appends incoming events to the event buffer, trimming from the front
 * when the buffer exceeds `maxEvents`.
 *
 * @param watchId - Watch subscription that received the events.
 * @param events  - Raw events to buffer.
 */
export function pushEvents(
  watchId: number,
  events: ReadonlyArray<{
    readonly type: string;
    readonly key: string;
    readonly value?: string;
  }>,
): void {
  const now = Date.now();
  const buffered: BufferedWatchEvent[] = events.map((e) => ({
    watchId,
    timestamp: now,
    type: e.type,
    key: e.key,
    value: e.value,
  }));

  const combined = [...watchStore.events, ...buffered];

  // Trim oldest events when the buffer exceeds the configured maximum.
  if (combined.length > watchStore.maxEvents) {
    watchStore.events = combined.slice(combined.length - watchStore.maxEvents);
  } else {
    watchStore.events = combined;
  }
}

/** Clears all buffered events while leaving active watches intact. */
export function clearEvents(): void {
  watchStore.events = [];
}
