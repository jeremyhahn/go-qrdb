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
 * Capabilities state store.
 *
 * Caches the compile-time feature set returned by the QRDB server so that
 * UI components can conditionally render sections based on available features
 * (e.g. hiding the time-series panel when the binary was compiled without it).
 */

import type { AdminClient } from '@qrdb/sdk';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Known feature flag names matching CapabilitiesResponse fields. */
export type FeatureName =
    | 'kv'
    | 'mvcc'
    | 'transaction'
    | 'batch'
    | 'watch'
    | 'lease'
    | 'timeSeries'
    | 'vector';

/** Shape of the reactive capabilities state. */
export interface CapabilitiesState {
    /** Whether capabilities have been fetched from the server. */
    loaded: boolean;
    /** Basic key-value operations. */
    kv: boolean;
    /** MVCC revision tracking. */
    mvcc: boolean;
    /** Compare-and-swap transactions. */
    transaction: boolean;
    /** Atomic batch operations. */
    batch: boolean;
    /** Watch event streaming. */
    watch: boolean;
    /** Distributed lease management. */
    lease: boolean;
    /** Time-series data operations. */
    timeSeries: boolean;
    /** Vector database operations. */
    vector: boolean;
}

// ---------------------------------------------------------------------------
// Feature lookup map
// ---------------------------------------------------------------------------

/**
 * Maps feature name strings to accessor functions on the store. Using a
 * dispatch map avoids a switch statement and keeps lookups O(1).
 */
const featureAccessors: Readonly<Record<FeatureName, () => boolean>> = {
    kv: () => capabilitiesStore.kv,
    mvcc: () => capabilitiesStore.mvcc,
    transaction: () => capabilitiesStore.transaction,
    batch: () => capabilitiesStore.batch,
    watch: () => capabilitiesStore.watch,
    lease: () => capabilitiesStore.lease,
    timeSeries: () => capabilitiesStore.timeSeries,
    vector: () => capabilitiesStore.vector,
};

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive capabilities state. */
export const capabilitiesStore: CapabilitiesState = $state({
    loaded: false,
    kv: true,
    mvcc: true,
    transaction: true,
    batch: true,
    watch: true,
    lease: true,
    timeSeries: false,
    vector: false,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Fetches the current capabilities from the server and updates the store.
 *
 * @param client - An AdminClient instance used to query capabilities.
 */
export async function refreshCapabilities(client: AdminClient): Promise<void> {
    const caps = await client.capabilities();
    capabilitiesStore.kv = caps.kv;
    capabilitiesStore.mvcc = caps.mvcc;
    capabilitiesStore.transaction = caps.transaction;
    capabilitiesStore.batch = caps.batch;
    capabilitiesStore.watch = caps.watch;
    capabilitiesStore.lease = caps.lease;
    capabilitiesStore.timeSeries = caps.timeSeries;
    capabilitiesStore.vector = caps.vector;
    capabilitiesStore.loaded = true;
}

/**
 * Returns whether a named feature is enabled.
 *
 * @param feature - Feature name to check (must be a valid FeatureName).
 * @returns `true` when the feature is enabled, `false` otherwise (including
 *          for unknown feature names).
 */
export function hasFeature(feature: string): boolean {
    const accessor = featureAccessors[feature as FeatureName];
    if (accessor === undefined) {
        return false;
    }
    return accessor();
}
