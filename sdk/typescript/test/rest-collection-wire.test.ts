// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * The REST collection client returns the handlers' snake_case JSON as
 * declared by CollectionInfo / ListDocumentsResponse (the shape the Wails
 * transport returns), and never renames the keys of a user's document.
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { RestClient } from '../src/index.js';

function stubServer(routes: Record<string, unknown>): void {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input)).pathname;
        if (path === '/health') {
            return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
        }
        if (path in routes) {
            return new Response(JSON.stringify(routes[path]), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        }
        return new Response('not found', { status: 404 });
    }));
}

async function connected(): Promise<RestClient> {
    const c = new RestClient('http://qrdb.test');
    await c.connection.connect('http://qrdb.test');
    return c;
}

const info = {
    name: 'users',
    database: 'app',
    id_strategy: 'field_hash',
    field_hash: 'email',
    schema: { first_name: 'string' },
    document_count: 2,
    storage_codec: 'json',
    created_at: '2026-11-12T00:00:00Z',
};

describe('REST collection client wire shape', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('get, create and list return CollectionInfo in its declared snake_case', async () => {
        stubServer({
            '/v1/collections': info,
            '/v1/collections/app/users': info,
            '/v1/collections/list': { collections: [info] },
        });
        const c = await connected();
        const got = await c.collection.get('app', 'users');
        expect(got.id_strategy).toBe('field_hash');
        expect(got.field_hash).toBe('email');
        expect(got.document_count).toBe(2);
        expect(got.storage_codec).toBe('json');
        expect(got.created_at).toBe('2026-11-12T00:00:00Z');
        expect(got.schema).toEqual({ first_name: 'string' });
        const created = await c.collection.create({ database: 'app', name: 'users', id_strategy: 'field_hash', field_hash: 'email' });
        expect(created.id_strategy).toBe('field_hash');
        const listed = await c.collection.list('app');
        expect(listed[0].document_count).toBe(2);
    });

    it('documents keep their own keys and the page fields stay snake_case', async () => {
        const doc = { id: '7', document: { first_name: 'Ada', last_login_at: 3, nested_obj: { inner_key: 1 } } };
        stubServer({
            '/v1/collections/app/users/documents/7': doc,
            '/v1/collections/app/users/documents': { documents: [doc], next_page_token: 'tok', total_count: 2 },
        });
        const c = await connected();
        const got = await c.collection.getDocument('app', 'users', '7');
        expect(got).toEqual(doc);
        const page = await c.collection.listDocuments({ database: 'app', collection: 'users', page_size: 1 });
        expect(page.next_page_token).toBe('tok');
        expect(page.total_count).toBe(2);
        expect(page.documents[0].document).toEqual(doc.document);
    });
});
