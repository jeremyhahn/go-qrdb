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
 * Seal status state store.
 *
 * Tracks whether the QRDB server's encryption barrier is sealed or unsealed,
 * the Shamir share progress during an unseal ceremony, and whether
 * background polling is active.
 */

import type { SealStatus, OfficerStatus } from '@qrdb/sdk';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Shape of the reactive seal state. */
export interface SealState {
    /** Whether the server's encryption barrier is currently sealed. */
    sealed: boolean;
    /** Whether the seal subsystem has been initialized (bootstrap complete). */
    initialized: boolean;
    /**
     * Active sealing strategy: "none", "passphrase", or "shamir". Empty before
     * initialization. Drives whether the unseal UI prompts for a passphrase or
     * for Shamir key shares.
     */
    strategy: string;
    /** Number of Shamir shares provided toward the current unseal ceremony. */
    sharesProvided: number;
    /** Number of Shamir shares required to unseal (M in M-of-N). */
    sharesRequired: number;
    /** Total number of Shamir shares generated (N in M-of-N). */
    sharesTotal: number;
    /** Whether background seal status polling is active. */
    polling: boolean;
    /**
     * Registered Security Officers for the system Shamir barrier.
     * Populated by listOfficers() and refreshed after registrations.
     */
    officers: OfficerStatus[];
    /** Whether officer list is currently being fetched. */
    officersLoading: boolean;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive seal state. */
export const sealStore: SealState = $state({
    sealed: true,
    initialized: false,
    strategy: "",
    sharesProvided: 0,
    sharesRequired: 0,
    sharesTotal: 0,
    polling: false,
    officers: [],
    officersLoading: false,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Updates the seal store from a server-provided SealStatus response.
 *
 * @param status - The seal status returned by `SealClient.status()`.
 */
export function updateSealStatus(status: SealStatus): void {
    sealStore.sealed = status.sealed;
    sealStore.strategy = status.strategy ?? "";
    sealStore.sharesProvided = status.sharesProvided;
    sealStore.sharesRequired = status.sharesRequired;
    sealStore.sharesTotal = status.sharesTotal;

    // Trust the server's `initialized` field. The previous implementation
    // derived it from `sharesTotal > 0`, which falsely classified passphrase-
    // and none-mode barriers as "not initialized" because those modes have no
    // Shamir shares.
    sealStore.initialized = status.initialized;
}

/**
 * Increments the local share counter by one. This provides immediate
 * UI feedback before the server round-trip confirms the share was accepted.
 * The counter is clamped to `sharesRequired` to prevent overshooting.
 */
export function addShare(): void {
    if (sealStore.sharesProvided < sealStore.sharesRequired) {
        sealStore.sharesProvided += 1;
    }
}

/**
 * Replaces the officer list in the store with the latest server response.
 *
 * @param officers - Officer status records returned by listOfficers() or
 *   officerClaimStatus().
 */
export function updateOfficers(officers: OfficerStatus[]): void {
    sealStore.officers = officers;
}

/**
 * Sets the officer loading flag. Used to drive spinner state in the
 * Security Officers panel while a fetch is in flight.
 *
 * @param loading - true while the fetch is outstanding, false on completion.
 */
export function setOfficersLoading(loading: boolean): void {
    sealStore.officersLoading = loading;
}
