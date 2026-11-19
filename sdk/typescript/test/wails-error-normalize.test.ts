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

// The Wails runtime rejects a Go method's error as its message string. The
// WailsClient must hand callers an Error carrying that message.

import { describe, it, expect, vi, afterEach } from 'vitest';
import { WailsClient, toError } from '../src/wails.js';

function installUserBinding(get: (id: string) => Promise<unknown>): void {
    vi.stubGlobal('window', { go: { main: { UserBinding: { Get: get } } } });
}

describe('WailsClient binding rejections', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('turns a string rejection into an Error with that message', async () => {
        installUserBinding(() => Promise.reject('user not found'));
        const client = new WailsClient();
        const err = await client.users.get('u1').then(
            () => null,
            (e: unknown) => e,
        );
        expect(err).toBeInstanceOf(Error);
        expect((err as Error).message).toBe('user not found');
    });

    it('passes an Error rejection through unchanged', async () => {
        const original = new Error('permission denied');
        installUserBinding(() => Promise.reject(original));
        const client = new WailsClient();
        await expect(client.users.get('u1')).rejects.toBe(original);
    });

    it('normalizes non-string, non-Error rejections', () => {
        expect(toError({ message: 'boom' }).message).toBe('boom');
        expect(toError(42).message).toBe('42');
    });
});
