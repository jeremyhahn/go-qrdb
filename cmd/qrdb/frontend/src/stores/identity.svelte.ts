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
 * Identity state store.
 *
 * Holds the current user identity extracted from the mTLS client certificate.
 * Components use this store to drive authorization-aware UI rendering (e.g.
 * hiding admin panels when the user lacks the required permission).
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Shape of the reactive identity state. */
export interface IdentityState {
    /** Whether identity information has been loaded. */
    loaded: boolean;
    /** Username from the X.509 certificate CN field. */
    username: string;
    /** Tenant identifier extracted from the certificate OU field. */
    tenantId: string;
    /** Roles assigned to the current user. */
    roles: string[];
    /** Flat list of permissions derived from the user's roles. */
    permissions: string[];
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive identity state. */
export const identityStore: IdentityState = $state({
    loaded: false,
    username: '',
    tenantId: '',
    roles: [] as string[],
    permissions: [] as string[],
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Populates the identity store with the authenticated user's details.
 *
 * @param username    - Username from the client certificate.
 * @param tenantId   - Tenant identifier from the client certificate.
 * @param roles      - Roles assigned to the user.
 * @param permissions - Permissions derived from the user's roles.
 */
export function setIdentity(
    username: string,
    tenantId: string,
    roles: string[],
    permissions: string[],
): void {
    identityStore.loaded = true;
    identityStore.username = username;
    identityStore.tenantId = tenantId;
    identityStore.roles = roles;
    identityStore.permissions = permissions;
}

/**
 * Checks whether the current user holds a specific permission.
 *
 * @param permission - Permission string to check (e.g. "kv:write").
 * @returns `true` when the user's permission set includes the given value.
 */
export function hasPermission(permission: string): boolean {
    return identityStore.permissions.includes(permission);
}

/** Resets the identity store to its initial unauthenticated state. */
export function clearIdentity(): void {
    identityStore.loaded = false;
    identityStore.username = '';
    identityStore.tenantId = '';
    identityStore.roles = [];
    identityStore.permissions = [];
}
