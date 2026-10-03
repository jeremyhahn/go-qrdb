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
 * Wire contract for WailsClient.admin.mintJoinToken: unlike every other
 * AdminClient verb, this one is deliberately UNIMPLEMENTED on the desktop
 * binding. The embedded transport the webview talks to carries no
 * authorization of its own (docs/embedded's trust-model note), and minting
 * a cluster join token is ScopeCross and platform-only -- exactly the kind
 * of admin verb that must never be reachable through a less-trusted
 * binding than the authenticated remote transports it IS implemented on
 * (REST, gRPC, QUIC, Unix, MCP).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';
import { NotImplementedError } from '../src/errors.js';

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

    it('refuses with NotImplementedError, never calling into any binding', async () => {
        const mintJoinToken = vi.fn<BindingFn>();
        installAdminBinding({ MintJoinToken: mintJoinToken });

        const client = new WailsClient();
        await expect(client.admin.mintJoinToken('node-1', 60)).rejects.toThrow(NotImplementedError);
        expect(mintJoinToken).not.toHaveBeenCalled();
    });

    it('refuses the same way with no ttlSeconds argument', async () => {
        installAdminBinding({});
        const client = new WailsClient();
        await expect(client.admin.mintJoinToken('node-2')).rejects.toMatchObject({
            name: 'NotImplementedError',
            method: 'admin.mintJoinToken',
        });
    });
});
