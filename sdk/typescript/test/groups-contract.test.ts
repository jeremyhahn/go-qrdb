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
 * IAM groups REST contract tests.
 *
 * For every route the tests pin the HTTP verb, the path and query, the exact
 * JSON body (the REST groups handlers use Go-style ID keys: tenantID,
 * roleIDs, groupID, and userID as the uint64 in a decimal string), and the mapping of
 * the response onto the SDK's camelCase types.
 */

/// <reference types="vite/client" />
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

// ---------------------------------------------------------------------------
// Fetch mock helpers (same pattern as the other contract tests)
// ---------------------------------------------------------------------------

interface CapturedRequest {
    url: string;
    path: string;
    method: string;
    body: unknown;
}

interface RouteResponse {
    status?: number;
    body?: unknown;
}

function installRouter(routes: Record<string, RouteResponse>): {
    calls: CapturedRequest[];
} {
    const calls: CapturedRequest[] = [];

    vi.stubGlobal(
        'fetch',
        async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            const method = init?.method ?? 'GET';
            const path = new URL(url).pathname;
            let body: unknown;
            if (init?.body && typeof init.body === 'string') {
                try { body = JSON.parse(init.body); } catch { body = init.body; }
            }
            calls.push({ url, path, method, body });

            const key = `${method} ${path}`;
            const route = routes[key];
            const status = route?.status ?? 200;
            const respBody = route?.body ?? {};
            return new Response(status === 204 ? null : JSON.stringify(respBody), {
                status,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );

    return { calls };
}

async function connectedClient(): Promise<RestClient> {
    installRouter({ 'GET /health': { body: { status: 'ok' } } });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

function query(call: CapturedRequest): Record<string, string> {
    return Object.fromEntries(new URL(call.url).searchParams.entries());
}

const GROUP_WIRE = {
    id: 'g-1',
    tenantID: 'acme',
    name: 'keepers',
    description: 'vault keepers',
    roleIDs: ['r-reader', 'r-writer'],
    createdAt: '2026-06-01T00:00:00Z',
    updatedAt: '2026-06-02T00:00:00Z',
};

const GROUP = {
    id: 'g-1',
    tenantId: 'acme',
    name: 'keepers',
    description: 'vault keepers',
    roleIds: ['r-reader', 'r-writer'],
    createdAt: '2026-06-01T00:00:00Z',
    updatedAt: '2026-06-02T00:00:00Z',
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('groups REST contract', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('create posts the Go-style body and maps the group', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /groups': { status: 201, body: GROUP_WIRE } });
        const got = await client.groups.create({
            name: 'keepers',
            tenantId: 'acme',
            description: 'vault keepers',
            roleIds: ['r-reader', 'r-writer'],
        });
        expect(calls).toHaveLength(1);
        expect(calls[0].method).toBe('POST');
        expect(calls[0].path).toBe('/groups');
        expect(calls[0].body).toEqual({
            name: 'keepers',
            tenantID: 'acme',
            description: 'vault keepers',
            roleIDs: ['r-reader', 'r-writer'],
        });
        expect(got).toEqual(GROUP);
    });

    it('create omits unset optional fields', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /groups': { status: 201, body: { ...GROUP_WIRE, roleIDs: null } } });
        const got = await client.groups.create({ name: 'keepers' });
        expect(calls[0].body).toEqual({ name: 'keepers' });
        expect(got.roleIds).toEqual([]);
    });

    it('get reads /groups/{id}', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'GET /groups/g%201': { body: GROUP_WIRE } });
        const got = await client.groups.get('g 1');
        expect(calls[0].method).toBe('GET');
        expect(calls[0].path).toBe('/groups/g%201');
        expect(got).toEqual(GROUP);
    });

    it('getByName queries tenantID and name', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'GET /groups/by-name': { body: GROUP_WIRE } });
        const got = await client.groups.getByName('keepers', 'acme');
        expect(calls[0].path).toBe('/groups/by-name');
        expect(query(calls[0])).toEqual({ tenantID: 'acme', name: 'keepers' });
        expect(got).toEqual(GROUP);
    });

    it('update puts only the given fields; an empty role list is sent', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'PUT /groups/g-1': { body: { ...GROUP_WIRE, roleIDs: [] } } });
        const got = await client.groups.update({ id: 'g-1', description: '', roleIds: [] });
        expect(calls[0].method).toBe('PUT');
        expect(calls[0].path).toBe('/groups/g-1');
        expect(calls[0].body).toEqual({ description: '', roleIDs: [] });
        expect(got.roleIds).toEqual([]);
    });

    it('delete sends DELETE /groups/{id}', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'DELETE /groups/g-1': { status: 204 } });
        await client.groups.delete('g-1');
        expect(calls[0].method).toBe('DELETE');
        expect(calls[0].path).toBe('/groups/g-1');
        expect(calls[0].body).toBeUndefined();
    });

    it('delete surfaces the Security Officer refusal', async () => {
        const client = await connectedClient();
        installRouter({ 'DELETE /groups/g-1': { status: 403, body: { error: 'security officer role required' } } });
        await expect(client.groups.delete('g-1')).rejects.toThrow();
    });

    it('list queries tenantID/page/pageSize and maps the page', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /groups/list': { body: { groups: [GROUP_WIRE], total: 3, hasMore: true } },
        });
        const got = await client.groups.list({ tenantId: 'acme', page: 2, pageSize: 1 });
        expect(calls[0].path).toBe('/groups/list');
        expect(query(calls[0])).toEqual({ tenantID: 'acme', page: '2', pageSize: '1' });
        expect(got).toEqual({ groups: [GROUP], total: 3, hasMore: true });
    });

    it('list without arguments sends no query and tolerates a null page', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'GET /groups/list': { body: { groups: null, total: 0, hasMore: false } } });
        const got = await client.groups.list();
        expect(query(calls[0])).toEqual({});
        expect(got).toEqual({ groups: [], total: 0, hasMore: false });
    });

    // 18446744073709551557 (the largest uint64 prime) is far above 2^53-1:
    // Number() would round it to another user, so it must stay a string.
    const bigUserId = '18446744073709551557';

    it('addMember posts userID as a decimal string and groupID', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /groups/members/add': { status: 204 } });
        await client.groups.addMember('g-1', bigUserId);
        expect(calls[0].method).toBe('POST');
        expect(calls[0].path).toBe('/groups/members/add');
        expect(calls[0].body).toEqual({ userID: bigUserId, groupID: 'g-1' });
    });

    it('removeMember posts userID as a decimal string and groupID', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /groups/members/remove': { status: 204 } });
        await client.groups.removeMember('g-1', bigUserId);
        expect(calls[0].path).toBe('/groups/members/remove');
        expect(calls[0].body).toEqual({ userID: bigUserId, groupID: 'g-1' });
    });

    it('members queries groupID and keeps the decimal-string userID exact', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /groups/members': {
                body: { members: [{ userID: bigUserId, username: 'alice', tenantID: 'acme' }] },
            },
        });
        const got = await client.groups.members('g-1');
        expect(calls[0].path).toBe('/groups/members');
        expect(query(calls[0])).toEqual({ groupID: 'g-1' });
        expect(got).toEqual([{ userId: bigUserId, username: 'alice', tenantId: 'acme' }]);
    });
});
