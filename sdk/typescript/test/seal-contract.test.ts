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
 * Seal client REST contract tests.
 *
 * These guard the web-parity fixes to the REST SealClient:
 *   - connect() tolerates a sealed server (503 on /health) as connected.
 *   - initialize() targets the passphrase endpoint (not Shamir).
 *   - initializeShamir() splits shares then mints a root token, and ALWAYS
 *     returns the shares even if the root-token step is unavailable.
 *   - unseal() routes by strategy: passphrase -> /v1/barrier/unseal {secret},
 *     shamir -> /v1/barrier/shamir/unseal-share {share}.
 *   - status() maps the server's shamir_threshold/shamir_total into the share
 *     counts and derives initialized from initialized_at.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

// ---------------------------------------------------------------------------
// Routing fetch mock
// ---------------------------------------------------------------------------

interface CapturedRequest {
    url: string;
    path: string;
    method: string;
    body: unknown;
}

/** A per-route response: either a JSON body (status 200) or an explicit status. */
interface RouteResponse {
    status?: number;
    body?: unknown;
    /** When set, the route returns this status/body the first N calls, then `body`. */
    failFirst?: { times: number; status: number; body?: unknown };
}

/**
 * Installs a global mock fetch that dispatches on `${METHOD} ${path}` and
 * records every call. Unmatched routes default to 200 {}.
 */
function installRouter(routes: Record<string, RouteResponse>): {
    calls: CapturedRequest[];
    counts: Record<string, number>;
} {
    const calls: CapturedRequest[] = [];
    const counts: Record<string, number> = {};

    vi.stubGlobal(
        'fetch',
        async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            const method = init?.method ?? 'GET';
            const path = new URL(url).pathname;
            let body: unknown;
            if (init?.body && typeof init.body === 'string') {
                try {
                    body = JSON.parse(init.body);
                } catch {
                    body = init.body;
                }
            }
            calls.push({ url, path, method, body });

            const key = `${method} ${path}`;
            counts[key] = (counts[key] ?? 0) + 1;
            const route = routes[key];

            if (route?.failFirst && counts[key] <= route.failFirst.times) {
                return new Response(JSON.stringify(route.failFirst.body ?? { error: 'transient' }), {
                    status: route.failFirst.status,
                    headers: { 'Content-Type': 'application/json' },
                });
            }

            const status = route?.status ?? 200;
            const respBody = route?.body ?? {};
            return new Response(status === 204 ? null : JSON.stringify(respBody), {
                status,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );

    return { calls, counts };
}

/** Builds a connected RestClient (health 200) ready to exercise seal methods. */
async function connectedClient(): Promise<RestClient> {
    installRouter({ 'GET /health': { body: { status: 'ok' } } });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

const BASE = 'https://localhost:63002';

describe('seal client – connect tolerates a sealed server', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('treats a 503 SEALED /health as connected-but-sealed', async () => {
        installRouter({
            'GET /health': { status: 503, body: { error: 'barrier sealed', code: 'SEALED' } },
            'GET /status': { status: 404 },
        });
        const client = new RestClient(BASE);
        await client.connection.connect(BASE);
        expect(client.connection.isConnected()).toBe(true);
    });

    it('rejects a genuinely unreachable server (non-503 error)', async () => {
        installRouter({ 'GET /health': { status: 500, body: { error: 'boom' } } });
        const client = new RestClient(BASE);
        await expect(client.connection.connect(BASE)).rejects.toThrow();
        expect(client.connection.isConnected()).toBe(false);
    });
});

describe('seal client – initialize (passphrase / none)', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs the secret to the passphrase endpoint', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/initialize': { status: 204 } });

        await client.seal.initialize('hunter2');

        const init = calls.find((c) => c.path === '/v1/barrier/initialize');
        expect(init).toBeDefined();
        expect(init?.method).toBe('POST');
        // The secret is base64 on the wire: the server field is `[]byte` so
        // the daemon can zeroize it, and `[]byte` is base64 in REST JSON.
        expect(init?.body).toEqual({ secret: 'aHVudGVyMg==' });
    });
});

describe('seal client – initializeShamir', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs share-free init and returns threshold+shareCount with no root token', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/initialize': {
                // Server response is share-free: only threshold, total_shares, share_count.
                body: { threshold: 3, total_shares: 5, share_count: 5 },
            },
        });

        const result = await client.seal.initializeShamir('', 3, 5);

        // Request carries snake_case total_shares.
        const initReq = calls.find((c) => c.path === '/v1/barrier/shamir/initialize');
        expect(initReq?.body).toEqual({ secret: '', threshold: 3, total_shares: 5 });

        // No root-token call is made: root-token generation requires a share
        // quorum, and shares are only obtainable by SOs via the claim flow.
        const rtReq = calls.find((c) => c.path === '/v1/barrier/root-token');
        expect(rtReq).toBeUndefined();

        // Result carries threshold and shareCount — never keyShares or a token.
        expect(result.rootToken).toBe('');
        expect(result.threshold).toBe(3);
        expect(result.shareCount).toBe(5);
        // Confirm keyShares is not present on the result type at runtime.
        expect((result as unknown as Record<string, unknown>)['keyShares']).toBeUndefined();
    });

    it('returns shareCount from total_shares fallback when share_count absent', async () => {
        const client = await connectedClient();
        installRouter({
            'POST /v1/barrier/shamir/initialize': {
                body: { threshold: 2, total_shares: 3 },
            },
        });

        const result = await client.seal.initializeShamir('', 2, 3);
        expect(result.threshold).toBe(2);
        expect(result.shareCount).toBe(3);
    });
});

describe('seal client – unseal routes by strategy', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('passphrase barrier: posts the secret to /v1/barrier/unseal', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /v1/barrier/status': { body: { sealed: true, strategy: 'passphrase', hardware_backed: false, initialized_at: '2026-01-01T00:00:00Z' } },
            'POST /v1/barrier/unseal': { status: 204 },
        });

        const progress = await client.seal.unseal('my-passphrase');

        expect(calls.some((c) => c.path === '/v1/barrier/shamir/unseal-share')).toBe(false);
        const unseal = calls.find((c) => c.path === '/v1/barrier/unseal');
        expect(unseal?.method).toBe('POST');
        // base64 on the wire; see the initialize contract test.
        expect(unseal?.body).toEqual({ secret: 'bXktcGFzc3BocmFzZQ==' });
        expect(progress.sealed).toBe(false);
    });

    it('shamir barrier: posts the share to /v1/barrier/shamir/unseal-share and reports progress', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'GET /v1/barrier/status': { body: { sealed: true, strategy: 'shamir', hardware_backed: false, initialized_at: '2026-01-01T00:00:00Z' } },
            'POST /v1/barrier/shamir/unseal-share': { body: { required: 3, submitted: 2, complete: false } },
        });

        const progress = await client.seal.unseal('share-2');

        expect(calls.some((c) => c.path === '/v1/barrier/unseal')).toBe(false);
        const share = calls.find((c) => c.path === '/v1/barrier/shamir/unseal-share');
        expect(share?.body).toEqual({ share: 'share-2' });
        expect(progress).toEqual({ sealed: true, sharesProvided: 2, sharesRequired: 3 });
    });
});

describe('seal client – status mapping', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('maps shamir_threshold/shamir_total into share counts and derives initialized', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/status': {
                body: {
                    sealed: false,
                    strategy: 'shamir',
                    hardware_backed: false,
                    initialized_at: '2026-06-06T00:00:00Z',
                    shamir_threshold: 2,
                    shamir_total: 3,
                },
            },
        });

        const status = await client.seal.status();
        expect(status.sealed).toBe(false);
        expect(status.initialized).toBe(true);
        expect(status.strategy).toBe('shamir');
        expect(status.sharesRequired).toBe(2);
        expect(status.sharesTotal).toBe(3);
    });

    it('reports uninitialized for a pre-init barrier (empty strategy, no initialized_at)', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/status': { body: { sealed: true, strategy: '', hardware_backed: false } },
        });

        const status = await client.seal.status();
        expect(status.initialized).toBe(false);
        expect(status.strategy).toBe('');
        expect(status.sealed).toBe(true);
    });

    it('treats a 404 (no barrier service) as none / unsealed', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/barrier/status': { status: 404, body: { error: 'not implemented' } } });

        const status = await client.seal.status();
        expect(status.strategy).toBe('none');
        expect(status.initialized).toBe(false);
        expect(status.sealed).toBe(false);
    });
});

describe('seal client – seal', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('POSTs to /v1/barrier/seal', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/seal': { status: 204 } });

        await client.seal.seal();

        const seal = calls.find((c) => c.path === '/v1/barrier/seal');
        expect(seal?.method).toBe('POST');
    });
});

// ---------------------------------------------------------------------------
// Secret-bearing wire fields are base64
//
// The server types every barrier credential as `[]byte` rather than `string`
// so the daemon can zeroize it — a Go string is immutable and cannot be
// wiped. `[]byte` is base64 in REST JSON, so these fields are base64 on the
// wire while the SDK-facing arguments stay plaintext. These tests fail if the
// encoding is dropped, which would silently send the wrong bytes: the server
// would base64-DECODE the plaintext and unseal against garbage.
// ---------------------------------------------------------------------------

describe('seal client – secret-bearing fields are base64 on the wire', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('base64-encodes the shamir initialize secret', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/initialize': { body: { threshold: 2, total_shares: 3, share_count: 3 } },
        });

        await client.seal.initializeShamir('hunter2', 2, 3);

        const req = calls.find((c) => c.path === '/v1/barrier/shamir/initialize');
        expect(req?.body).toEqual({ secret: 'aHVudGVyMg==', threshold: 2, total_shares: 3 });
    });

    it('base64-encodes the enable-encryption passphrase', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/enable-encryption': { body: { status: 'migration_started', scope: 'system' } },
        });

        await client.seal.enableEncryption({ strategy: 'passphrase', passphrase: 'hunter2' });

        const req = calls.find((c) => c.path === '/v1/barrier/enable-encryption');
        expect(req?.body).toEqual({ strategy: 'passphrase', passphrase: 'aHVudGVyMg==' });
    });

    it('base64-encodes the tenant barrier-init passphrase', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/acme/barrier/init': { status: 204 },
        });

        await client.tenants.barrierInit({
            tenantId: 'acme',
            passphrase: 'tenant-pw',
            threshold: 0,
            shares: 0,
            strategy: 'passphrase',
        });

        const req = calls.find((c) => c.path === '/v1/tenants/acme/barrier/init');
        expect((req?.body as Record<string, unknown>)?.passphrase).toBe('dGVuYW50LXB3');
    });
});
