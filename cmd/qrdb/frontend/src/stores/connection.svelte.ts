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
 * Connection state store.
 *
 * Tracks the current transport connection lifecycle so that UI components
 * can react to connect/disconnect transitions and display transport details.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Supported transport identifiers. */
export type Transport = 'wails' | 'rest' | 'grpc' | 'unix';

/** Shape of the reactive connection state. */
export interface ConnectionState {
    /** Whether the client is currently connected to a QRDB server. */
    connected: boolean;
    /** Server address the client is connected to. */
    address: string;
    /** Active transport protocol identifier. */
    transport: string;
    /** Last connection error message, or null when healthy. */
    error: string | null;
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive connection state. */
export const connectionStore: ConnectionState = $state({
    connected: false,
    address: '',
    transport: '',
    error: null,
});

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Transitions the store to a connected state.
 *
 * @param address   - Server address that was connected to.
 * @param transport - Transport protocol that established the connection.
 */
export function setConnected(address: string, transport: string): void {
    connectionStore.connected = true;
    connectionStore.address = address;
    connectionStore.transport = transport;
    connectionStore.error = null;
}

/**
 * Transitions the store to a disconnected state.
 *
 * @param error - Optional error message describing why the connection was lost.
 */
export function setDisconnected(error?: string): void {
    connectionStore.connected = false;
    connectionStore.address = '';
    connectionStore.transport = '';
    connectionStore.error = error ?? null;
}
