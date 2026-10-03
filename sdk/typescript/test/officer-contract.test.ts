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
 * Share-holder administration REST contract tests.
 *
 * The SDK administers share holders — enrolment, listing, rekey and
 * revocation — for the system barrier's unseal and recovery domains and for
 * tenant barriers. Claiming an envelope and submitting a share are holder
 * operations that need the holder's private keys; the SDK does not implement
 * them (see docs/shamir-custody).
 *
 * For every route the tests pin the HTTP verb and path, the exact JSON body,
 * and the mapping of the response, and they assert no share material appears
 * in any administration response.
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

const HOLDER_STATUS = {
    username: 'alice',
    role: 'so',
    cert_thumbprint: 'abc123',
    share_index: 1,
    designated: 'mtls',
    factors: ['mtls', 'fido2'],
    encryption_kem: 'dhkem-p256',
    enrolled_at: '2026-06-01T00:00:00Z',
    claimed_at: '0001-01-01T00:00:00Z',
    share_pending: true,
};

const OPEN_ROUND = {
    domain: 'unseal',
    op: 'rekey',
    required: 2,
    submitted: 0,
    expires_at: '2026-06-01T00:10:00Z',
    new_threshold: 3,
    policy: 'any-enrolled',
    remove: ['erin'],
};

const ENROLMENT = {
    username: 'carol',
    role: 'custodian' as const,
    csrPem: '-----BEGIN CERTIFICATE REQUEST-----\n...',
    encryptionKem: 'dhkem-x25519',
    encryptionKey: 'cHVibGljLWtleQ==',
    factors: [{ name: 'fido2', params: { credentialId: 'Y3JlZA==', rpId: 'db.example.com' } }],
    designated: 'fido2',
};

// The wire body an enrolment must produce: snake_case field names, with each
// factor's params passed through verbatim — they belong to the factor, not to
// the SDK, so their keys are never re-cased.
const ENROLMENT_WIRE = {
    username: 'carol',
    role: 'custodian',
    csr_pem: '-----BEGIN CERTIFICATE REQUEST-----\n...',
    encryption_kem: 'dhkem-x25519',
    encryption_key: 'cHVibGljLWtleQ==',
    factors: [{ name: 'fido2', params: { credentialId: 'Y3JlZA==', rpId: 'db.example.com' } }],
    designated: 'fido2',
};

const REGISTERED = {
    cert_pem: '-----BEGIN CERTIFICATE-----\n...',
    role: 'custodian',
    designated: 'fido2',
    encryption_kem: 'dhkem-x25519',
    factors: ['mtls', 'fido2'],
};

function expectNoShareMaterial(v: unknown): void {
    const json = JSON.stringify(v);
    for (const field of ['share"', 'shares"', 'shareBytes', 'envelope', 'share_bytes']) {
        expect(json).not.toContain(field);
    }
}

// ---------------------------------------------------------------------------
// System barrier
// ---------------------------------------------------------------------------

describe('holders – enrolment', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('registerOfficer POSTs the enrolment to /v1/barrier/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/officers': { body: REGISTERED } });

        const result = await client.officers.registerOfficer(ENROLMENT);

        const req = calls.find((c) => c.path === '/v1/barrier/officers');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual(ENROLMENT_WIRE);
        expect(result).toEqual({
            certPem: '-----BEGIN CERTIFICATE-----\n...',
            role: 'custodian',
            designated: 'fido2',
            encryptionKem: 'dhkem-x25519',
            factors: ['mtls', 'fido2'],
        });
        expectNoShareMaterial(result);
    });

    it('registerRecoveryOfficer POSTs to /v1/barrier/recovery/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/recovery/officers': { body: REGISTERED } });

        await client.officers.registerRecoveryOfficer({ username: 'dave', csrPem: 'CSR' });

        const req = calls.find((c) => c.path === '/v1/barrier/recovery/officers');
        expect(req?.method).toBe('POST');
        // Optional fields are omitted, not sent empty.
        expect(req?.body).toEqual({ username: 'dave', csr_pem: 'CSR' });
    });
});

describe('holders – listing', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('listOfficers GETs /v1/barrier/officers and maps each holder', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/barrier/officers': { body: { officers: [HOLDER_STATUS] } } });

        const list = await client.officers.listOfficers();

        expect(list).toHaveLength(1);
        expect(list[0]).toMatchObject({
            username: 'alice',
            role: 'so',
            certThumbprint: 'abc123',
            shareIndex: 1,
            designated: 'mtls',
            factors: ['mtls', 'fido2'],
            encryptionKem: 'dhkem-p256',
            sharePending: true,
        });
        expectNoShareMaterial(list);
    });

    it('reports a never-claimed envelope as no claim, and a real claim as its time', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/officers': {
                body: { officers: [HOLDER_STATUS, { ...HOLDER_STATUS, username: 'bob', claimed_at: '2026-06-02T00:00:00Z', share_pending: false }] },
            },
        });

        const [alice, bob] = await client.officers.listOfficers();

        expect(alice).not.toHaveProperty('claimedAt');
        expect(bob?.claimedAt).toBe('2026-06-02T00:00:00Z');
    });

    it('listOfficers returns an empty array when the officers key is missing', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/barrier/officers': { body: {} } });
        expect(await client.officers.listOfficers()).toEqual([]);
    });
});

describe('holders – rekey and revocation', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('startRekey POSTs to /v1/barrier/rekey/start and returns the open round', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/rekey/start': { body: OPEN_ROUND } });

        const round = await client.officers.startRekey({
            domain: 'unseal',
            threshold: 3,
            policy: 'any-enrolled',
            soPin: '1234',
        });

        const req = calls.find((c) => c.path === '/v1/barrier/rekey/start');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual({ domain: 'unseal', threshold: 3, policy: 'any-enrolled', so_pin: '1234' });
        expect(round).toEqual({
            domain: 'unseal',
            op: 'rekey',
            required: 2,
            submitted: 0,
            expiresAt: '2026-06-01T00:10:00Z',
            newThreshold: 3,
            policy: 'any-enrolled',
            remove: ['erin'],
        });
    });

    it('startRekey defaults the domain to unseal', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/rekey/start': { body: OPEN_ROUND } });

        await client.officers.startRekey({ threshold: 2 });

        expect(calls.find((c) => c.path === '/v1/barrier/rekey/start')?.body).toEqual({ domain: 'unseal', threshold: 2 });
    });

    it('revokeOfficer POSTs to /v1/barrier/officers/revoke and returns the open round', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/officers/revoke': { body: OPEN_ROUND } });

        const round = await client.officers.revokeOfficer({ domain: 'recovery', username: 'erin', soPin: '1234' });

        const req = calls.find((c) => c.path === '/v1/barrier/officers/revoke');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual({ domain: 'recovery', username: 'erin', so_pin: '1234' });
        expect(round.remove).toEqual(['erin']);
    });
});

// ---------------------------------------------------------------------------
// Tenant barrier
// ---------------------------------------------------------------------------

describe('holders – tenant barrier', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('registerTenantOfficer POSTs the enrolment to /v1/tenants/{id}/barrier/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/tenants/acme/barrier/officers': { body: REGISTERED } });

        await client.officers.registerTenantOfficer({ tenantId: 'acme', ...ENROLMENT });

        const req = calls.find((c) => c.path === '/v1/tenants/acme/barrier/officers');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual(ENROLMENT_WIRE);
    });

    it('listTenantOfficers GETs /v1/tenants/{id}/barrier/officers', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/tenants/acme/barrier/officers': { body: { officers: [HOLDER_STATUS] } } });

        const list = await client.officers.listTenantOfficers('acme');

        expect(list.map((h) => h.username)).toEqual(['alice']);
    });

    it('startTenantRekey POSTs to /v1/tenants/{id}/barrier/rekey/start', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/tenants/acme/barrier/rekey/start': { body: OPEN_ROUND } });

        await client.officers.startTenantRekey({ tenantId: 'acme', threshold: 3, policy: 'designated' });

        expect(calls.find((c) => c.path === '/v1/tenants/acme/barrier/rekey/start')?.body)
            .toEqual({ threshold: 3, policy: 'designated' });
    });

    it('revokeTenantOfficer POSTs to /v1/tenants/{id}/barrier/officers/revoke', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/tenants/acme/barrier/officers/revoke': { body: OPEN_ROUND } });

        await client.officers.revokeTenantOfficer({ tenantId: 'acme', username: 'erin', threshold: 2 });

        expect(calls.find((c) => c.path === '/v1/tenants/acme/barrier/officers/revoke')?.body)
            .toEqual({ username: 'erin', threshold: 2 });
    });

    it('tenant ids and usernames are path-escaped', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({});

        await client.officers.listTenantOfficers('a/b');

        expect(calls.some((c) => c.url.endsWith('/v1/tenants/a%2Fb/barrier/officers'))).toBe(true);
    });
});

// ---------------------------------------------------------------------------
// Database barrier holders (docs/databases/api.md)
// ---------------------------------------------------------------------------

describe('holders – database barrier', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('registerDatabaseOfficer POSTs the enrolment to /v1/databases/{name}/barrier/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/databases/vault/barrier/officers': { body: REGISTERED } });

        const result = await client.officers.registerDatabaseOfficer({ database: 'vault', ...ENROLMENT });

        const req = calls.find((c) => c.path === '/v1/databases/vault/barrier/officers');
        expect(req?.method).toBe('POST');
        // The database travels in the path, never in the body.
        expect(req?.body).toEqual(ENROLMENT_WIRE);
        expect(result.certPem).toBe(REGISTERED.cert_pem);
    });

    it('listDatabaseOfficers GETs /v1/databases/{name}/barrier/officers', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/databases/vault/barrier/officers': { body: { officers: [HOLDER_STATUS] } } });

        const list = await client.officers.listDatabaseOfficers('vault');

        expect(list.map((h) => h.username)).toEqual(['alice']);
        expectNoShareMaterial(list);
    });

    it('initializeDatabaseShamir POSTs only the policy and returns the share-free split', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/databases/vault/barrier/shamir/initialize': {
                body: { threshold: 2, total_shares: 3, share_count: 3 },
            },
        });

        const result = await client.officers.initializeDatabaseShamir({ database: 'vault', policy: 'designated' });

        expect(calls.find((c) => c.path === '/v1/databases/vault/barrier/shamir/initialize')?.body)
            .toEqual({ policy: 'designated' });
        expect(result).toEqual({ threshold: 2, totalShares: 3, shareCount: 3 });
        expectNoShareMaterial(result);
    });

    it('startDatabaseRekey POSTs to /v1/databases/{name}/barrier/rekey/start', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/databases/vault/barrier/rekey/start': { body: OPEN_ROUND } });

        await client.officers.startDatabaseRekey({ database: 'vault', threshold: 3, policy: 'designated' });

        expect(calls.find((c) => c.path === '/v1/databases/vault/barrier/rekey/start')?.body)
            .toEqual({ threshold: 3, policy: 'designated' });
    });

    it('revokeDatabaseOfficer POSTs to /v1/databases/{name}/barrier/officers/revoke', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/databases/vault/barrier/officers/revoke': { body: OPEN_ROUND } });

        await client.officers.revokeDatabaseOfficer({ database: 'vault', username: 'erin', threshold: 2 });

        expect(calls.find((c) => c.path === '/v1/databases/vault/barrier/officers/revoke')?.body)
            .toEqual({ username: 'erin', threshold: 2 });
    });

    it('database names are path-escaped', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({});

        await client.officers.listDatabaseOfficers('a/b');

        expect(calls.some((c) => c.url.endsWith('/v1/databases/a%2Fb/barrier/officers'))).toBe(true);
    });
});

// ---------------------------------------------------------------------------
// Shamir initialization
// ---------------------------------------------------------------------------

describe('initializeShamir – holders, not a secret', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs threshold, total shares and policy, and never carries share material', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/initialize': {
                // Even if a server returned shares, they must not surface.
                body: { threshold: 2, total_shares: 3, share_count: 3, shares: ['SHOULD_NOT_APPEAR'] },
            },
        });

        const result = await client.seal.initializeShamir({ threshold: 2, totalShares: 3, policy: 'designated' });

        const req = calls.find((c) => c.path === '/v1/barrier/shamir/initialize');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual({ threshold: 2, total_shares: 3, policy: 'designated' });
        expect(result).toEqual({ rootToken: '', threshold: 2, shareCount: 3 });
    });

    it('sends the PINs only when given', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/initialize': { body: { threshold: 2, total_shares: 2, share_count: 2 } },
        });

        await client.seal.initializeShamir({ threshold: 2, totalShares: 2, soPin: 'so', userPin: 'user' });

        expect(calls.find((c) => c.path === '/v1/barrier/shamir/initialize')?.body)
            .toEqual({ threshold: 2, total_shares: 2, so_pin: 'so', user_pin: 'user' });
    });

    it('TenantBarrierInitResponse has no shares field', async () => {
        const client = await connectedClient();
        installRouter({
            'POST /v1/tenants/t1/barrier/init': {
                body: { tenant_id: 't1', strategy: 'shamir', threshold: 2, total_shares: 3, share_count: 3 },
            },
        });

        const result = await client.tenants.barrierInit({ tenantId: 't1', threshold: 2, shares: 3, strategy: 'shamir' });

        expect((result as unknown as Record<string, unknown>)['shares']).toBeUndefined();
        expect(result.shareCount).toBe(3);
    });
});

// ---------------------------------------------------------------------------
// Retired routes
// ---------------------------------------------------------------------------

describe('retired holder routes', () => {
    // The raw-share routes and the claim-status listing are gone from the
    // server. No transport in this SDK may name them again: a call to one is a
    // 404 at runtime that no type check catches.
    const retired = [
        '/unseal-share',
        '/unseal-shares',
        '/seal-vote',
        '/claim-status',
        '/v1/barrier/rekey\'',
        '/v1/barrier/root-token',
        '/v1/barrier/recovery/recover',
    ];

    it('no SDK source file names a retired route', () => {
        // Every SDK source file, read as text by the test runner. The SDK's
        // tests are typechecked without Node's types, so the file system is
        // not read directly.
        const sources = import.meta.glob<string>('../src/*.ts', { query: '?raw', import: 'default', eager: true });
        const names = Object.keys(sources);
        // A discovery floor: an empty glob would pass every assertion below.
        for (const required of ['../src/rest.ts', '../src/types.ts', '../src/interfaces.ts']) {
            expect(names).toContain(required);
        }
        for (const name of names) {
            const src = sources[name];
            expect(src).toContain('export');
            for (const route of retired) {
                expect(src.includes(route), `${name} names the retired route ${route}`).toBe(false);
            }
        }
    });
});
