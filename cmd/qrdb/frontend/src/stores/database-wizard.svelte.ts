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
 * Database creation wizard state store.
 *
 * Implements a dynamic step-based state machine for creating new databases.
 * The step sequence adapts based on the selected database mode (embedded vs
 * remote), skipping irrelevant configuration steps.
 */

import type { SealMode, EmbeddedSealConfig } from '@qrdb/sdk';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** All possible step identifiers in the database creation wizard. */
export type DatabaseWizardStep =
    | 'name'
    | 'mode'
    | 'storage'
    | 'raft'
    | 'connection'
    | 'sql'
    | 'barrier'
    | 'summary'
    | 'creating'
    | 'complete';

/** Whether the database is embedded locally or connects to a remote server. */
export type DatabaseMode = 'embedded' | 'remote';

/** The type of data engine powering the database. */
export type EngineType = 'kv' | 'timeseries' | 'vector';

/** Storage backend for embedded databases. */
export type StorageBackend = 'memory' | 'file' | 'pebble';

/** Raft cluster mode for embedded databases. */
export type RaftClusterMode = 'none' | 'local' | 'distributed';

/** Raft cluster configuration for embedded databases. */
/** Raft group (shard) configuration for databases on clustered servers. */
export interface RaftGroupsConfig {
    /** Number of raft groups (shards) for this database. */
    shardCount: number;
    /** Replication factor per shard. */
    replicationFactor: number;
}

export interface DatabaseRaftConfig {
    clusterMode: RaftClusterMode;
    shardId: number;
    replicaId: number;
    raftAddress: string;
    peers: string[];
    localNodeCount: number;
    /** Raft group configuration (only when cluster mode is not 'none'). */
    raftGroups: RaftGroupsConfig | null;
}

/** Remote connection configuration. */
export interface DatabaseConnectionConfig {
    transport: string;
    address: string;
    caFile: string;
    certFile: string;
    keyFile: string;
}

/** Shape of the reactive database wizard state. */
export interface DatabaseWizardState {
    /** Current active step in the wizard flow. */
    currentStep: DatabaseWizardStep;
    /** Database name (lowercase alphanumeric + hyphens). */
    name: string;
    /** Engine type: kv, timeseries, or vector. */
    engine: EngineType;
    /** Embedded or remote mode (inherited from the active connection). */
    mode: DatabaseMode;
    /** Server topology mode (set by MainApp before opening the wizard). */
    serverMode: 'standalone' | 'raft';
    /** Storage backend (embedded only). */
    backend: StorageBackend;
    /** Custom data directory (embedded file/pebble only, empty = default). */
    dataDir: string;
    /** Raft cluster configuration (embedded only, null when not applicable). */
    raftConfig: DatabaseRaftConfig | null;
    /**
     * Replication factor for a dedicated-raft database (voting replicas per
     * group). Bounded by the tenant's replica quota and the cluster size.
     * Defaults to 1 (single replica).
     */
    replicas: number;
    /**
     * Whether to enable per-database WAL encryption under the tenant's own key.
     * Only honoured for the raft backend; requires the `allow_dedicated_wal`
     * entitlement and the `max_dedicated_wal_groups` quota. Defaults to false.
     */
    dedicatedWal: boolean;
    /** Remote connection configuration (remote only, null when not applicable). */
    connectionConfig: DatabaseConnectionConfig | null;
    /** Whether MySQL wire protocol is enabled. */
    sqlMySQL: boolean;
    /** Whether PostgreSQL wire protocol is enabled. */
    sqlPostgreSQL: boolean;
    /** Whether etcd API compatibility is enabled (Raft databases only). */
    etcdEnabled: boolean;
    /** Whether the per-database encryption barrier is enabled. */
    barrierEnabled: boolean;
    /** Seal mode for the per-database barrier. */
    sealMode: SealMode;
    /** Seal configuration (shamir shares, etc.). */
    sealConfig: EmbeddedSealConfig;
}

// ---------------------------------------------------------------------------
// Step sequences
// ---------------------------------------------------------------------------

/** Step sequence for standalone servers — no Raft configuration needed. */
const standaloneSteps: readonly DatabaseWizardStep[] = [
    'name',
    'storage',
    'sql',
    'barrier',
    'summary',
    'creating',
    'complete',
] as const;

/** Step sequence for Raft cluster servers — includes Raft group configuration. */
const raftSteps: readonly DatabaseWizardStep[] = [
    'name',
    'storage',
    'raft',
    'sql',
    'barrier',
    'summary',
    'creating',
    'complete',
] as const;

/**
 * Returns the active step sequence based on the server's topology. Standalone
 * servers skip the Raft step entirely. The server mode is stored on the
 * wizard state itself (set by MainApp before opening the wizard).
 */
export function activeSteps(): readonly DatabaseWizardStep[] {
    return dbWizardStore.serverMode === 'raft' ? raftSteps : standaloneSteps;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Default seal configuration values. */
const defaultSealConfig: EmbeddedSealConfig = {
    mode: 'passphrase',
    sharesRequired: 3,
    sharesTotal: 5,
};

/** Default raft configuration. */
const defaultRaftConfig: DatabaseRaftConfig = {
    clusterMode: 'none',
    shardId: 0,
    replicaId: 1,
    raftAddress: 'localhost:63001',
    peers: [],
    localNodeCount: 3,
    raftGroups: null,
};

/** Reactive database wizard state. */
export const dbWizardStore: DatabaseWizardState = $state({
    currentStep: 'name',
    name: '',
    engine: 'kv',
    mode: 'embedded',
    serverMode: 'standalone',
    backend: 'pebble',
    dataDir: '',
    raftConfig: { ...defaultRaftConfig },
    replicas: 1,
    dedicatedWal: false,
    connectionConfig: null,
    sqlMySQL: false,
    sqlPostgreSQL: false,
    etcdEnabled: false,
    barrierEnabled: true,
    sealMode: 'passphrase',
    sealConfig: { ...defaultSealConfig },
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Advances the wizard to the next step based on the current mode's step
 * sequence. No-op when on the final step.
 */
export function nextStep(): void {
    const steps = activeSteps();
    const currentIndex = steps.indexOf(dbWizardStore.currentStep);
    if (currentIndex === -1 || currentIndex >= steps.length - 1) {
        return;
    }
    const next = steps[currentIndex + 1];
    if (next !== undefined) {
        dbWizardStore.currentStep = next;
    }
}

/**
 * Moves the wizard back to the previous step based on the current mode's
 * step sequence. No-op when on the first step.
 */
export function prevStep(): void {
    const steps = activeSteps();
    const currentIndex = steps.indexOf(dbWizardStore.currentStep);
    if (currentIndex <= 0) {
        return;
    }
    const prev = steps[currentIndex - 1];
    if (prev !== undefined) {
        dbWizardStore.currentStep = prev;
    }
}

/**
 * Resets the database wizard to its initial state, clearing all user-provided
 * configuration values.
 */
export function resetDatabaseWizard(): void {
    dbWizardStore.currentStep = 'name';
    dbWizardStore.name = '';
    dbWizardStore.engine = 'kv';
    dbWizardStore.mode = 'embedded';
    dbWizardStore.serverMode = 'standalone';
    dbWizardStore.backend = 'pebble';
    dbWizardStore.dataDir = '';
    dbWizardStore.raftConfig = { ...defaultRaftConfig };
    dbWizardStore.replicas = 1;
    dbWizardStore.dedicatedWal = false;
    dbWizardStore.connectionConfig = null;
    dbWizardStore.sqlMySQL = false;
    dbWizardStore.sqlPostgreSQL = false;
    dbWizardStore.etcdEnabled = false;
    dbWizardStore.barrierEnabled = true;
    dbWizardStore.sealMode = 'passphrase';
    dbWizardStore.sealConfig = { ...defaultSealConfig };
}
