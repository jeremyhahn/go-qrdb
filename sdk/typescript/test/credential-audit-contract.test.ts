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
 * REST contract tests for the CredentialClient and AuditQueryClient
 * namespaces: verify that the SDK sends exactly the URLs and JSON bodies
 * that the Go REST handlers (pkg/api/rest/handlers_credentials.go,
 * pkg/api/rest/handlers_audit.go) expect.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

// ---------------------------------------------------------------------------
// Fetch mock helpers (mirrors rest-contract.test.ts)
// ---------------------------------------------------------------------------

interface CapturedRequest {
    url: string;
    method: string;
    body: unknown;
}

function installMockFetch(responseBody: unknown = {}, status = 200): CapturedRequest[] {
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
            calls.push({ url, method, body });

            if (status === 204) {
                return new Response(null, { status: 204 });
            }
            return new Response(JSON.stringify(responseBody), {
                status,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );

    return calls;
}

async function connectedClient(): Promise<RestClient> {
    installMockFetch({ status: 'ok' });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

beforeEach(() => {
    vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// CredentialClient
// ---------------------------------------------------------------------------

describe('CredentialClient — REST wire contract', () => {
    it('submit POSTs /v1/credentials/submit with name+value', async () => {
        const client = await connectedClient();
        const calls = installMockFetch(null, 204);

        await client.credentials.submit('db-password', 'hunter2');

        expect(calls).toHaveLength(1);
        expect(calls[0]!.method).toBe('POST');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/submit');
        // The wire field is []byte: base64 of the UTF-8 value.
        expect(calls[0]!.body).toEqual({ name: 'db-password', value: 'aHVudGVyMg==' });
    });

    it('store POSTs /v1/credentials/store with name+value', async () => {
        const client = await connectedClient();
        const calls = installMockFetch(null, 204);

        await client.credentials.store('api-key', 'abc123');

        expect(calls[0]!.method).toBe('POST');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/store');
        expect(calls[0]!.body).toEqual({ name: 'api-key', value: 'YWJjMTIz' });
    });

    it('retrieve GETs /v1/credentials/{name}/value and returns the value', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ value: 'c2VjcmV0LXZhbHVl' }); // base64('secret-value')

        const value = await client.credentials.retrieve('api-key');

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/api-key/value');
        expect(value).toBe('secret-value');
    });

    it('retrieve percent-encodes the credential name', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ value: 'eA==' });

        await client.credentials.retrieve('db/prod key');

        expect(calls[0]!.url).toBe(
            'https://localhost:63002/v1/credentials/db%2Fprod%20key/value',
        );
    });

    it('list GETs /v1/credentials and returns names', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ names: ['a', 'b', 'c'] });

        const names = await client.credentials.list();

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials');
        expect(names).toEqual(['a', 'b', 'c']);
    });

    it('list returns an empty array when names is absent', async () => {
        const client = await connectedClient();
        installMockFetch({});

        const names = await client.credentials.list();

        expect(names).toEqual([]);
    });

    it('strategy GETs /v1/credentials/strategy and returns the strategy string', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ strategy: 'manual' });

        const strategy = await client.credentials.strategy();

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/strategy');
        expect(strategy).toBe('manual');
    });

    it('delete DELETEs /v1/credentials/{name}', async () => {
        const client = await connectedClient();
        const calls = installMockFetch(null, 204);

        await client.credentials.delete('stale-key');

        expect(calls[0]!.method).toBe('DELETE');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/stale-key');
    });

    it('rotate POSTs /v1/credentials/{name}/rotate with snake_case body', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'db-password',
            previous_version: 1,
            new_version: 2,
            grace_until_unix: 1700000000,
        });

        const result = await client.credentials.rotate('db-password', 'newvalue', 3600);

        expect(calls[0]!.method).toBe('POST');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/db-password/rotate');
        expect(calls[0]!.body).toEqual({
            name: 'db-password',
            new_value: 'bmV3dmFsdWU=', // base64('newvalue')
            grace_period_seconds: 3600,
        });
        expect(result).toEqual({
            name: 'db-password',
            previousVersion: 1,
            newVersion: 2,
            graceUntilUnix: 1700000000,
        });
    });

    it('rotate defaults gracePeriodSeconds to 0 when omitted', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ name: 'k', previous_version: 1, new_version: 2 });

        await client.credentials.rotate('k', 'v2');

        expect(calls[0]!.body).toEqual({
            name: 'k',
            new_value: 'djI=', // base64('v2')
            grace_period_seconds: 0,
        });
    });

    it('listVersions GETs /v1/credentials/{name}/versions and returns versions', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            name: 'db-password',
            versions: [
                { version: 1, is_current: false, created_at_unix: 100 },
                { version: 2, is_current: true, created_at_unix: 200 },
            ],
        });

        const versions = await client.credentials.listVersions('db-password');

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/db-password/versions');
        expect(versions).toEqual([
            { version: 1, isCurrent: false, createdAtUnix: 100 },
            { version: 2, isCurrent: true, createdAtUnix: 200 },
        ]);
    });

    it('retrieveVersion GETs /v1/credentials/{name}/versions/{version}', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ value: 'b2xkLXNlY3JldA==' }); // base64('old-secret')

        const value = await client.credentials.retrieveVersion('db-password', 1);

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/db-password/versions/1');
        expect(value).toBe('old-secret');
    });

    it('gcExpired POSTs /v1/credentials/{name}/gc and returns the reaped count', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ reaped: 3 });

        const reaped = await client.credentials.gcExpired('db-password');

        expect(calls[0]!.method).toBe('POST');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/credentials/db-password/gc');
        expect(reaped).toBe(3);
    });
});

// ---------------------------------------------------------------------------
// AuditQueryClient
// ---------------------------------------------------------------------------

describe('AuditQueryClient — REST wire contract', () => {
    it('query with no filter GETs /v1/audit/events with no query string', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ events: [], total: 0 });

        const page = await client.audit.query();

        expect(calls[0]!.method).toBe('GET');
        expect(calls[0]!.url).toBe('https://localhost:63002/v1/audit/events');
        expect(page).toEqual({ events: [], total: 0 });
    });

    it('query serializes filter fields to snake_case query params', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ events: [], total: 0 });

        await client.audit.query({
            actor: 'alice',
            resource: 'barrier',
            action: 'unseal',
            outcome: 'success',
            tenantId: 'tenant-1',
            limit: 50,
            cursor: 'abc',
        });

        const url = new URL(calls[0]!.url);
        expect(url.pathname).toBe('/v1/audit/events');
        expect(url.searchParams.get('actor')).toBe('alice');
        expect(url.searchParams.get('resource')).toBe('barrier');
        expect(url.searchParams.get('action')).toBe('unseal');
        expect(url.searchParams.get('outcome')).toBe('success');
        expect(url.searchParams.get('tenant_id')).toBe('tenant-1');
        expect(url.searchParams.get('limit')).toBe('50');
        expect(url.searchParams.get('cursor')).toBe('abc');
    });

    it('query omits empty/undefined filter fields from the query string', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ events: [], total: 0 });

        await client.audit.query({ actor: 'bob' });

        const url = new URL(calls[0]!.url);
        expect(url.searchParams.get('actor')).toBe('bob');
        expect(url.searchParams.has('resource')).toBe(false);
        expect(url.searchParams.has('tenant_id')).toBe(false);
    });

    it('query returns the events array from the response', async () => {
        const client = await connectedClient();
        installMockFetch({
            events: [
                {
                    timestamp: '2026-08-01T00:00:00Z',
                    actor: 'alice',
                    resource: 'barrier',
                    action: 'unseal',
                    outcome: 'success',
                },
            ],
            total: 1,
        });

        const page = await client.audit.query({ actor: 'alice' });

        expect(page.events).toHaveLength(1);
        expect(page.events[0]!.actor).toBe('alice');
        expect(page.total).toBe(1);
    });
});
