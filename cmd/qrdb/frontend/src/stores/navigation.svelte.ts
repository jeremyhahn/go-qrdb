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
 * Navigation state store.
 *
 * Owns the complete navigation context for the application: which server is
 * active, which view within a server is selected, and which database tool is
 * active when a database is open. Decouples routing logic from component state
 * so that any component can read or drive navigation without prop-drilling.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/**
 * The set of server-level administration views.
 * Each value maps 1:1 to an existing admin view component.
 */
export type AdminView =
    | 'dashboard'
    | 'cluster'
    | 'topology'
    | 'seal-manager'
    | 'tenants'
    | 'users'
    | 'roles'
    | 'certificates'
    | 'databases'
    | 'backups'
    | 'rebalance'
    | 'credentials'
    | 'audit-log'
    | 'daemon'
    | 'settings';

/**
 * The set of per-database tool views.
 * Every database exposes every compiled-in engine; availability of individual
 * tools depends on server capability flags, and callers are responsible for
 * filtering.
 */
export type DbTool =
    | 'data'
    | 'collections'
    | 'indexes'
    | 'transactions'
    | 'batch'
    | 'watch'
    | 'leases'
    | 'locks'
    | 'timeseries'
    | 'vector'
    | 'sql'
    | 'db-settings';

/**
 * A discriminated union describing what the content area is currently
 * showing. Components derive their rendering from this single source of
 * truth rather than from multiple independent boolean flags.
 *
 *   none            - No server connected. Shows welcome / first-run screen.
 *   server-admin    - A server-level admin view is active.
 *   database        - A database is open with a specific tool selected.
 *   create-database - The new-database wizard is open for a server.
 */
export type NavContext =
    | { readonly type: 'none' }
    | { readonly type: 'server-admin'; readonly profileId: string; readonly view: AdminView }
    | { readonly type: 'database'; readonly profileId: string; readonly dbName: string; readonly tool: DbTool }
    | { readonly type: 'create-database'; readonly profileId: string };

/** Human-readable labels for admin views used in breadcrumbs and tooltips. */
export const ADMIN_VIEW_LABELS: Readonly<Record<AdminView, string>> = {
    'dashboard': 'Dashboard',
    'cluster': 'Cluster',
    'topology': 'Topology',
    'seal-manager': 'Seal Manager',
    'tenants': 'Tenants',
    'users': 'Users',
    'roles': 'Roles',
    'certificates': 'Certificates',
    'databases': 'Databases',
    'backups': 'Backups',
    'rebalance': 'Rebalance',
    'credentials': 'Credentials',
    'audit-log': 'Audit Log',
    'daemon': 'Daemon',
    'settings': 'Settings',
} as const;

/** Human-readable labels for database tool views. */
export const DB_TOOL_LABELS: Readonly<Record<DbTool, string>> = {
    'data': 'Data',
    'collections': 'Collections',
    'indexes': 'Indexes',
    'transactions': 'Transactions',
    'batch': 'Batch',
    'watch': 'Watch',
    'leases': 'Leases',
    'locks': 'Locks',
    'timeseries': 'Time Series',
    'vector': 'Vectors',
    'sql': 'SQL Browser',
    'db-settings': 'Settings',
} as const;

/** Shape of the full reactive navigation state. */
export interface NavigationState {
    /** The current navigation context driving the content area. */
    context: NavContext;
    /** Set of profile IDs whose server tree nodes are expanded in the sidebar. */
    expandedServers: ReadonlySet<string>;
    /** Whether the primary sidebar is collapsed to icon-rail mode. */
    sidebarCollapsed: boolean;
    /**
     * The profile ID to restore to after a create-database wizard is cancelled.
     * Used to return to the previous context without losing server state.
     */
    priorProfileId: string;
    /**
     * The tool that was active before the create-database wizard opened,
     * used to restore the database workspace if the wizard is cancelled.
     */
    priorDbName: string;
    priorTool: DbTool;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive navigation state. Mutated only through the exported functions. */
export const navStore: NavigationState = $state({
    context: { type: 'none' },
    expandedServers: new Set<string>(),
    sidebarCollapsed: false,
    priorProfileId: '',
    priorDbName: '',
    priorTool: 'data',
});

// ---------------------------------------------------------------------------
// Transitions
// ---------------------------------------------------------------------------

/**
 * Navigates to a server-level admin view, scoped to the given profile.
 *
 * The currently selected database (if any) is preserved in the store so the
 * user can switch back to the database workspace without losing context.
 *
 * @param profileId - The profile whose admin view to show.
 * @param view      - The admin view to activate. Defaults to 'dashboard'.
 */
export function navigateToAdmin(profileId: string, view: AdminView = 'dashboard'): void {
    navStore.context = { type: 'server-admin', profileId, view };
}

/**
 * Navigates to a database tool view.
 *
 * When switching to a database that is already active with a given tool, the
 * tool selection is preserved so that re-clicking the same database node in
 * the sidebar does not reset the tool to 'data'.
 *
 * @param profileId - The profile that owns the database.
 * @param dbName    - The database name.
 * @param tool      - The tool to activate. Defaults to 'data'.
 */
export function navigateToDatabase(profileId: string, dbName: string, tool: DbTool = 'data'): void {
    const ctx = navStore.context;
    // Preserve current tool when re-clicking the already-active database.
    if (
        ctx.type === 'database' &&
        ctx.profileId === profileId &&
        ctx.dbName === dbName
    ) {
        return;
    }
    navStore.context = { type: 'database', profileId, dbName, tool };
}

/**
 * Switches the active tool within the current database workspace.
 *
 * No-ops when the context is not a database view.
 *
 * @param tool - The tool to activate.
 */
export function selectDbTool(tool: DbTool): void {
    const ctx = navStore.context;
    if (ctx.type !== 'database') return;
    navStore.context = { type: 'database', profileId: ctx.profileId, dbName: ctx.dbName, tool };
}

/**
 * Opens the create-database wizard for the given server, preserving the
 * current context so it can be restored on cancel.
 *
 * @param profileId - The profile to create the database under.
 */
export function navigateToCreateDatabase(profileId: string): void {
    const ctx = navStore.context;
    navStore.priorProfileId = ctx.type !== 'none' ? (ctx as { profileId?: string }).profileId ?? '' : '';
    if (ctx.type === 'database') {
        navStore.priorDbName = ctx.dbName;
        navStore.priorTool = ctx.tool;
    } else {
        navStore.priorDbName = '';
        navStore.priorTool = 'data';
    }
    navStore.context = { type: 'create-database', profileId };
}

/**
 * Cancels the create-database wizard, restoring the previous navigation
 * context. If the prior context was a database workspace, that database and
 * tool are restored. Otherwise falls back to the server dashboard.
 */
export function cancelCreateDatabase(): void {
    const { priorProfileId, priorDbName, priorTool } = navStore;
    if (priorDbName !== '' && priorProfileId !== '') {
        navStore.context = { type: 'database', profileId: priorProfileId, dbName: priorDbName, tool: priorTool };
    } else if (priorProfileId !== '') {
        navStore.context = { type: 'server-admin', profileId: priorProfileId, view: 'dashboard' };
    } else {
        navStore.context = { type: 'none' };
    }
}

/**
 * Transitions to the no-context state, shown when no server is connected.
 */
export function navigateToNone(): void {
    navStore.context = { type: 'none' };
}

/**
 * Toggles the expansion state of a server node in the sidebar.
 *
 * @param profileId - The profile whose tree node to toggle.
 */
export function toggleServerExpanded(profileId: string): void {
    const next = new Set(navStore.expandedServers);
    if (next.has(profileId)) {
        next.delete(profileId);
    } else {
        next.add(profileId);
    }
    navStore.expandedServers = next;
}

/**
 * Expands a server node without toggling (idempotent).
 *
 * Used when a server connects — it should always be expanded after connecting.
 *
 * @param profileId - The profile to expand.
 */
export function expandServer(profileId: string): void {
    if (navStore.expandedServers.has(profileId)) return;
    const next = new Set(navStore.expandedServers);
    next.add(profileId);
    navStore.expandedServers = next;
}

/**
 * Collapses a server node and clears its navigation context if it is currently
 * the active server.
 *
 * @param profileId - The profile to collapse and remove from active context.
 */
export function collapseServer(profileId: string): void {
    const next = new Set(navStore.expandedServers);
    next.delete(profileId);
    navStore.expandedServers = next;
    // If this server owns the current context, reset to none.
    const ctx = navStore.context;
    if (ctx.type !== 'none' && (ctx as { profileId?: string }).profileId === profileId) {
        navStore.context = { type: 'none' };
    }
}

/**
 * Toggles the primary sidebar between expanded and icon-rail (collapsed) mode.
 */
export function toggleSidebar(): void {
    navStore.sidebarCollapsed = !navStore.sidebarCollapsed;
}
