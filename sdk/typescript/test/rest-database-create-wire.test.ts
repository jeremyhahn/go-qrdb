// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * The REST database client's create body carries protocol.etcd under the
 * handler's json tags (pkg/api/rest/handlers_database.go etcdConfigJSON).
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { RestClient } from '../src/index.js';

describe('REST database create wire body', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends protocol.etcd with enabled, listenAddr and advertiseClientUrls', async () => {
        const bodies: unknown[] = [];
        vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
            const path = new URL(String(input)).pathname;
            if (path === '/health') {
                return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
            }
            if (path === '/v1/databases') {
                bodies.push(JSON.parse(String(init?.body)));
                return new Response(JSON.stringify({ name: 'app', backend: 'memory', status: 'open' }), { status: 200 });
            }
            return new Response('not found', { status: 404 });
        }));
        const c = new RestClient('http://qrdb.test');
        await c.connection.connect('http://qrdb.test');
        await c.database.create({
            name: 'app',
            backend: 'memory',
            protocol: {
                mysql: false,
                postgresql: true,
                etcd: { enabled: true, listenAddr: '127.0.0.1:2379', advertiseClientUrls: ['http://127.0.0.1:2379'] },
            },
        });
        expect(bodies).toEqual([{
            name: 'app',
            backend: 'memory',
            protocol: {
                mysql: false,
                postgresql: true,
                etcd: { enabled: true, listenAddr: '127.0.0.1:2379', advertiseClientUrls: ['http://127.0.0.1:2379'] },
            },
        }]);
    });
});
