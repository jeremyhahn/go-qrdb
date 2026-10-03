// Copyright 2026 Jeremy Hahn
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

/**
 * REST contract tests: verify that the SDK sends exactly the URLs and JSON
 * bodies that the Go REST handlers expect.  Each test installs a mock fetch,
 * exercises one SDK method, then asserts on the captured request.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

// ---------------------------------------------------------------------------
// Fetch mock helpers
// ---------------------------------------------------------------------------

/** Recorded details of a single fetch call. */
interface CapturedRequest {
    url: string;
    method: string;
    body: unknown;
    headers: Record<string, string>;
}

/**
 * Installs a global mock fetch that records every call and responds with the
 * provided JSON body (status 200). Returns an array that accumulates records.
 */
function installMockFetch(responseBody: unknown = {}): CapturedRequest[] {
    const calls: CapturedRequest[] = [];

    vi.stubGlobal(
        'fetch',
        async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            const method = init?.method ?? 'GET';
            let body: unknown = undefined;
            if (init?.body && typeof init.body === 'string') {
                try {
                    body = JSON.parse(init.body);
                } catch {
                    body = init.body;
                }
            }
            const headers: Record<string, string> = {};
            if (init?.headers && typeof init.headers === 'object') {
                for (const [k, v] of Object.entries(init.headers as Record<string, string>)) {
                    headers[k.toLowerCase()] = v;
                }
            }
            calls.push({ url, method, body, headers });

            return new Response(JSON.stringify(responseBody), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );

    return calls;
}

/**
 * Creates a RestClient already in the "connected" state using a mock fetch
 * that accepts all requests.  The mock is consumed by the connect handshake;
 * callers should install a fresh mock before exercising the method under test.
 */
async function connectedClient(): Promise<RestClient> {
    installMockFetch({ status: 'ok' });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

// ---------------------------------------------------------------------------
// Bug 1: Transaction target/result/type must be strings, and field names must
//        be snake_case: success_ops, failure_ops, create_revision, mod_revision,
//        range_end, lease_id.
// ---------------------------------------------------------------------------

describe('Bug 1 – transaction wire format', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('maps CompareTarget numbers to server string names', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ succeeded: true, revision: 1, responses: [] });

        await client.transaction.execute({
            compares: [
                { key: 'k', target: 0, result: 0 }, // version / equal
                { key: 'k', target: 1, result: 1 }, // create_revision / greater
                { key: 'k', target: 2, result: 2 }, // mod_revision / less
                { key: 'k', target: 3, result: 3, value: 'v' }, // value / not_equal
            ],
            successOps: [],
            failureOps: [],
        });

        expect(calls).toHaveLength(1);
        const req = calls[0];
        expect(req.url).toBe('https://localhost:63002/tx');
        expect(req.method).toBe('POST');

        const body = req.body as {
            compares: Array<{ target: string; result: string }>;
            success_ops: unknown[];
            failure_ops: unknown[];
        };

        expect(body.compares[0].target).toBe('version');
        expect(body.compares[0].result).toBe('equal');
        expect(body.compares[1].target).toBe('create_revision');
        expect(body.compares[1].result).toBe('greater');
        expect(body.compares[2].target).toBe('mod_revision');
        expect(body.compares[2].result).toBe('less');
        expect(body.compares[3].target).toBe('value');
        expect(body.compares[3].result).toBe('not_equal');
    });

    it('maps OpType numbers to server string names and uses snake_case field names', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ succeeded: true, revision: 1, responses: [] });

        await client.transaction.execute({
            compares: [],
            successOps: [
                { type: 0, key: 'a', value: 'v', leaseId: 42 }, // put
                { type: 1, key: 'b' },                            // get
                { type: 2, key: 'c' },                            // delete
                { type: 3, key: 'd', rangeEnd: 'z' },             // range
            ],
            failureOps: [{ type: 0, key: 'fb', value: 'fv' }],
        });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as {
            success_ops: Array<{
                type: string;
                key: string;
                value?: string;
                range_end?: string;
                lease_id?: number;
            }>;
            failure_ops: Array<{ type: string; key: string; value?: string }>;
        };

        // Top-level field names must be snake_case.
        expect(Object.keys(body)).toContain('success_ops');
        expect(Object.keys(body)).toContain('failure_ops');
        expect(Object.keys(body)).not.toContain('successOps');
        expect(Object.keys(body)).not.toContain('failureOps');

        expect(body.success_ops[0].type).toBe('put');
        expect(body.success_ops[0].lease_id).toBe(42);
        expect(body.success_ops[1].type).toBe('get');
        expect(body.success_ops[2].type).toBe('delete');
        expect(body.success_ops[3].type).toBe('range');
        expect(body.success_ops[3].range_end).toBe('z');

        expect(body.failure_ops[0].type).toBe('put');
    });

    it('serializes compare camelCase fields to snake_case wire names', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ succeeded: false, revision: 0, responses: [] });

        await client.transaction.execute({
            compares: [{
                key: 'k',
                target: 1,
                result: 0,
                createRevision: 5,
                modRevision: 10,
            }],
            successOps: [],
            failureOps: [],
        });

        expect(calls).toHaveLength(1);
        const compare = (calls[0].body as { compares: Array<Record<string, unknown>> }).compares[0];
        expect(compare['create_revision']).toBe(5);
        expect(compare['mod_revision']).toBe(10);
        expect(compare['createRevision']).toBeUndefined();
        expect(compare['modRevision']).toBeUndefined();
    });
});

// ---------------------------------------------------------------------------
// Bug 2: watchPrefix must send key=<prefix>&prefix=true, not prefix=<prefix>.
// ---------------------------------------------------------------------------

describe('Bug 2 – watchPrefix query parameters', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends key=<prefix>&prefix=true for watchPrefix', async () => {
        const client = await connectedClient();

        let capturedUrl = '';
        vi.stubGlobal(
            'fetch',
            async (input: RequestInfo | URL): Promise<Response> => {
                capturedUrl = typeof input === 'string' ? input : (input as URL).toString();
                // Minimal SSE response that ends the stream immediately.
                return new Response(null, {
                    status: 200,
                    headers: { 'Content-Type': 'text/event-stream' },
                });
            },
        );

        const handle = await client.watch.watchPrefix('test-', {});
        await handle.cancel();

        const url = new URL(capturedUrl);
        expect(url.pathname).toBe('/watch');
        expect(url.searchParams.get('key')).toBe('test-');
        expect(url.searchParams.get('prefix')).toBe('true');
        // Must NOT use the prefix value as the parameter name.
        expect(url.searchParams.get('test-')).toBeNull();
    });

    it('exact-key watch does not set prefix=true', async () => {
        const client = await connectedClient();

        let capturedUrl = '';
        vi.stubGlobal(
            'fetch',
            async (input: RequestInfo | URL): Promise<Response> => {
                capturedUrl = typeof input === 'string' ? input : (input as URL).toString();
                return new Response(null, {
                    status: 200,
                    headers: { 'Content-Type': 'text/event-stream' },
                });
            },
        );

        const handle = await client.watch.watch('exact-key', {});
        await handle.cancel();

        const url = new URL(capturedUrl);
        expect(url.searchParams.get('key')).toBe('exact-key');
        expect(url.searchParams.get('prefix')).toBeNull();
    });
});

// ---------------------------------------------------------------------------
// Bug 3: Index query and scan use index_name (snake_case), not indexName.
// ---------------------------------------------------------------------------

describe('Bug 3 – index query parameter naming', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('query sends index_name=<val>&value=<val>', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ keys: [] });

        await client.index.query('my-index', 'search-value');

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/index/query');
        expect(url.searchParams.get('index_name')).toBe('my-index');
        expect(url.searchParams.get('value')).toBe('search-value');
        expect(url.searchParams.get('indexName')).toBeNull();
    });

    it('scan sends index_name=<val>&start=<val>&end=<val>', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ entries: {} });

        await client.index.scan('my-index', 'aaa', 'zzz');

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/index/scan');
        expect(url.searchParams.get('index_name')).toBe('my-index');
        expect(url.searchParams.get('start')).toBe('aaa');
        expect(url.searchParams.get('end')).toBe('zzz');
        expect(url.searchParams.get('indexName')).toBeNull();
    });
});

// ---------------------------------------------------------------------------
// Bug 4: updateProtocol must send PATCH /v1/databases/{name}/protocol with
//         {mysql, postgresql} body. (It was named updateSQL until the name
//         was taken by the SQL settings, matching the Go SDK's
//         UpdateDatabaseProtocol / UpdateDatabaseSQL.)
// ---------------------------------------------------------------------------

describe('Bug 4 – database protocol endpoint path and body', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends PATCH /v1/databases/{name}/protocol with mysql+postgresql body', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'default',
            engine: 'mvcc',
            backend: 'pebble',
            protocol: { mysql: true, postgresql: false },
        });

        await client.database.updateProtocol('default', { mysql: true, postgresql: false });

        expect(calls).toHaveLength(1);
        const req = calls[0];
        const url = new URL(req.url);
        expect(url.pathname).toBe('/v1/databases/default/protocol');
        expect(req.method).toBe('PATCH');

        const body = req.body as { mysql: boolean; postgresql: boolean };
        expect(body.mysql).toBe(true);
        expect(body.postgresql).toBe(false);
    });

    it('percent-encodes the database name in the URL path', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'my db',
            engine: 'mvcc',
            backend: 'pebble',
            protocol: { mysql: false, postgresql: true },
        });

        await client.database.updateProtocol('my db', { mysql: false, postgresql: true });

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/v1/databases/my%20db/protocol');
    });
});

// ---------------------------------------------------------------------------
// updateSQL sets a database's SQL settings -- its concurrency mode -- with
// PATCH /v1/databases/{name}/sql and a {"sql": {...}} body, the same request
// the Go SDK's UpdateDatabaseSQL sends. Omitting the settings restores the
// defaults.
// ---------------------------------------------------------------------------

describe('database SQL settings (concurrency)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends PATCH /v1/databases/{name}/sql with {sql: {concurrency}}', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            status: 'open',
            sql: { concurrency: 'pessimistic' },
        });

        const info = await client.database.updateSQL('shop', { concurrency: 'pessimistic' });

        expect(calls).toHaveLength(1);
        const req = calls[0];
        expect(new URL(req.url).pathname).toBe('/v1/databases/shop/sql');
        expect(req.method).toBe('PATCH');
        expect(req.body).toEqual({ sql: { concurrency: 'pessimistic' } });
        expect(info.sql?.concurrency).toBe('pessimistic');
    });

    it('omitting the settings sends no sql, restoring the defaults', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'shop', engine: 'kv', backend: 'pebble', status: 'open' });

        const info = await client.database.updateSQL('shop');

        expect(calls).toHaveLength(1);
        expect(calls[0].body).toEqual({});
        expect(info.sql).toBeUndefined();
    });

    it('percent-encodes the database name', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'my db', engine: 'kv', backend: 'pebble', status: 'open' });

        await client.database.updateSQL('my db', { concurrency: 'optimistic' });

        expect(new URL(calls[0].url).pathname).toBe('/v1/databases/my%20db/sql');
    });

    it('creates a database with sql settings and protocol settings side by side', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            status: 'open',
            protocol: { mysql: true, postgresql: false },
            sql: { concurrency: 'pessimistic' },
        });

        const info = await client.database.create({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            protocol: { mysql: true, postgresql: false },
            sql: { concurrency: 'pessimistic' },
        });

        const body = calls[0].body as Record<string, unknown>;
        expect(body.protocol).toEqual({ mysql: true, postgresql: false });
        expect(body.sql).toEqual({ concurrency: 'pessimistic' });
        expect(info.protocol?.mysql).toBe(true);
        expect(info.sql?.concurrency).toBe('pessimistic');
    });
});

// ---------------------------------------------------------------------------
// updateCompaction sets a database's scheduled-compaction policy with
// PATCH /v1/databases/{name}/compaction and a {"compaction": {...}} body, the
// same request the Go SDK's UpdateDatabaseCompaction sends: camelCase
// retainRevisions/retainPeriod, exactly the server's field names. Omitting the
// policy turns it off.
// ---------------------------------------------------------------------------

describe('database compaction policy', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends PATCH /v1/databases/{name}/compaction with a revision policy', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            status: 'open',
            compaction: { mode: 'revision', retainRevisions: 1000 },
        });

        const info = await client.database.updateCompaction('shop', { mode: 'revision', retainRevisions: 1000 });

        expect(calls).toHaveLength(1);
        const req = calls[0];
        expect(new URL(req.url).pathname).toBe('/v1/databases/shop/compaction');
        expect(req.method).toBe('PATCH');
        expect(req.body).toEqual({ compaction: { mode: 'revision', retainRevisions: 1000 } });
        expect(info.compaction).toEqual({ mode: 'revision', retainRevisions: 1000 });
    });

    it('sends a period policy with its Go duration untouched', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'shop',
            engine: 'kv',
            backend: 'pebble',
            status: 'open',
            compaction: { mode: 'period', retainPeriod: '24h' },
        });

        const info = await client.database.updateCompaction('shop', { mode: 'period', retainPeriod: '24h' });

        expect(calls[0].body).toEqual({ compaction: { mode: 'period', retainPeriod: '24h' } });
        expect(info.compaction?.mode).toBe('period');
        expect(info.compaction?.retainPeriod).toBe('24h');
    });

    it('omitting the policy sends no compaction, turning it off', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'shop', engine: 'kv', backend: 'pebble', status: 'open' });

        const info = await client.database.updateCompaction('shop');

        expect(calls).toHaveLength(1);
        expect(calls[0].body).toEqual({});
        expect(info.compaction).toBeUndefined();
    });

    it('percent-encodes the database name', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'my db', engine: 'kv', backend: 'pebble', status: 'open' });

        await client.database.updateCompaction('my db', { mode: 'off' });

        expect(new URL(calls[0].url).pathname).toBe('/v1/databases/my%20db/compaction');
    });

    it('surfaces a refused policy as a transport error carrying the status', async () => {
        const client = await connectedClient();
        vi.stubGlobal('fetch', vi.fn(async () => new Response(
            JSON.stringify({ error: 'compaction: invalid policy' }),
            { status: 400, headers: { 'Content-Type': 'application/json' } },
        )));

        await expect(client.database.updateCompaction('shop', { mode: 'revision' }))
            .rejects.toMatchObject({ statusCode: 400 });
    });
});

// ---------------------------------------------------------------------------
// The platform's node-keyspace compaction policy: GET and PUT
// /admin/node-compaction with a {"compaction": {...}} body, the request the Go
// SDK's SetNodeCompaction sends. Omitting the policy turns it off.
// ---------------------------------------------------------------------------

describe('admin node-keyspace compaction policy', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('reads it with GET /admin/node-compaction', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ compaction: { mode: 'revision', retainRevisions: 100 } });

        const policy = await client.admin.nodeCompaction();

        expect(calls).toHaveLength(1);
        expect(new URL(calls[0].url).pathname).toBe('/admin/node-compaction');
        expect(calls[0].method).toBe('GET');
        expect(policy).toEqual({ mode: 'revision', retainRevisions: 100 });
    });

    it('reports no policy as undefined', async () => {
        const client = await connectedClient();
        installMockFetch({});

        expect(await client.admin.nodeCompaction()).toBeUndefined();
    });

    it('sets it with PUT /admin/node-compaction', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ compaction: { mode: 'period', retainPeriod: '720h' } });

        const policy = await client.admin.setNodeCompaction({ mode: 'period', retainPeriod: '720h' });

        expect(new URL(calls[0].url).pathname).toBe('/admin/node-compaction');
        expect(calls[0].method).toBe('PUT');
        expect(calls[0].body).toEqual({ compaction: { mode: 'period', retainPeriod: '720h' } });
        expect(policy).toEqual({ mode: 'period', retainPeriod: '720h' });
    });

    it('omitting the policy sends no compaction, turning it off', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        const policy = await client.admin.setNodeCompaction();

        expect(calls[0].body).toEqual({});
        expect(policy).toBeUndefined();
    });

    it('surfaces a tenant caller\'s refusal as a transport error carrying the status', async () => {
        const client = await connectedClient();
        vi.stubGlobal('fetch', vi.fn(async () => new Response(
            JSON.stringify({ error: 'permission denied' }),
            { status: 403, headers: { 'Content-Type': 'application/json' } },
        )));

        await expect(client.admin.setNodeCompaction({ mode: 'revision', retainRevisions: 1 }))
            .rejects.toMatchObject({ statusCode: 403 });
    });
});

// ---------------------------------------------------------------------------
// Minting a member-side CSR join token: POST /v1/cluster/join-tokens with a
// {"node_id": ..., "ttl_seconds": ...} body, snake_case as the Go REST
// handler (handleMintJoinToken) decodes it, and a {"token", "expires_at"}
// response the SDK converts back to camelCase.
// ---------------------------------------------------------------------------

describe('admin.mintJoinToken', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('mints with POST /v1/cluster/join-tokens, snake_case body', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ token: 'tok_abc123', expires_at: '2026-01-01T00:00:00Z' });

        const resp = await client.admin.mintJoinToken('node-7', 3600);

        expect(calls).toHaveLength(1);
        expect(new URL(calls[0].url).pathname).toBe('/v1/cluster/join-tokens');
        expect(calls[0].method).toBe('POST');
        expect(calls[0].body).toEqual({ node_id: 'node-7', ttl_seconds: 3600 });
        expect(resp).toEqual({ token: 'tok_abc123', expiresAt: '2026-01-01T00:00:00Z' });
    });

    it('omitting ttlSeconds sends 0, letting the server apply its default TTL', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ token: 'tok_def456', expires_at: '2026-01-01T01:00:00Z' });

        await client.admin.mintJoinToken('node-8');

        expect(calls[0].body).toEqual({ node_id: 'node-8', ttl_seconds: 0 });
    });

    it('surfaces a tenant caller\'s ScopeCross refusal as a transport error carrying the status', async () => {
        const client = await connectedClient();
        vi.stubGlobal('fetch', vi.fn(async () => new Response(
            JSON.stringify({ error: 'permission denied' }),
            { status: 403, headers: { 'Content-Type': 'application/json' } },
        )));

        await expect(client.admin.mintJoinToken('node-9', 60))
            .rejects.toMatchObject({ statusCode: 403 });
    });
});

// ---------------------------------------------------------------------------
// Outbound snake_case transform — camelCase SDK fields must arrive as
// snake_case in the serialized JSON body that the Go server receives.
// These are the request types that were silently broken before jsonBody()
// applied deepCamelToSnake: isolationType→isolation_type, tenantId→tenant_id,
// maxDatabases→max_databases, createRevision→create_revision, etc.
// ---------------------------------------------------------------------------

describe('Outbound transform — tenants.create sends isolation_type', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('isolationType is serialized as isolation_type in POST body', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ tenant: { id: 't1', name: 'T', isolation_type: 'prefix' } });

        await client.tenants.create({ id: 't1', name: 'T', isolationType: 'prefix' });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        // Must arrive as snake_case — Go struct tag is json:"isolation_type".
        expect(body['isolation_type']).toBe('prefix');
        // camelCase key must NOT be present.
        expect(body['isolationType']).toBeUndefined();
        expect(body['id']).toBe('t1');
        expect(body['name']).toBe('T');
    });

    it('tenants.update sends isolation_type in PUT body', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ tenant: { id: 't1', name: 'T', isolation_type: 'prefix' } });

        await client.tenants.update('t1', { isolationType: 'prefix' });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['isolation_type']).toBe('prefix');
        expect(body['isolationType']).toBeUndefined();
    });
});

describe('Outbound transform — tenants.barrierInit sends tenant_id', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('tenantId is serialized as tenant_id', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.tenants.barrierInit({ tenantId: 'abc', threshold: 2, shares: 3 });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['tenant_id']).toBe('abc');
        expect(body['tenantId']).toBeUndefined();
        expect(body['threshold']).toBe(2);
        expect(body['shares']).toBe(3);
    });
});

describe('Outbound transform — tenants.updateQuotas sends snake_case quota fields', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('POSTs a FLAT snake_case quota body (no wrapper, no tenant_id)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ id: 'q1' });

        await client.tenants.updateQuotas({
            tenantId: 'q1',
            quotas: {
                maxDatabases: 5,
                maxDedicatedWalGroups: 2,
                maxRaftGroups: 3,
                maxReplicasPerGroup: 3,
                maxTotalReplicas: 9,
                maxCollections: 10,
                maxKeys: 1000,
                maxStorageBytes: 500000,
                maxDatabaseSizeBytes: 250000,
                maxUsers: 20,
                maxRoles: 10,
                maxCertificates: 50,
                maxBackups: 5,
                maxVectorDims: 512,
                maxTSMetrics: 100,
            },
        });

        expect(calls).toHaveLength(1);
        // The canonical REST route is POST /v1/tenants/{id}/quotas with a flat
        // body decoded into setTenantQuotasRequest (top-level max_* fields).
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toContain('/v1/tenants/q1/quotas');
        const body = calls[0].body as Record<string, unknown>;
        // The flat quota fields appear at the top level, not under a wrapper.
        expect(body['quotas']).toBeUndefined();
        expect(body['tenant_id']).toBeUndefined();
        expect(body['max_databases']).toBe(5);
        expect(body['max_keys']).toBe(1000);
        expect(body['max_storage_bytes']).toBe(500000);
        expect(body['max_database_size_bytes']).toBe(250000);
        expect(body['max_raft_groups']).toBe(3);
        expect(body['max_replicas_per_group']).toBe(3);
        expect(body['max_total_replicas']).toBe(9);
        expect(body['max_users']).toBe(20);
        expect(body['max_dedicated_wal_groups']).toBe(2);
        // camelCase originals must not appear.
        expect(body['maxDatabases']).toBeUndefined();
        expect(body['maxKeys']).toBeUndefined();
    });
});

describe('Outbound transform — tenants.updateEntitlements PATCHes snake_case fields', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends a PATCH to /entitlements with snake_case entitlement fields', async () => {
        const client = await connectedClient();
        // The real PATCH /v1/tenants/{id}/entitlements handler returns the BARE
        // updated tenant (not a {tenant} wrapper), exactly like the quotas
        // handler. The SDK must wrap it to honor UpdateTenantEntitlementsResponse.
        const calls = installMockFetch({ id: 'e1', name: 'E One' });

        const resp = await client.tenants.updateEntitlements({
            tenantId: 'e1',
            entitlements: {
                allowedIsolationModes: ['prefix', 'volume'],
                allowDedicatedRaft: true,
                allowVolumeStorage: false,
            },
        });

        expect(calls).toHaveLength(1);
        expect(calls[0].method).toBe('PATCH');
        expect(calls[0].url).toContain('/v1/tenants/e1/entitlements');
        const body = calls[0].body as Record<string, unknown>;
        expect(body['allowed_isolation_modes']).toEqual(['prefix', 'volume']);
        expect(body['allow_dedicated_raft']).toBe(true);
        expect(body['allow_volume_storage']).toBe(false);
        // camelCase originals must not appear.
        expect(body['allowDedicatedRaft']).toBeUndefined();
        // The bare-tenant response is wrapped to match the SDK contract so the
        // UI can read resp.tenant (mirrors updateQuotas).
        expect(resp.tenant.id).toBe('e1');
    });
});

describe('Outbound transform — membership endpoints preserve existing snake_case', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('addMember hand-written shard_id/replica_id survive deepCamelToSnake', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.admin.addMember(7, 3, '10.0.0.1:63001');

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        // These were hand-written snake_case — must not double-transform.
        expect(body['shard_id']).toBe(7);
        expect(body['replica_id']).toBe(3);
        expect(body['address']).toBe('10.0.0.1:63001');
    });

    it('removeMember preserves shard_id and replica_id', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.admin.removeMember(2, 1);

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['shard_id']).toBe(2);
        expect(body['replica_id']).toBe(1);
    });

    it('requestSnapshot preserves shard_id', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.admin.requestSnapshot(5);

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['shard_id']).toBe(5);
    });
});

describe('Outbound transform — users.create body contains correct field names', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('users.create username is emitted unchanged', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ id: 'u1', username: 'alice' });

        // SDK CreateUserRequest uses tenantId/roleIds (camelCase with lowercase d/s).
        // deepCamelToSnake converts them to tenant_id/role_ids which are correct
        // snake_case equivalents. The Go handler uses json:"tenantID"/json:"roleIDs"
        // (uppercase acronyms), which is a pre-existing server-side inconsistency
        // separate from the camelCase outbound bug fixed here.
        await client.users.create({ username: 'alice' });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['username']).toBe('alice');
    });

    it('users.create roleIds is serialized as role_ids (snake_case of SDK field)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ id: 'u1', username: 'bob' });

        await client.users.create({ username: 'bob', roleIds: ['editor', 'viewer'] });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        expect(body['role_ids']).toEqual(['editor', 'viewer']);
        // camelCase key must not appear.
        expect(body['roleIds']).toBeUndefined();
    });
});

// ---------------------------------------------------------------------------
// Inbound transform — kv.scan must base64-decode each value so the REST client
// returns the same plaintext the Wails client does (KVBinding.Scan uses
// string(v)) and stays consistent with the REST single-key get(). Go marshals
// the scan response's map[string][]byte as base64, so undecoded values would
// surface base64 in the UI and corrupt edits on save.
// ---------------------------------------------------------------------------

describe('Inbound transform — kv.scan base64-decodes values to plaintext', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('decodes base64 scan values to UTF-8 plaintext', async () => {
        const client = await connectedClient();
        // "aGVsbG8tZW5jcnlwdGVkLXdvcmxk" === base64("hello-encrypted-world").
        installMockFetch({ pairs: { 'chrometest/key1': 'aGVsbG8tZW5jcnlwdGVkLXdvcmxk' } });

        const result = await client.kv.scan('chrometest');

        expect(result).toEqual({ 'chrometest/key1': 'hello-encrypted-world' });
    });

    it('decodes multibyte UTF-8 scan values correctly', async () => {
        const client = await connectedClient();
        // "aMOpbGxv" === base64(UTF-8 bytes of "héllo").
        installMockFetch({ pairs: { 'chrometest/accent': 'aMOpbGxv' } });

        const result = await client.kv.scan('chrometest');

        expect(result).toEqual({ 'chrometest/accent': 'héllo' });
    });
});

// ---------------------------------------------------------------------------
// mvcc.restoreToRevision — request encodes prefix/keys as base64, response
// decodes changes/conflicts keys back to plaintext.
// ---------------------------------------------------------------------------

describe('mvcc.restoreToRevision wire contract', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('POSTs /kv/restore with base64-encoded prefix and snake_case fields', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            revision: 5,
            changes: [],
            applied: 0,
            skipped: 0,
            conflicted: 0,
            dry_run: false,
        });

        await client.mvcc.restoreToRevision({ revision: 5, prefix: 'prefix' });

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/kv/restore');
        expect(calls[0].method).toBe('POST');
        // "cHJlZml4" === base64("prefix").
        expect(calls[0].body).toEqual({ revision: 5, prefix: 'cHJlZml4' });
    });

    it('POSTs base64-encoded keys, dry_run, and force', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            revision: 5,
            changes: [],
            applied: 0,
            skipped: 0,
            conflicted: 0,
            dry_run: true,
        });

        await client.mvcc.restoreToRevision({
            revision: 5,
            keys: ['a', 'b'],
            dryRun: true,
            force: true,
        });

        expect(calls).toHaveLength(1);
        // "YQ==" === base64("a"), "Yg==" === base64("b").
        expect(calls[0].body).toEqual({
            revision: 5,
            keys: ['YQ==', 'Yg=='],
            dry_run: true,
            force: true,
        });
    });

    it('decodes response changes/conflicts key fields from base64 to plaintext', async () => {
        const client = await connectedClient();
        installMockFetch({
            revision: 5,
            changes: [
                { key: 'aGVsbG8=', action: 'revert', from_revision: 9, to_revision: 5, value_bytes: 3 },
            ],
            conflicts: [
                { key: 'd29ybGQ=', action: 'revert', from_revision: 9, to_revision: 5, value_bytes: 3 },
            ],
            applied: 1,
            skipped: 0,
            conflicted: 1,
            dry_run: false,
        });

        const result = await client.mvcc.restoreToRevision({ revision: 5, prefix: 'p' });

        expect(result.revision).toBe(5);
        expect(result.applied).toBe(1);
        expect(result.conflicted).toBe(1);
        expect(result.changes[0].key).toBe('hello');
        // snake_case → camelCase field transform applied automatically.
        expect(result.changes[0].fromRevision).toBe(9);
        expect(result.changes[0].toRevision).toBe(5);
        expect(result.changes[0].valueBytes).toBe(3);
        expect(result.conflicts?.[0]?.key).toBe('world');
    });
});

// ---------------------------------------------------------------------------
// Active-database scope — a databaseScoped() client injects X-QRDB-Database on
// data-plane requests, the unscoped client never sends it, and management/admin
// requests never carry it even on a scoped client.
// ---------------------------------------------------------------------------

describe('Active-database scope — X-QRDB-Database header injection', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('scoped kv.put sends X-QRDB-Database: db2', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.databaseScoped('db2').kv.put('k', 'v');

        expect(calls).toHaveLength(1);
        expect(calls[0].headers['x-qrdb-database']).toBe('db2');
    });

    it('scoped kv.get sends X-QRDB-Database: db2', async () => {
        const client = await connectedClient();
        const calls = installMockFetch('value');

        await client.databaseScoped('db2').kv.get('k');

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/kv/k');
        expect(calls[0].headers['x-qrdb-database']).toBe('db2');
    });

    it('scoped kv.scan sends X-QRDB-Database: db2', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ pairs: {} });

        await client.databaseScoped('db2').kv.scan('prefix');

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/kv/scan');
        expect(calls[0].headers['x-qrdb-database']).toBe('db2');
    });

    it('scoped data-plane calls beyond kv also carry the header', async () => {
        const client = await connectedClient().then((c) => c.databaseScoped('analytics'));
        const calls = installMockFetch({ succeeded: true, revision: 1, responses: [] });

        await client.transaction.execute({ compares: [], successOps: [], failureOps: [] });
        await client.index.list();
        await client.lease.list();

        expect(calls).toHaveLength(3);
        for (const call of calls) {
            expect(call.headers['x-qrdb-database']).toBe('analytics');
        }
    });

    it('unscoped client sends NO X-QRDB-Database header', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.kv.put('k', 'v');

        expect(calls).toHaveLength(1);
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });

    it('databaseScoped("") returns an unscoped client (no header)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.databaseScoped('').kv.put('k', 'v');

        expect(calls).toHaveLength(1);
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });

    it('databaseScoped("system") returns an unscoped client (no header)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        await client.databaseScoped('system').kv.put('k', 'v');

        expect(calls).toHaveLength(1);
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });

    it('admin call on a scoped client does NOT carry the header (databases.list)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ databases: [] });

        await client.databaseScoped('db2').database.list();

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/v1/databases/list');
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });

    it('admin call on a scoped client does NOT carry the header (users.list)', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ users: [], total: 0 });

        await client.databaseScoped('db2').users.list({});

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/users/list');
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });

    it('does not mutate the source client when scoping', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({});

        client.databaseScoped('db2');
        // The original client remains unscoped after deriving a scoped one.
        await client.kv.put('k', 'v');

        expect(calls).toHaveLength(1);
        expect(calls[0].headers['x-qrdb-database']).toBeUndefined();
    });
});

// ---------------------------------------------------------------------------
// database.create carries the per-database barrier passphrase: the server
// initializes the barrier from barrier.secret when barrier.enabled is set.
// ---------------------------------------------------------------------------

describe('Outbound — database.create sends the barrier secret', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('posts barrier.enabled, mode and secret to /v1/databases', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'orders', engine: 'kv', backend: 'pebble', status: 'open' });

        await client.database.create({
            name: 'orders',
            engine: 'kv',
            backend: 'pebble',
            dataDir: '/var/lib/qrdb/orders',
            barrier: { enabled: true, mode: 'passphrase', secret: 'correct horse' },
        });

        expect(calls).toHaveLength(1);
        expect(calls[0].url).toBe('https://localhost:63002/v1/databases');
        expect(calls[0].method).toBe('POST');
        const body = calls[0].body as { barrier: Record<string, unknown> };
        expect(body.barrier).toEqual({ enabled: true, mode: 'passphrase', secret: 'correct horse' });
    });
});
