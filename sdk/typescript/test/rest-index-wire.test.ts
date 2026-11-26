// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * The REST index client sends every IndexCreateRequest flag under the
 * handler's json tag (pkg/api/rest/handlers_index.go indexCreateRequest).
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { RestClient } from '../src/index.js';

describe('REST index create wire body', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends name, field_name, unique, sparse, numeric and case_insensitive', async () => {
        const bodies: unknown[] = [];
        vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
            const path = new URL(String(input)).pathname;
            if (path === '/health') {
                return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
            }
            if (path === '/index/create') {
                bodies.push(JSON.parse(String(init?.body)));
                return new Response(null, { status: 204 });
            }
            return new Response('not found', { status: 404 });
        }));
        const c = new RestClient('http://qrdb.test');
        await c.connection.connect('http://qrdb.test');
        await c.index.create({
            name: 'by_email',
            fieldName: 'email',
            unique: true,
            sparse: true,
            numeric: false,
            caseInsensitive: true,
        });
        expect(bodies).toEqual([{
            name: 'by_email',
            field_name: 'email',
            unique: true,
            sparse: true,
            numeric: false,
            case_insensitive: true,
        }]);
    });
});
