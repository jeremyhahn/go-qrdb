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
 * Officer client REST contract tests.
 *
 * Guards the new security-officer enrollment and share-claim REST surface
 * introduced in the SO share-distribution redesign (§7.3 of the design doc).
 *
 * For every new route the test verifies:
 *   - The correct HTTP verb and path are used.
 *   - The request body carries the expected JSON fields.
 *   - The response is mapped to the correct TypeScript shape.
 *   - No share bytes appear in registration or init responses.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

// ---------------------------------------------------------------------------
// Fetch mock helpers (same pattern as existing contract tests)
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

const OFFICER_STATUS = {
    username: 'alice',
    cert_thumbprint: 'abc123',
    share_index: 1,
    auth_factor: 'software',
    enrolled_at: '2026-06-01T00:00:00Z',
    claimed_at: '',
    share_pending: true,
};

// ---------------------------------------------------------------------------
// System barrier officer routes
// ---------------------------------------------------------------------------

describe('officers – registerOfficer', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs CSR to /v1/barrier/officers and returns cert+shareIndex', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/officers': {
                body: { cert_pem: '-----BEGIN CERT-----\n...', share_index: 1 },
            },
        });

        const result = await client.officers.registerOfficer({
            username: 'alice',
            csrPem: '-----BEGIN CERTIFICATE REQUEST-----\n...',
            authenticatorType: 'software',
        });

        const req = calls.find((c) => c.path === '/v1/barrier/officers');
        expect(req?.method).toBe('POST');
        // Body must contain csr_pem (snake_case via deepCamelToSnake).
        expect((req?.body as Record<string, unknown>)['csr_pem']).toBeDefined();

        expect(result.certPem).toContain('BEGIN CERT');
        expect(result.shareIndex).toBe(1);
        // Confirm no share bytes in the response.
        expect((result as Record<string, unknown>)['shareBytes']).toBeUndefined();
        expect((result as Record<string, unknown>)['share']).toBeUndefined();
    });
});

describe('officers – listOfficers', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('GETs /v1/barrier/officers and returns officer array', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/officers': { body: { officers: [OFFICER_STATUS] } },
        });

        const officers = await client.officers.listOfficers();

        expect(officers).toHaveLength(1);
        expect(officers[0].username).toBe('alice');
        expect(officers[0].sharePending).toBe(true);
    });

    it('returns empty array when officers key is missing', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/barrier/officers': { body: {} } });

        const officers = await client.officers.listOfficers();
        expect(officers).toEqual([]);
    });
});

describe('officers – officerClaimStatus', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('GETs /v1/barrier/officers/claim-status', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /v1/barrier/officers/claim-status': {
                body: {
                    officers: [{ ...OFFICER_STATUS, claimed_at: '2026-06-02T00:00:00Z', share_pending: false }],
                },
            },
        });

        const status = await client.officers.officerClaimStatus();

        const req = calls.find((c) => c.path === '/v1/barrier/officers/claim-status');
        expect(req?.method).toBe('GET');
        expect(status[0].claimedAt).toBe('2026-06-02T00:00:00Z');
        expect(status[0].sharePending).toBe(false);
    });
});

describe('officers – claimBegin', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs to /v1/barrier/officers/{username}/claim/begin and returns nonce', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/officers/alice/claim/begin': {
                body: { nonce_hex: 'deadbeef01234567', expires_at: '2026-06-01T00:05:00Z' },
            },
        });

        const resp = await client.officers.claimBegin({ username: 'alice' });

        const req = calls.find((c) => c.path === '/v1/barrier/officers/alice/claim/begin');
        expect(req?.method).toBe('POST');
        expect(resp.nonceHex).toBe('deadbeef01234567');
        expect(resp.expiresAt).toBeDefined();
    });
});

describe('officers – claimComplete', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs signature to /v1/barrier/officers/{username}/claim/complete and returns share', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/officers/alice/claim/complete': {
                body: { share_index: 1, share_bytes: 'c2hhcmU=' },
            },
        });

        const resp = await client.officers.claimComplete({
            username: 'alice',
            nonceHex: 'deadbeef',
            signature: 'c2ln',
        });

        const req = calls.find((c) => c.path === '/v1/barrier/officers/alice/claim/complete');
        expect(req?.method).toBe('POST');
        // Wire body must use snake_case for nonce_hex and signature.
        expect((req?.body as Record<string, unknown>)['nonce_hex']).toBe('deadbeef');
        expect((req?.body as Record<string, unknown>)['signature']).toBe('c2ln');

        expect(resp.shareIndex).toBe(1);
        // share_bytes is the ONLY place share material appears.
        expect(resp.shareBytes).toBe('c2hhcmU=');
    });

    it('completeClaimResponse contains shareBytes — never absent', async () => {
        const client = await connectedClient();
        installRouter({
            'POST /v1/barrier/officers/bob/claim/complete': {
                body: { share_index: 2, share_bytes: 'Ynl0ZXM=' },
            },
        });

        const resp = await client.officers.claimComplete({
            username: 'bob',
            nonceHex: 'ff',
            signature: 'sig',
        });
        expect(typeof resp.shareBytes).toBe('string');
        expect(resp.shareBytes.length).toBeGreaterThan(0);
    });
});

describe('officers – recovery routes', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('registerRecoveryOfficer POSTs to /v1/barrier/recovery/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/recovery/officers': { body: { cert_pem: 'pem', share_index: 1 } },
        });

        await client.officers.registerRecoveryOfficer({ username: 'carol', csrPem: 'csr' });

        const req = calls.find((c) => c.path === '/v1/barrier/recovery/officers');
        expect(req?.method).toBe('POST');
    });

    it('recoveryClaimBegin POSTs to /v1/barrier/recovery/officers/{username}/claim/begin', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/recovery/officers/carol/claim/begin': {
                body: { nonce_hex: 'abc', expires_at: '2026-01-01T00:05:00Z' },
            },
        });

        const resp = await client.officers.recoveryClaimBegin({ username: 'carol' });

        expect(calls.some((c) => c.path === '/v1/barrier/recovery/officers/carol/claim/begin')).toBe(true);
        expect(resp.nonceHex).toBe('abc');
    });

    it('recoveryClaimComplete POSTs to /v1/barrier/recovery/officers/{username}/claim/complete', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/recovery/officers/carol/claim/complete': {
                body: { share_index: 1, share_bytes: 'cmVj' },
            },
        });

        const resp = await client.officers.recoveryClaimComplete({
            username: 'carol',
            nonceHex: 'abc',
            signature: 'sig',
        });

        expect(calls.some((c) => c.path === '/v1/barrier/recovery/officers/carol/claim/complete')).toBe(true);
        expect(resp.shareBytes).toBe('cmVj');
    });
});

describe('officers – submitSealVote', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs share to /v1/barrier/shamir/seal-vote and returns quorum progress', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/seal-vote': {
                body: { required: 3, submitted: 2, complete: false },
            },
        });

        const resp = await client.officers.submitSealVote({ share: 'my-share-value' });

        const req = calls.find((c) => c.path === '/v1/barrier/shamir/seal-vote');
        expect(req?.method).toBe('POST');
        expect((req?.body as Record<string, unknown>)['share']).toBe('my-share-value');
        expect(resp.required).toBe(3);
        expect(resp.submitted).toBe(2);
        expect(resp.complete).toBe(false);
    });

    it('reports complete=true when quorum is met', async () => {
        const client = await connectedClient();
        installRouter({
            'POST /v1/barrier/shamir/seal-vote': {
                body: { required: 2, submitted: 2, complete: true },
            },
        });

        const resp = await client.officers.submitSealVote({ share: 'share-3' });
        expect(resp.complete).toBe(true);
    });
});

// ---------------------------------------------------------------------------
// Tenant barrier officer routes
// ---------------------------------------------------------------------------

describe('officers – tenant officer routes', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('registerTenantOfficer POSTs to /v1/tenants/{id}/barrier/officers', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/tenant-1/barrier/officers': {
                body: { cert_pem: 'pem', share_index: 1 },
            },
        });

        const resp = await client.officers.registerTenantOfficer({
            tenantId: 'tenant-1',
            username: 'dave',
            csrPem: 'csr-pem',
        });

        const req = calls.find((c) => c.path === '/v1/tenants/tenant-1/barrier/officers');
        expect(req?.method).toBe('POST');
        expect(resp.shareIndex).toBe(1);
    });

    it('listTenantOfficers GETs /v1/tenants/{id}/barrier/officers', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/tenants/tenant-1/barrier/officers': {
                body: { officers: [{ ...OFFICER_STATUS, username: 'dave' }] },
            },
        });

        const officers = await client.officers.listTenantOfficers('tenant-1');
        expect(officers[0].username).toBe('dave');
    });

    it('tenantOfficerClaimStatus GETs /v1/tenants/{id}/barrier/officers/claim-status', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /v1/tenants/tenant-1/barrier/officers/claim-status': {
                body: { officers: [] },
            },
        });

        await client.officers.tenantOfficerClaimStatus('tenant-1');
        expect(calls.some((c) => c.path === '/v1/tenants/tenant-1/barrier/officers/claim-status')).toBe(true);
    });

    it('tenantClaimBegin POSTs to /v1/tenants/{id}/barrier/officers/{username}/claim/begin', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/tenant-1/barrier/officers/dave/claim/begin': {
                body: { nonce_hex: 'nonce01', expires_at: '2026-01-01T00:05:00Z' },
            },
        });

        const resp = await client.officers.tenantClaimBegin({ tenantId: 'tenant-1', username: 'dave' });

        expect(calls.some((c) => c.path === '/v1/tenants/tenant-1/barrier/officers/dave/claim/begin')).toBe(true);
        expect(resp.nonceHex).toBe('nonce01');
    });

    it('tenantClaimComplete POSTs to /v1/tenants/{id}/barrier/officers/{username}/claim/complete', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/tenant-1/barrier/officers/dave/claim/complete': {
                body: { share_index: 1, share_bytes: 'dGVuYW50' },
            },
        });

        const resp = await client.officers.tenantClaimComplete({
            tenantId: 'tenant-1',
            username: 'dave',
            nonceHex: 'nonce01',
            signature: 'sig',
        });

        expect(calls.some((c) => c.path === '/v1/tenants/tenant-1/barrier/officers/dave/claim/complete')).toBe(true);
        expect(resp.shareBytes).toBe('dGVuYW50');
    });

    it('submitTenantSealVote POSTs to /v1/tenants/{id}/barrier/seal-vote', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/tenant-1/barrier/seal-vote': {
                body: { required: 2, submitted: 1, complete: false },
            },
        });

        const resp = await client.officers.submitTenantSealVote({ tenantId: 'tenant-1', share: 'sv' });

        const req = calls.find((c) => c.path === '/v1/tenants/tenant-1/barrier/seal-vote');
        expect(req?.method).toBe('POST');
        expect((req?.body as Record<string, unknown>)['share']).toBe('sv');
        expect(resp.submitted).toBe(1);
    });
});

// ---------------------------------------------------------------------------
// Invariant: init response never contains share bytes
// ---------------------------------------------------------------------------

describe('initializeShamir – share bytes never present in response', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('BootstrapResult has no keyShares or shares field at runtime', async () => {
        installRouter({ 'GET /health': { body: { status: 'ok' } } });
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        installRouter({
            'POST /v1/barrier/shamir/initialize': {
                // Even if server accidentally returned shares, they must not appear.
                body: { threshold: 2, total_shares: 3, share_count: 3, shares: ['SHOULD_NOT_APPEAR'] },
            },
            'POST /v1/barrier/root-token': { body: { token: 'tok', created_at: '2026-01-01T00:00:00Z' } },
        });

        const result = await client.seal.initializeShamir('', 2, 3);

        // The BootstrapResult type has no keyShares field; verify at runtime too.
        expect((result as Record<string, unknown>)['keyShares']).toBeUndefined();
        expect((result as Record<string, unknown>)['shares']).toBeUndefined();
        expect(result.threshold).toBe(2);
        expect(result.shareCount).toBe(3);
    });

    it('TenantBarrierInitResponse has no shares field', async () => {
        installRouter({ 'GET /health': { body: { status: 'ok' } } });
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        installRouter({
            'POST /v1/tenants/t1/barrier/init': {
                body: {
                    tenant_id: 't1',
                    strategy: 'shamir',
                    threshold: 2,
                    total_shares: 3,
                    share_count: 3,
                },
            },
        });

        const result = await client.tenants.barrierInit({
            tenantId: 't1',
            threshold: 2,
            shares: 3,
            strategy: 'shamir',
        });

        expect((result as Record<string, unknown>)['shares']).toBeUndefined();
        expect(result.shareCount).toBe(3);
    });
});
