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
 * Wire contract for WailsClient.database.updateCompaction: the call reaches
 * DatabaseBinding.UpdateCompaction(name, req) with the Go
 * UpdateDatabaseCompactionRequest shape, {compaction: {mode, retainRevisions,
 * retainPeriod}}, and an absent policy is an empty request (off).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

/** Installs window.go.main.DatabaseBinding.UpdateCompaction as a spy. */
function installDatabaseBinding(result: unknown): ReturnType<typeof vi.fn<BindingFn>> {
    const updateCompaction = vi.fn<BindingFn>(async () => result);
    vi.stubGlobal('window', { go: { main: { DatabaseBinding: { UpdateCompaction: updateCompaction } } } });
    return updateCompaction;
}

describe('WailsClient database.updateCompaction', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('passes the Go UpdateDatabaseCompactionRequest shape to the binding', async () => {
        const spy = installDatabaseBinding({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            status: 'open',
            compaction: { mode: 'revision', retainRevisions: 500 },
        });

        const info = await new WailsClient().database.updateCompaction('shop', { mode: 'revision', retainRevisions: 500 });

        expect(spy).toHaveBeenCalledTimes(1);
        expect(spy).toHaveBeenCalledWith('shop', { compaction: { mode: 'revision', retainRevisions: 500 } });
        expect(info.compaction).toEqual({ mode: 'revision', retainRevisions: 500 });
    });

    it('omitting the policy sends an empty request, turning it off', async () => {
        const spy = installDatabaseBinding({ name: 'shop', engine: 'kv', backend: 'pebble', status: 'open' });

        const info = await new WailsClient().database.updateCompaction('shop');

        expect(spy).toHaveBeenCalledWith('shop', {});
        expect(info.compaction).toBeUndefined();
    });

    it('propagates a refusal from the binding', async () => {
        const updateCompaction = vi.fn<BindingFn>(async () => {
            throw new Error('compaction: invalid policy');
        });
        vi.stubGlobal('window', { go: { main: { DatabaseBinding: { UpdateCompaction: updateCompaction } } } });

        await expect(new WailsClient().database.updateCompaction('shop', { mode: 'period' }))
            .rejects.toThrow('compaction: invalid policy');
    });
});
