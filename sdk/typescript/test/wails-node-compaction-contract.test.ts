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
 * Wire contract for WailsClient.admin.nodeCompaction / setNodeCompaction: the
 * calls reach AdminBinding.NodeCompaction() and
 * AdminBinding.SetNodeCompaction(policy) with the Go
 * *DatabaseCompactionConfig -- null for no policy, both ways.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installAdminBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { AdminBinding: binding } } });
}

describe('WailsClient admin node-keyspace compaction', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('reads the policy, and null as undefined', async () => {
        const nodeCompaction = vi.fn<BindingFn>()
            .mockResolvedValueOnce({ mode: 'revision', retainRevisions: 10 })
            .mockResolvedValueOnce(null);
        installAdminBinding({ NodeCompaction: nodeCompaction });

        const client = new WailsClient();
        expect(await client.admin.nodeCompaction()).toEqual({ mode: 'revision', retainRevisions: 10 });
        expect(await client.admin.nodeCompaction()).toBeUndefined();
        expect(nodeCompaction).toHaveBeenCalledTimes(2);
    });

    it('passes the policy to the binding, and null to turn it off', async () => {
        const setNodeCompaction = vi.fn<BindingFn>(async (policy: unknown) => policy);
        installAdminBinding({ SetNodeCompaction: setNodeCompaction });

        const client = new WailsClient();
        const set = await client.admin.setNodeCompaction({ mode: 'period', retainPeriod: '24h' });
        expect(setNodeCompaction).toHaveBeenLastCalledWith({ mode: 'period', retainPeriod: '24h' });
        expect(set).toEqual({ mode: 'period', retainPeriod: '24h' });

        const off = await client.admin.setNodeCompaction();
        expect(setNodeCompaction).toHaveBeenLastCalledWith(null);
        expect(off).toBeUndefined();
    });

    it('propagates a refusal from the binding', async () => {
        installAdminBinding({
            SetNodeCompaction: vi.fn<BindingFn>(async () => {
                throw new Error('permission denied');
            }),
        });

        await expect(new WailsClient().admin.setNodeCompaction({ mode: 'revision', retainRevisions: 1 }))
            .rejects.toThrow('permission denied');
    });
});
