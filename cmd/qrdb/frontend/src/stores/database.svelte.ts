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
 * Database selection state store.
 *
 * Manages the list of available databases and the currently selected
 * database. Shared between the sidebar (for list display) and the
 * workspace (for current database context).
 */

import type { DatabaseInfo } from "@qrdb/sdk";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Shape of the reactive database state. */
export interface DatabaseState {
    /** Available databases fetched from the server. */
    databases: DatabaseInfo[];
    /** Currently selected database, or null if none. */
    selectedDatabase: DatabaseInfo | null;
    /** Whether the database list is being loaded. */
    loading: boolean;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive database state. */
export const databaseStore: DatabaseState = $state({
    databases: [] as DatabaseInfo[],
    selectedDatabase: null,
    loading: false,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Replaces the database list with a new set of databases.
 *
 * @param databases - The full list of databases from the server.
 */
export function setDatabases(databases: DatabaseInfo[]): void {
    databaseStore.databases = databases;
}

/**
 * Selects a database for the workspace view.
 *
 * @param database - The database to select.
 */
export function selectDatabase(database: DatabaseInfo): void {
    databaseStore.selectedDatabase = database;
}

/**
 * Replaces a single database entry in the list (matched by name) with an
 * updated snapshot, keeping selectedDatabase in sync when it is the same
 * database. Used after server-side mutations that change database metadata
 * (e.g. enabling a SQL wire protocol) so every consumer deriving from the
 * store — such as the workspace tab bar gating the SQL Browser tab — reacts
 * immediately instead of waiting for a full list reload.
 *
 * @param database - The updated database snapshot from the server.
 */
export function updateDatabase(database: DatabaseInfo): void {
    databaseStore.databases = databaseStore.databases.map((d) =>
        d.name === database.name ? database : d,
    );
    if (databaseStore.selectedDatabase?.name === database.name) {
        databaseStore.selectedDatabase = database;
    }
}

/** Clears the current database selection. */
export function clearSelection(): void {
    databaseStore.selectedDatabase = null;
}

/**
 * Sets the loading state for the database list.
 *
 * @param loading - Whether databases are currently being fetched.
 */
export function setLoading(loading: boolean): void {
    databaseStore.loading = loading;
}
