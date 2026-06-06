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
// Bug 4: updateSQL must send PATCH /v1/databases/{name}/protocol with
//         {mysql, postgresql} body, not PATCH /databases/{name}/sql.
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

        await client.database.updateSQL('default', { mysql: true, postgresql: false });

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

        await client.database.updateSQL('my db', { mysql: false, postgresql: true });

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/v1/databases/my%20db/protocol');
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

    it('maxDatabases, maxKeys, maxStorageBytes all become snake_case', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ tenant: { id: 'q1' } });

        await client.tenants.updateQuotas({
            tenantId: 'q1',
            quotas: {
                maxDatabases: 5,
                maxRaftGroups: 3,
                maxCollections: 10,
                maxKeys: 1000,
                maxStorageBytes: 500000,
                maxUsers: 20,
                maxRoles: 10,
                maxCertificates: 50,
                maxBackups: 5,
                maxVectorDims: 512,
                maxTSMetrics: 100,
            },
        });

        expect(calls).toHaveLength(1);
        const body = calls[0].body as Record<string, unknown>;
        // tenantId is present in body (alongside URL path) as tenant_id.
        expect(body['tenant_id']).toBe('q1');
        const quotas = body['quotas'] as Record<string, unknown>;
        // Simple camelCase fields convert correctly.
        expect(quotas['max_databases']).toBe(5);
        expect(quotas['max_keys']).toBe(1000);
        expect(quotas['max_storage_bytes']).toBe(500000);
        expect(quotas['max_raft_groups']).toBe(3);
        expect(quotas['max_users']).toBe(20);
        // camelCase originals must not appear.
        expect(quotas['maxDatabases']).toBeUndefined();
        expect(quotas['maxKeys']).toBeUndefined();
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
