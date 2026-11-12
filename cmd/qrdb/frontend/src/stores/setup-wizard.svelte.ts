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
 * Server setup wizard state store.
 *
 * Implements a dynamic step-based state machine for first-run server
 * configuration. The step sequence adapts based on the selected server mode
 * (standalone vs clustered), skipping the cluster configuration step when
 * it is not applicable.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** All possible step identifiers in the server setup wizard. */
export type SetupWizardStep =
  | "welcome"
  | "serverMode"
  | "clusterConfig"
  | "globalBarrier"
  | "summary"
  | "applying"
  | "complete";

/** Whether the server runs as a single node or a distributed Raft cluster. */
export type ServerMode = "standalone" | "clustered";

/** Peer discovery method for distributed clusters. */
export type DiscoveryMethod = "multicast" | "dns" | "static";

/** Whether the cluster runs on localhost (dev) or across multiple hosts (prod). */
export type ClusterType = "local" | "distributed";

/** Encryption barrier mode for the global server barrier. */
export type BarrierMode = "none" | "passphrase" | "shamir";

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

/** Cluster configuration for clustered server mode. */
export interface ClusterConfig {
  /** Whether the cluster is a local dev cluster or a distributed production cluster. */
  clusterType: ClusterType;
  /** Peer discovery method (distributed clusters only). */
  discoveryMethod: DiscoveryMethod;
  /** Shared secret for multicast peer discovery. */
  multicastSecret: string;
  /** SRV DNS domain for DNS-based peer discovery. */
  dnsDomain: string;
  /** Static peer list in replicaID=host:port format (static discovery). */
  staticPeers: string[];
  /** Number of nodes in a local dev cluster (3, 5, or 7). */
  localNodeCount: number;
  /** Unique replica ID for this node within the cluster. */
  replicaId: number;
  /** QUIC transport address other nodes use to reach this replica. */
  raftAddress: string;
}

/** Global encryption barrier configuration. */
export interface BarrierConfig {
  /** Barrier mode: none, passphrase, or shamir. */
  mode: BarrierMode;
  /** Passphrase for passphrase-based barrier. */
  passphrase: string;
  /** Minimum number of Shamir shares required to unseal (M). */
  shamirThreshold: number;
  /** Total number of Shamir shares generated (N). */
  shamirShares: number;
}

/** Shape of the reactive server setup wizard state. */
export interface SetupWizardState {
  /** Current active step in the wizard flow. */
  currentStep: SetupWizardStep;
  /** Server deployment mode: standalone or clustered. */
  serverMode: ServerMode;
  /** Cluster configuration (clustered mode only). */
  clusterConfig: ClusterConfig;
  /** Global encryption barrier configuration. */
  barrierConfig: BarrierConfig;
}

// ---------------------------------------------------------------------------
// Step sequences
// ---------------------------------------------------------------------------

/** Step sequence for standalone (single-node) server mode. */
const standaloneSteps: readonly SetupWizardStep[] = [
  "welcome",
  "serverMode",
  "globalBarrier",
  "summary",
  "applying",
  "complete",
] as const;

/** Step sequence for clustered (multi-node Raft) server mode. */
const clusteredSteps: readonly SetupWizardStep[] = [
  "welcome",
  "serverMode",
  "clusterConfig",
  "globalBarrier",
  "summary",
  "applying",
  "complete",
] as const;

/**
 * Returns the active step sequence based on the current server mode.
 */
export function activeSteps(): readonly SetupWizardStep[] {
  return setupWizardStore.serverMode === "clustered"
    ? clusteredSteps
    : standaloneSteps;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Default cluster configuration values. */
const defaultClusterConfig: ClusterConfig = {
  clusterType: "local",
  discoveryMethod: "multicast",
  multicastSecret: "",
  dnsDomain: "",
  staticPeers: [],
  localNodeCount: 3,
  replicaId: 1,
  raftAddress: "localhost:63001",
};

/** Default barrier configuration values. */
const defaultBarrierConfig: BarrierConfig = {
  mode: "passphrase",
  passphrase: "",
  shamirThreshold: 3,
  shamirShares: 5,
};

/** Reactive server setup wizard state. */
export const setupWizardStore: SetupWizardState = $state({
  currentStep: "welcome",
  serverMode: "standalone",
  clusterConfig: { ...defaultClusterConfig },
  barrierConfig: { ...defaultBarrierConfig },
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Advances the wizard to the next step based on the current server mode's
 * step sequence. No-op when on the final step.
 */
export function nextStep(): void {
  const steps = activeSteps();
  const currentIndex = steps.indexOf(setupWizardStore.currentStep);
  if (currentIndex === -1 || currentIndex >= steps.length - 1) {
    return;
  }
  const next = steps[currentIndex + 1];
  if (next !== undefined) {
    setupWizardStore.currentStep = next;
  }
}

/**
 * Moves the wizard back to the previous step based on the current server
 * mode's step sequence. No-op when on the first step.
 */
export function prevStep(): void {
  const steps = activeSteps();
  const currentIndex = steps.indexOf(setupWizardStore.currentStep);
  if (currentIndex <= 0) {
    return;
  }
  const prev = steps[currentIndex - 1];
  if (prev !== undefined) {
    setupWizardStore.currentStep = prev;
  }
}

/**
 * Resets the setup wizard to its initial state, clearing all user-provided
 * configuration values.
 */
export function resetSetupWizard(): void {
  setupWizardStore.currentStep = "welcome";
  setupWizardStore.serverMode = "standalone";
  setupWizardStore.clusterConfig = { ...defaultClusterConfig };
  setupWizardStore.barrierConfig = { ...defaultBarrierConfig };
}
