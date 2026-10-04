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
 * Wire contract for WailsClient.seal.seal: the call reaches
 * SealBinding.Seal(soPin) with the Security Officer PIN, and with the empty
 * string when the caller supplies none.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installSealBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { SealBinding: binding } } });
}

describe('WailsClient seal', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('passes the SO PIN to the binding', async () => {
        const seal = vi.fn<BindingFn>(async () => undefined);
        installSealBinding({ Seal: seal });

        await new WailsClient().seal.seal('SO123');
        expect(seal).toHaveBeenCalledWith('SO123');
    });

    it('passes the empty string without an SO PIN', async () => {
        const seal = vi.fn<BindingFn>(async () => undefined);
        installSealBinding({ Seal: seal });

        await new WailsClient().seal.seal();
        expect(seal).toHaveBeenCalledWith('');
    });

    it('propagates a refusal from the binding', async () => {
        installSealBinding({
            Seal: vi.fn<BindingFn>(async () => {
                throw new Error('pin: invalid PIN');
            }),
        });

        await expect(new WailsClient().seal.seal('wrong')).rejects.toThrow('pin: invalid PIN');
    });
});
