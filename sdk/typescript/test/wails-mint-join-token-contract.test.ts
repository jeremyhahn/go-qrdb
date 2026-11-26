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
 * Wire contract for WailsClient.admin.mintJoinToken and
 * admin.joinTokenMintable. The desktop binding forwards a mint to the
 * active connection only when that connection is an authenticated external
 * (remote) one; the Go AdminBinding refuses the embedded server, whose
 * transport carries no authorization of its own (docs/embedding.md,
 * "Trust model"). joinTokenMintable reports which case applies so the GUI
 * offers the form exactly when the mint can succeed.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installAdminBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { AdminBinding: binding } } });
}

describe('WailsClient admin.mintJoinToken', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('forwards node id and ttl to AdminBinding.MintJoinToken and decodes the reply', async () => {
        const mintJoinToken = vi.fn<BindingFn>(async () => ({
            token: 'tok-1',
            expires_at: '2030-01-01T00:00:00Z',
        }));
        installAdminBinding({ MintJoinToken: mintJoinToken });

        const client = new WailsClient();
        const resp = await client.admin.mintJoinToken('node-1', 60);
        expect(mintJoinToken).toHaveBeenCalledWith('node-1', 60);
        expect(resp).toEqual({ token: 'tok-1', expiresAt: '2030-01-01T00:00:00Z' });
    });

    it('sends ttl 0 (server default) when no ttlSeconds is given', async () => {
        const mintJoinToken = vi.fn<BindingFn>(async () => ({ token: 't', expires_at: '' }));
        installAdminBinding({ MintJoinToken: mintJoinToken });
        await new WailsClient().admin.mintJoinToken('node-2');
        expect(mintJoinToken).toHaveBeenCalledWith('node-2', 0);
    });

    it('surfaces the binding refusal on an embedded connection', async () => {
        installAdminBinding({
            MintJoinToken: async () => {
                throw new Error('join tokens are minted only over an authenticated remote connection');
            },
        });
        await expect(new WailsClient().admin.mintJoinToken('node-3')).rejects.toThrow(
            /authenticated remote connection/,
        );
    });

    it('joinTokenMintable reports the binding capability', async () => {
        installAdminBinding({ JoinTokenMintable: async () => true });
        await expect(new WailsClient().admin.joinTokenMintable()).resolves.toBe(true);
        installAdminBinding({ JoinTokenMintable: async () => false });
        await expect(new WailsClient().admin.joinTokenMintable()).resolves.toBe(false);
    });
});
