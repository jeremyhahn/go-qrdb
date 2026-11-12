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
 * Connection manager state store.
 *
 * Manages the lifecycle of saved connection profiles and their runtime
 * connection states. Provides mutations for loading, connecting,
 * disconnecting, deleting, and activating profiles.
 *
 * Profile persistence is pluggable: desktop mode delegates to the Wails
 * ConnectionManagerBinding, web mode uses localStorage so that the browser
 * tab can manage multiple qrdbd servers without any desktop dependency.
 */

import type { Client } from "@qrdb/sdk";
import { isDesktop } from "@qrdb/sdk";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Raft cluster discovery configuration for an embedded server. */
export interface EmbeddedRaftConfig {
    /** Discovery mechanism: multicast, DNS SRV, or static peer list. */
    discovery?: string;
    /** Shared secret used for multicast peer discovery. */
    multicastSecret?: string;
    /** DNS domain used for SRV record-based discovery. */
    dnsDomain?: string;
    /** Explicit peer addresses used for static discovery. */
    peers?: string[];
}

/** Encryption barrier configuration for an embedded server. */
export interface EmbeddedSealConfig {
    /** Barrier mode: none, passphrase, or shamir. */
    mode?: string;
    /** Passphrase used when mode === 'passphrase'. */
    passphrase?: string;
    /** Minimum shares required to unseal when mode === 'shamir'. */
    sharesRequired?: number;
    /** Total shares generated when mode === 'shamir'. */
    sharesTotal?: number;
}

/** Server-level configuration for an embedded QRDB instance. */
export interface EmbeddedConfig {
    /** Root data directory for all server data. */
    dataDir?: string;
    /** Whether multi-node Raft consensus is enabled on this server. */
    raftEnabled?: boolean;
    /** Raft cluster discovery configuration (present when raftEnabled). */
    raftConfig?: EmbeddedRaftConfig;
    /** Encryption barrier bootstrap configuration. */
    sealConfig?: EmbeddedSealConfig;
}

/** Configuration for an externally-hosted QRDB server. */
export interface ExternalConnectionConfig {
    /** Transport protocol identifier. */
    transport: string;
    /** Server address or socket path. */
    address: string;
    /** Optional PEM file path for the CA certificate. */
    caFile?: string;
    /** Optional PEM file path for the client certificate. */
    certFile?: string;
    /** Optional PEM file path for the client private key. */
    keyFile?: string;
}

/** A saved connection profile describing how to reach a QRDB instance. */
export interface ConnectionProfile {
    /** Unique identifier for this profile. */
    id: string;
    /** Human-readable name shown in the UI. */
    name: string;
    /** Whether this profile targets an embedded or external server. */
    type: 'embedded' | 'external';
    /** Embedded server configuration (present when type === 'embedded'). */
    embedded?: EmbeddedConfig;
    /** External server configuration (present when type === 'external'). */
    external?: ExternalConnectionConfig;
    /** Server topology mode recorded at connection time. */
    serverMode?: 'standalone' | 'raft';
    /** Number of nodes in the cluster (only meaningful when serverMode === 'raft'). */
    clusterNodeCount?: number;
    /** ISO 8601 creation timestamp. */
    createdAt: string;
    /** ISO 8601 timestamp of the most recent successful connection, if any. */
    lastUsed?: string;
    /**
     * When true this profile was auto-created by the application and must not
     * be deleted by the user. The delete UI must check this flag before
     * allowing removal.
     */
    builtin?: boolean;
}

/** The runtime connection state for a single profile. */
export interface ConnectionState {
    /** The profile this state belongs to. */
    profileId: string;
    /** Current lifecycle status. */
    status: 'disconnected' | 'connecting' | 'needs-init' | 'sealed' | 'connected' | 'error';
    /** Human-readable error message when status === 'error'. */
    error?: string;
    /** Whether this profile targets an embedded server. */
    isEmbedded: boolean;
}

/** Shape of the reactive connection manager state. */
export interface ConnManagerState {
    /** All saved connection profiles. */
    profiles: ConnectionProfile[];
    /** Runtime connection state keyed by profile ID. */
    states: Map<string, ConnectionState>;
    /** ID of the currently active profile, or empty string if none. */
    activeProfileId: string;
    /** Whether an embedded server can be created on this platform. */
    embeddedAvailable: boolean;
    /** Whether the initial profile list is being loaded. */
    loading: boolean;
    /** Server mode of the currently active profile. */
    activeServerMode: 'standalone' | 'raft' | '';
    /** Number of cluster nodes for the currently active profile. */
    activeClusterNodeCount: number;
}

// ---------------------------------------------------------------------------
// Profile persistence adapters
// ---------------------------------------------------------------------------

/**
 * Pluggable persistence contract for connection profiles.
 *
 * Two implementations are provided: WailsProfilePersistence for the desktop
 * app (delegates to Wails IPC bindings) and LocalStoragePersistence for web
 * mode (persists to localStorage so one browser tab can manage multiple
 * qrdbd servers).
 */
interface ProfilePersistence {
    /** Returns all saved profiles. */
    load(client: Client): Promise<ConnectionProfile[]>;
    /** Persists the current profile list. Replaces any previous snapshot. */
    save(client: Client, profiles: ConnectionProfile[]): Promise<void>;
}

/** localStorage key used to store web-mode profiles. */
const WEB_PROFILES_KEY = 'qrdb:profiles:v1';

/** localStorage key used to store the active profile ID in web mode. */
const WEB_ACTIVE_KEY = 'qrdb:active-profile:v1';

/**
 * LocalStoragePersistence persists connection profiles to localStorage.
 *
 * Used in web (browser) mode where the Wails ConnectionManagerBinding is not
 * available. Profiles survive page reloads but are scoped to the browser
 * origin, which is appropriate for a web UI served by qrdbd.
 */
const localStoragePersistence: ProfilePersistence = {
    async load(_client: Client): Promise<ConnectionProfile[]> {
        const raw = localStorage.getItem(WEB_PROFILES_KEY);
        if (raw === null) return [];
        try {
            return JSON.parse(raw) as ConnectionProfile[];
        } catch {
            return [];
        }
    },

    async save(_client: Client, profiles: ConnectionProfile[]): Promise<void> {
        localStorage.setItem(WEB_PROFILES_KEY, JSON.stringify(profiles));
    },
};

/**
 * WailsProfilePersistence delegates to the Wails ConnectionManagerBinding.
 *
 * Used in desktop mode where the native IPC layer provides profile storage in
 * the user's config directory.
 */
const wailsProfilePersistence: ProfilePersistence = {
    async load(client: Client): Promise<ConnectionProfile[]> {
        const profiles = await client.connectionManager.listProfiles();
        return profiles as unknown as ConnectionProfile[];
    },

    async save(_client: Client, _profiles: ConnectionProfile[]): Promise<void> {
        // Desktop profiles are saved individually through client.connectionManager
        // when a new connection is established. This adapter is only responsible
        // for loading the initial list; saves happen via saveProfile in the SDK.
    },
};

/** The active persistence adapter, chosen once at module load time. */
const persistence: ProfilePersistence = isDesktop()
    ? wailsProfilePersistence
    : localStoragePersistence;

// ---------------------------------------------------------------------------
// Web-mode helpers
// ---------------------------------------------------------------------------

/**
 * Returns the built-in "This Server" profile that web mode auto-registers
 * for the qrdbd instance serving the current browser origin.
 */
function builtinWebProfile(): ConnectionProfile {
    return {
        id: 'self',
        name: 'This Server',
        type: 'external',
        external: {
            transport: 'rest',
            address: window.location.origin,
        },
        builtin: true,
        createdAt: new Date().toISOString(),
    };
}

/**
 * Returns the active profile ID persisted in localStorage, or '' if none.
 */
function webGetActiveProfileId(): string {
    return localStorage.getItem(WEB_ACTIVE_KEY) ?? '';
}

/**
 * Persists the active profile ID to localStorage.
 */
function webSetActiveProfileId(id: string): void {
    localStorage.setItem(WEB_ACTIVE_KEY, id);
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive connection manager state. */
export const connManagerStore: ConnManagerState = $state({
    profiles: [] as ConnectionProfile[],
    states: new Map<string, ConnectionState>(),
    activeProfileId: '',
    embeddedAvailable: false,
    loading: true,
    activeServerMode: '',
    activeClusterNodeCount: 0,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Loads all saved profiles and resolves their current connection states.
 *
 * In desktop mode delegates to the Wails ConnectionManagerBinding. In web
 * mode loads from localStorage and auto-registers the "This Server" built-in
 * profile for the hosting qrdbd origin if it is not already present. After
 * loading, the store's loading flag is cleared.
 *
 * @param client - The SDK client singleton.
 */
export async function loadProfiles(client: Client): Promise<void> {
    connManagerStore.loading = true;
    try {
        if (isDesktop()) {
            await loadDesktopProfiles(client);
        } else {
            await loadWebProfiles(client);
        }
    } finally {
        connManagerStore.loading = false;
    }
}

async function loadDesktopProfiles(client: Client): Promise<void> {
    const [profiles, embeddedAvailable, activeId] = await Promise.all([
        persistence.load(client),
        client.connectionManager.isEmbeddedAvailable(),
        client.connectionManager.getActiveProfileId(),
    ]);

    connManagerStore.profiles = profiles;
    connManagerStore.embeddedAvailable = embeddedAvailable;
    connManagerStore.activeProfileId = activeId;

    // Fetch current connection state for each profile.
    const stateEntries = await Promise.all(
        profiles.map(async (p) => {
            const state = await client.connectionManager.getState(p.id);
            return [p.id, state] as const;
        }),
    );

    const stateMap = new Map<string, ConnectionState>();
    for (const [id, state] of stateEntries) {
        if (state !== null) {
            stateMap.set(id, state as unknown as ConnectionState);
        }
    }
    connManagerStore.states = stateMap;

    // Populate active server mode from the active profile if present.
    if (activeId !== '') {
        const activeProfile = profiles.find((p) => p.id === activeId);
        if (activeProfile !== undefined) {
            connManagerStore.activeServerMode = activeProfile.serverMode ?? '';
            connManagerStore.activeClusterNodeCount = activeProfile.clusterNodeCount ?? 0;
        }
    }
}

async function loadWebProfiles(client: Client): Promise<void> {
    let profiles = await persistence.load(client);

    // Auto-register the built-in "This Server" profile for the hosting origin
    // if it is not already present in localStorage.
    const hasSelf = profiles.some((p) => p.id === 'self');
    if (!hasSelf) {
        const self = builtinWebProfile();
        profiles = [self, ...profiles];
        await persistence.save(client, profiles);
    }

    connManagerStore.profiles = profiles;
    // Embedded server creation is not available in web (browser) mode.
    connManagerStore.embeddedAvailable = false;

    // Determine the active profile from localStorage. Default to 'self' when
    // no active profile has been persisted yet.
    let activeId = webGetActiveProfileId();
    if (activeId === '' || !profiles.some((p) => p.id === activeId)) {
        activeId = 'self';
        webSetActiveProfileId(activeId);
    }
    connManagerStore.activeProfileId = activeId;

    // Resolve connection state for each profile by probing the server.
    // The active profile (self) connects via the REST client that was already
    // initialised before loadProfiles was called. Non-active external profiles
    // are shown as disconnected until the user explicitly connects them.
    const stateMap = new Map<string, ConnectionState>();
    for (const profile of profiles) {
        if (profile.id === activeId) {
            // Probe the active profile by checking the seal status.
            const state = await resolveWebConnectionState(client, profile);
            stateMap.set(profile.id, state);
        } else {
            stateMap.set(profile.id, {
                profileId: profile.id,
                status: 'disconnected',
                isEmbedded: false,
            });
        }
    }
    connManagerStore.states = stateMap;

    // Populate active server mode.
    const activeProfile = profiles.find((p) => p.id === activeId);
    if (activeProfile !== undefined) {
        connManagerStore.activeServerMode = activeProfile.serverMode ?? '';
        connManagerStore.activeClusterNodeCount = activeProfile.clusterNodeCount ?? 0;
    }

    // When the active server is reachable, probe its live topology so the UI
    // exposes Raft cluster surfaces for multi-node deployments (the built-in
    // web profile carries no recorded topology). Skipped while sealed /
    // needs-init: the topology endpoint is unavailable until the server is
    // fully booted.
    if (stateMap.get(activeId)?.status === 'connected') {
        await applyWebServerMode(client, activeId);
    }
}

/**
 * Probes the server's cluster topology to decide whether the web UI should
 * expose Raft cluster surfaces (the Cluster / Topology nav entries and the
 * database-wizard replication-factor selection).
 *
 * Desktop profiles record their topology at creation time (the setup wizard's
 * standalone/clustered choice). The built-in web "This Server" profile has no
 * such record, so without this probe a browser connected to a real multi-node
 * cluster would always present the standalone UI. We derive the mode from the
 * live topology: a server whose topology loads with at least one replica is
 * "raft", so a single-node Raft server still exposes the Cluster surfaces
 * (minting its first join token is how it grows). Only a server with no
 * readable topology (a minimal embeddable server, forbidden or unreachable)
 * resolves to "standalone". The database wizard keys its multi-node steps on
 * the node count, not on this mode (see isMultiNodeServer).
 *
 * @returns The detected mode and the distinct cluster node count (0 when the
 *   topology could not be read).
 */
async function detectWebServerMode(
    client: Client,
): Promise<{ serverMode: 'standalone' | 'raft'; nodeCount: number }> {
    try {
        const topology = await client.admin.getTopology();
        const replicaIds = new Set<string>();
        for (const shard of topology.shards ?? []) {
            for (const replicaId of Object.keys(shard.nodes ?? {})) {
                replicaIds.add(replicaId);
            }
        }
        const nodeCount = replicaIds.size;
        return { serverMode: nodeCount >= 1 ? 'raft' : 'standalone', nodeCount };
    } catch {
        // No topology endpoint (minimal embeddable server), forbidden, or
        // unreachable → treat as standalone; cluster surfaces stay hidden.
        return { serverMode: 'standalone', nodeCount: 0 };
    }
}

/**
 * Reports whether the active server is a multi-node Raft cluster, which is what
 * the database wizard's Raft steps (replica count, etcd) need. A single-node
 * Raft server is "raft" for the admin nav but gets the wizard's single-node
 * steps. A desktop profile recorded as clustered carries no node count (0) and
 * stays multi-node.
 */
export function isMultiNodeServer(): boolean {
    if (connManagerStore.activeServerMode !== 'raft') return false;
    const n = connManagerStore.activeClusterNodeCount;
    return n === 0 || n > 1;
}

/**
 * Applies detected topology to a web profile in the store and to the active
 * server-mode fields. Persists the updated profile so the mode survives
 * reloads without re-probing being strictly required.
 */
async function applyWebServerMode(client: Client, profileId: string): Promise<void> {
    const { serverMode, nodeCount } = await detectWebServerMode(client);
    connManagerStore.profiles = connManagerStore.profiles.map((p) =>
        p.id === profileId ? { ...p, serverMode, clusterNodeCount: nodeCount } : p,
    );
    if (connManagerStore.activeProfileId === profileId) {
        connManagerStore.activeServerMode = serverMode;
        connManagerStore.activeClusterNodeCount = nodeCount;
    }
    await persistence.save(client, connManagerStore.profiles).catch(() => {
        // Persistence is best-effort; the in-memory mode is what the UI reads.
    });
}

/**
 * Resolves the connection state for a web-mode profile by querying the REST
 * client for the server's seal status. The REST client is already connected
 * to the server origin at this point.
 */
async function resolveWebConnectionState(
    client: Client,
    profile: ConnectionProfile,
): Promise<ConnectionState> {
    try {
        const status = await client.seal.status();
        if (status.sealed) {
            return { profileId: profile.id, status: 'sealed', isEmbedded: false };
        }
        if (!status.initialized) {
            // The SDK returns initialized=false both when the barrier
            // genuinely needs initialization AND when the barrier service
            // isn't configured at all (404 → synthetic response). Probe a
            // known endpoint to distinguish: if database.list succeeds the
            // server is healthy and barrier-less, not uninitialized.
            try {
                await client.database.list();
                return { profileId: profile.id, status: 'connected', isEmbedded: false };
            } catch {
                return { profileId: profile.id, status: 'needs-init', isEmbedded: false };
            }
        }
        return { profileId: profile.id, status: 'connected', isEmbedded: false };
    } catch {
        return { profileId: profile.id, status: 'error', isEmbedded: false, error: 'Unreachable' };
    }
}

/**
 * Connects to the server described by the given profile.
 *
 * Updates the profile's connection state in the store on success.
 *
 * @param client    - The SDK client singleton.
 * @param profileId - ID of the profile to connect.
 * @returns The resulting connection state.
 */
export async function connectProfile(
    client: Client,
    profileId: string,
): Promise<ConnectionState> {
    // Mark as connecting immediately so the UI can reflect the transition.
    const connecting: ConnectionState = {
        profileId,
        status: 'connecting',
        isEmbedded: isEmbeddedProfile(profileId),
    };
    connManagerStore.states = new Map(connManagerStore.states).set(
        profileId,
        connecting,
    );

    if (isDesktop()) {
        try {
            const raw = await client.connectionManager.connect(profileId);
            const state = raw as unknown as ConnectionState;
            connManagerStore.states = new Map(connManagerStore.states).set(
                profileId,
                state,
            );
            return state;
        } catch (err: unknown) {
            // Clear the "connecting" placeholder so the sidebar spinner stops.
            // Callers catch this exception and present the error to the user.
            const errState: ConnectionState = {
                profileId,
                status: 'error',
                isEmbedded: isEmbeddedProfile(profileId),
                error: err instanceof Error ? err.message : String(err),
            };
            connManagerStore.states = new Map(connManagerStore.states).set(
                profileId,
                errState,
            );
            throw err;
        }
    }

    // Web mode: resolve state by probing the REST endpoint.
    const profile = connManagerStore.profiles.find((p) => p.id === profileId);
    if (profile === undefined) {
        const errState: ConnectionState = { profileId, status: 'error', isEmbedded: false, error: 'Profile not found' };
        connManagerStore.states = new Map(connManagerStore.states).set(profileId, errState);
        return errState;
    }
    const state = await resolveWebConnectionState(client, profile);
    connManagerStore.states = new Map(connManagerStore.states).set(profileId, state);

    // Probe live topology on a successful connect so cluster surfaces appear
    // immediately for multi-node servers (mirrors loadWebProfiles).
    if (state.status === 'connected') {
        await applyWebServerMode(client, profileId);
    }
    return state;
}

/**
 * Disconnects the profile with the given ID.
 *
 * Updates the store to reflect the disconnected status.
 *
 * @param client    - The SDK client singleton.
 * @param profileId - ID of the profile to disconnect.
 */
export async function disconnectProfile(
    client: Client,
    profileId: string,
): Promise<void> {
    if (isDesktop()) {
        await client.connectionManager.disconnect(profileId);
    }
    const disconnected: ConnectionState = {
        profileId,
        status: 'disconnected',
        isEmbedded: isEmbeddedProfile(profileId),
    };
    connManagerStore.states = new Map(connManagerStore.states).set(
        profileId,
        disconnected,
    );
}

/**
 * Deletes a saved profile and removes it from the store.
 *
 * Throws if the profile is marked builtin — the UI must prevent this, but
 * the store enforces it as a second layer of defence.
 *
 * @param client    - The SDK client singleton.
 * @param profileId - ID of the profile to delete.
 */
/**
 * Saves a profile (insert or replace by id) and adds it to the store.
 *
 * Desktop mode delegates to the Wails ConnectionManagerBinding; web mode
 * persists the updated list to localStorage, since the REST client has no
 * connection-manager backend.
 */
export async function saveProfile(
    client: Client,
    profile: ConnectionProfile,
): Promise<void> {
    const updated = [
        ...connManagerStore.profiles.filter((p) => p.id !== profile.id),
        profile,
    ];
    if (isDesktop()) {
        await client.connectionManager.saveProfile(profile as never);
    } else {
        await persistence.save(client, updated);
    }
    connManagerStore.profiles = updated;
}

export async function deleteProfile(
    client: Client,
    profileId: string,
): Promise<void> {
    const profile = connManagerStore.profiles.find((p) => p.id === profileId);
    if (profile?.builtin === true) {
        throw new Error('Built-in profiles cannot be deleted.');
    }

    if (isDesktop()) {
        await client.connectionManager.deleteProfile(profileId);
    } else {
        const updated = connManagerStore.profiles.filter((p) => p.id !== profileId);
        await persistence.save(client, updated);
    }

    connManagerStore.profiles = connManagerStore.profiles.filter(
        (p) => p.id !== profileId,
    );
    const next = new Map(connManagerStore.states);
    next.delete(profileId);
    connManagerStore.states = next;
    if (connManagerStore.activeProfileId === profileId) {
        connManagerStore.activeProfileId = '';
        connManagerStore.activeServerMode = '';
        connManagerStore.activeClusterNodeCount = 0;
        if (!isDesktop()) {
            webSetActiveProfileId('');
        }
    }
}

/**
 * Sets the active profile that the rest of the application operates against.
 *
 * Populates activeServerMode and activeClusterNodeCount from the profile.
 *
 * @param client    - The SDK client singleton.
 * @param profileId - ID of the profile to make active.
 */
export async function setActiveProfile(
    client: Client,
    profileId: string,
): Promise<void> {
    if (isDesktop()) {
        await client.connectionManager.setActive(profileId);
    } else {
        webSetActiveProfileId(profileId);
    }
    connManagerStore.activeProfileId = profileId;

    // Refresh the lastUsed timestamp on the profile in local state.
    connManagerStore.profiles = connManagerStore.profiles.map((p) =>
        p.id === profileId ? { ...p, lastUsed: new Date().toISOString() } : p,
    );

    // Propagate server topology metadata to the store.
    const profile = connManagerStore.profiles.find((p) => p.id === profileId);
    if (profile !== undefined) {
        connManagerStore.activeServerMode = profile.serverMode ?? '';
        connManagerStore.activeClusterNodeCount = profile.clusterNodeCount ?? 0;
    }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/**
 * Returns true when the identified profile is of type 'embedded'.
 *
 * Used to populate ConnectionState.isEmbedded for optimistic updates where
 * the full profile data is already in the store.
 */
function isEmbeddedProfile(profileId: string): boolean {
    const profile = connManagerStore.profiles.find((p) => p.id === profileId);
    return profile?.type === 'embedded';
}
