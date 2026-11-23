// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * batch.importContent runs the server's chunked import session (begin,
 * append?seq=N in 1 MiB chunks, commit; abort on failure), and the format
 * and codec listings come from GET /batch/import/formats.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { RestClient, IMPORT_CHUNK_BYTES } from '../src/rest.js';
import { TransportError } from '../src/errors.js';

interface Call {
    readonly method: string;
    readonly path: string;
    readonly body?: unknown;
    readonly headers: Readonly<Record<string, string>>;
}

function json(status: number, body: unknown): Response {
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/**
 * Stubs fetch with a fake session server: route answers each call; every
 * call is recorded with its path (relative to the base URL) and body.
 */
function stubServer(route: (c: Call) => Response): Call[] {
    const calls: Call[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        const c: Call = {
            method: init?.method ?? 'GET',
            path: url.replace('https://localhost:63002', ''),
            body: init?.body,
            headers: (init?.headers ?? {}) as Record<string, string>,
        };
        calls.push(c);
        return route(c);
    }));
    return calls;
}

async function connected(): Promise<RestClient> {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, { status: 'ok' })));
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

const opts = { format: 'csv', key_field: 'id', value_mode: 'row', filename: 'users.csv' } as const;

describe('batch.importContent', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('begins, appends 1 MiB chunks in sequence and commits', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 's 1' });
            if (c.path.endsWith('/commit')) return json(200, { total: 2, succeeded: 2, failed: 0, errors: [] });
            return new Response(null, { status: 204 });
        });
        const data = new Uint8Array(IMPORT_CHUNK_BYTES * 2 + 5).fill(65);

        const result = await client.batch.importContent(data, opts);

        expect(result).toEqual({ total: 2, succeeded: 2, failed: 0, errors: [] });
        expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
            'POST /batch/import/begin',
            'POST /batch/import/s%201/append?seq=0',
            'POST /batch/import/s%201/append?seq=1',
            'POST /batch/import/s%201/append?seq=2',
            'POST /batch/import/s%201/commit',
        ]);
        const begin = JSON.parse(calls[0]!.body as string) as Record<string, unknown>;
        expect(begin).toMatchObject({ format: 'csv', filename: 'users.csv', key_field: 'id', value_mode: 'row' });
        const sizes = calls.slice(1, 4).map((c) => (c.body as Uint8Array).byteLength);
        expect(sizes).toEqual([IMPORT_CHUNK_BYTES, IMPORT_CHUNK_BYTES, 5]);
    });

    it('sends every session request to the database named in the options', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 's1' });
            if (c.path.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0, errors: [] });
            return new Response(null, { status: 204 });
        });
        const data = new Uint8Array(IMPORT_CHUNK_BYTES + 5).fill(65);

        await client.batch.importContent(data, { ...opts, database: 'orders' });

        expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
            'POST /batch/import/begin',
            'POST /batch/import/s1/append?seq=0',
            'POST /batch/import/s1/append?seq=1',
            'POST /batch/import/s1/commit',
        ]);
        for (const c of calls) {
            expect(c.headers['X-QRDB-Database'], `${c.method} ${c.path}`).toBe('orders');
        }
    });

    it('sends the options database rather than the client scope', async () => {
        const client = (await connected()).databaseScoped('a') as RestClient;
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 's1' });
            if (c.path.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0, errors: [] });
            return new Response(null, { status: 204 });
        });
        const data = new Uint8Array(IMPORT_CHUNK_BYTES + 5).fill(65);

        await client.batch.importContent(data, { ...opts, database: 'orders' });

        expect(calls).toHaveLength(4);
        for (const c of calls) {
            expect(c.headers['X-QRDB-Database'], `${c.method} ${c.path}`).toBe('orders');
        }
    });

    it('sends the abort to the database named in the options', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 's1' });
            if (c.path.includes('/append')) return json(500, { error: 'disk full' });
            return new Response(null, { status: 204 });
        });

        await expect(
            client.batch.importContent(new Uint8Array(5).fill(65), { ...opts, database: 'orders' }),
        ).rejects.toBeDefined();

        expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
            'POST /batch/import/begin',
            'POST /batch/import/s1/append?seq=0',
            'POST /batch/import/s1/abort',
        ]);
        for (const c of calls) {
            expect(c.headers['X-QRDB-Database'], `${c.method} ${c.path}`).toBe('orders');
        }
    });

    it('keeps every session request on the origin it began on across a reconnect', async () => {
        const client = await connected();
        const other = 'https://127.0.0.1:63003';
        const urls: string[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            urls.push(url);
            if (url.endsWith('/batch/import/begin')) {
                // A connect to another server lands right after begin.
                void client.connection.connect(other);
                return json(200, { session_id: 's1' });
            }
            if (url.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0 });
            if (url.endsWith('/health')) return json(200, { status: 'ok' });
            return new Response(null, { status: 204 });
        }));
        // The body is still being read when the first reconnect lands.
        let release: (b: ArrayBuffer) => void = () => undefined;
        const pending = new Promise<ArrayBuffer>((resolve) => {
            release = resolve;
        });
        class SlowBlob extends Blob {
            override arrayBuffer(): Promise<ArrayBuffer> {
                return pending;
            }
        }

        const done = client.batch.importContent(new SlowBlob(['id,name\n1,a\n']), opts);
        void client.connection.connect(other);
        release(new TextEncoder().encode('id,name\n1,a\n').buffer as ArrayBuffer);
        await done;

        const session = urls.filter((u) => u.includes('/batch/import/'));
        expect(session.length).toBe(3);
        for (const u of session) {
            expect(u.startsWith('https://localhost:63002/')).toBe(true);
        }
        // The reconnects themselves did reach the other server.
        expect(urls.some((u) => u.startsWith(other))).toBe(true);
    });

    it('accepts a Blob', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 'b' });
            if (c.path.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0 });
            return new Response(null, { status: 204 });
        });

        await client.batch.importContent(new Blob(['id,name\n1,a\n']), opts);

        expect(new TextDecoder().decode(calls[1]!.body as Uint8Array)).toBe('id,name\n1,a\n');
    });

    it('sends the collection target on begin', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 'c' });
            if (c.path.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0 });
            return new Response(null, { status: 204 });
        });

        await client.batch.importContent(new Blob(['id,name\n1,a\n']), { ...opts, collection: 'people' });

        const begin = JSON.parse(calls[0]!.body as string) as Record<string, unknown>;
        expect(begin.collection).toBe('people');
    });

    it('omits an empty collection so a KV import stays a KV import', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 'k' });
            if (c.path.endsWith('/commit')) return json(200, { total: 1, succeeded: 1, failed: 0 });
            return new Response(null, { status: 204 });
        });

        await client.batch.importContent(new Blob(['id,name\n1,a\n']), { ...opts, collection: '' });

        const begin = JSON.parse(calls[0]!.body as string) as Record<string, unknown>;
        expect(begin).not.toHaveProperty('collection');
    });

    it('aborts the session and rethrows the server refusal when a step fails', async () => {
        const client = await connected();
        const calls = stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 'x' });
            if (c.path.endsWith('/commit')) return json(400, { error: 'batch: parse row 2: bad csv' });
            return new Response(null, { status: 204 });
        });

        const err = await client.batch.importContent(new Uint8Array([1, 2, 3]), opts).catch((e: unknown) => e);

        expect(err).toBeInstanceOf(TransportError);
        expect((err as TransportError).message).toContain('bad csv');
        expect(calls.at(-1)).toMatchObject({ method: 'POST', path: '/batch/import/x/abort' });
    });

    it('keeps the original error when the abort fails too', async () => {
        const client = await connected();
        stubServer((c) => {
            if (c.path === '/batch/import/begin') return json(200, { session_id: 'y' });
            if (c.path.endsWith('/abort')) return json(404, { error: 'session not found' });
            return json(409, { error: 'append out of order' });
        });

        const err = await client.batch.importContent(new Uint8Array([1]), opts).catch((e: unknown) => e);

        expect((err as TransportError).message).toContain('append out of order');
    });

    it('does not abort when begin itself is refused', async () => {
        const client = await connected();
        const calls = stubServer(() => json(403, { error: 'denied' }));

        await expect(client.batch.importContent(new Uint8Array([1]), opts)).rejects.toThrow('denied');
        expect(calls).toHaveLength(1);
    });
});

describe('batch format listings over REST', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('reads formats and codecs from GET /batch/import/formats', async () => {
        const client = await connected();
        const calls = stubServer(() => json(200, { formats: ['auto', 'csv'], codecs: ['json', 'raw'] }));

        expect(await client.batch.availableImportFormats()).toEqual(['auto', 'csv']);
        expect(await client.batch.availableCodecs()).toEqual(['json', 'raw']);
        expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
            'GET /batch/import/formats',
            'GET /batch/import/formats',
        ]);
    });
});
