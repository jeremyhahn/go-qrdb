// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Wire contract for WailsClient.database.updateRaft, updateBarrier and
 * updateConnection: each reaches its DatabaseBinding method with the Go
 * request envelope the binding decodes, {raft: {...}}, {barrier: {...}} and
 * {connection: {...}} (transport.UpdateDatabase{Raft,Barrier,Connection}Request),
 * never the bare configuration.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

const info = { name: 'shop', backend: 'pebble', status: 'open' };

/** Installs window.go.main.DatabaseBinding.<method> as a spy. */
function installBinding(method: string): ReturnType<typeof vi.fn<BindingFn>> {
    const spy = vi.fn<BindingFn>(async () => info);
    vi.stubGlobal('window', { go: { main: { DatabaseBinding: { [method]: spy } } } });
    return spy;
}

describe('WailsClient database settings updates', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('wraps the raft config in the UpdateDatabaseRaftRequest envelope', async () => {
        const spy = installBinding('UpdateRaft');
        const raft = {
            clusterMode: 'distributed',
            replicaId: 2,
            discovery: 'static',
            static: { peers: ['db4.example.com:63001'], sharedSecret: 'static-secret' },
        } as const;

        await new WailsClient().database.updateRaft('shop', raft);

        expect(spy).toHaveBeenCalledTimes(1);
        expect(spy).toHaveBeenCalledWith('shop', { raft });
    });

    it('wraps the barrier config in the UpdateDatabaseBarrierRequest envelope', async () => {
        const spy = installBinding('UpdateBarrier');
        const barrier = { enabled: true, mode: 'shamir', sharesRequired: 2, sharesTotal: 3 };

        await new WailsClient().database.updateBarrier('shop', barrier);

        expect(spy).toHaveBeenCalledTimes(1);
        expect(spy).toHaveBeenCalledWith('shop', { barrier });
    });

    it('wraps the connection config in the UpdateDatabaseConnectionRequest envelope', async () => {
        const spy = installBinding('UpdateConnection');
        const connection = { transport: 'grpc', address: 'db.example.com:63000' };

        await new WailsClient().database.updateConnection('shop', connection);

        expect(spy).toHaveBeenCalledTimes(1);
        expect(spy).toHaveBeenCalledWith('shop', { connection });
    });

    it('propagates a refusal from the binding', async () => {
        const spy = vi.fn<BindingFn>(async () => {
            throw new Error('raft: invalid config');
        });
        vi.stubGlobal('window', { go: { main: { DatabaseBinding: { UpdateRaft: spy } } } });

        await expect(new WailsClient().database.updateRaft('shop', { clusterMode: 'single' }))
            .rejects.toThrow('raft: invalid config');
    });
});
