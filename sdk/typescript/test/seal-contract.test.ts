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
 *   - initializeShamir() sends the share-holder split parameters and no
 *     secret; the response is share-free and mints no root token.
 *   - unseal() posts the credential to /v1/barrier/unseal {secret}; a Shamir
 *     barrier opens through a holder quorum, never through this client.
 *   - status() maps the server's shamir_threshold/shamir_total into the share
 *     counts and reads initialized from the server's initialized flag, the
 *     one fact a sealed node reports besides sealed.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { ConfigError, DesktopOnlyError, TransportError } from '../src/errors.js';

// ---------------------------------------------------------------------------
// Routing fetch mock
// ---------------------------------------------------------------------------

interface CapturedRequest {
    url: string;
    path: string;
    method: string;
    body: unknown;
    headers: Record<string, string>;
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
            const headers = Object.fromEntries(new Headers(init?.headers).entries());
            calls.push({ url, path, method, body, headers });

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

    it('POSTs the split parameters and returns threshold+shareCount with no root token', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/shamir/initialize': {
                // Server response is share-free: only threshold, total_shares, share_count.
                body: { threshold: 3, total_shares: 5, share_count: 5 },
            },
        });

        const result = await client.seal.initializeShamir({ threshold: 3, totalShares: 5 });

        // No secret: the unseal key is split to the enrolled holders.
        const initReq = calls.find((c) => c.path === '/v1/barrier/shamir/initialize');
        expect(initReq?.body).toEqual({ threshold: 3, total_shares: 5 });

        // No root-token call is made: a Shamir root token needs a holder quorum.
        expect(calls.some((c) => c.path.includes('root-token'))).toBe(false);

        expect(result).toEqual({ rootToken: '', threshold: 3, shareCount: 5 });
    });
});

describe('seal client – changePassphrase', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('posts both passphrases base64 with the SO PIN in the body and header', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/change-passphrase': { status: 204 },
        });

        await client.seal.changePassphrase('old-pass', 'new-pass', 'SO123');

        const change = calls.find((c) => c.path === '/v1/barrier/change-passphrase');
        expect(change?.method).toBe('POST');
        expect(change?.body).toEqual({ current: 'b2xkLXBhc3M=', next: 'bmV3LXBhc3M=', so_pin: 'SO123' });
        expect(change?.headers['x-qrdb-so-pin']).toBe('SO123');
        expect(change?.headers['content-type']).toBe('application/json');
    });

    it('sends no SO PIN header or body field when none is given', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/change-passphrase': { status: 204 },
        });

        await client.seal.changePassphrase('old-pass', 'new-pass');

        const change = calls.find((c) => c.path === '/v1/barrier/change-passphrase');
        expect(change?.headers['x-qrdb-so-pin']).toBeUndefined();
        expect(change?.body).toEqual({ current: 'b2xkLXBhc3M=', next: 'bmV3LXBhc3M=' });
    });

    // Models the server with an SO PIN provisioned: the authorization
    // middleware checks X-QRDB-SO-PIN, then handleBarrierChangePassphrase
    // checks the body's so_pin through pin.RequireSOPIN; either one missing
    // or wrong is a 401.
    function installProvisionedPINServer(pin: string): void {
        vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            if (new URL(url).pathname !== '/v1/barrier/change-passphrase') {
                return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
            }
            const header = new Headers(init?.headers).get('X-QRDB-SO-PIN');
            const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as { so_pin?: string }) : {};
            if (header !== pin || body.so_pin !== pin) {
                return new Response(JSON.stringify({ error: 'pin: SO PIN required' }), {
                    status: 401,
                    headers: { 'Content-Type': 'application/json' },
                });
            }
            return new Response(null, { status: 204 });
        });
    }

    it('passes both SO PIN gates of a provisioned server with the correct PIN', async () => {
        const client = await connectedClient();
        installProvisionedPINServer('SO123');

        await expect(client.seal.changePassphrase('old-pass', 'new-pass', 'SO123')).resolves.toBeUndefined();
    });

    it('is refused by a provisioned server with a wrong or missing PIN', async () => {
        const client = await connectedClient();
        installProvisionedPINServer('SO123');

        await expect(client.seal.changePassphrase('old-pass', 'new-pass', 'SO999')).rejects.toThrow();
        await expect(client.seal.changePassphrase('old-pass', 'new-pass')).rejects.toThrow();
    });

    it('rejects when the server refuses the current passphrase', async () => {
        const client = await connectedClient();
        installRouter({
            'POST /v1/barrier/change-passphrase': {
                status: 403,
                body: { error: 'seal: credential mismatch' },
            },
        });

        await expect(client.seal.changePassphrase('wrong', 'new-pass', 'SO123')).rejects.toThrow();
    });

    it('refuses an http:// base URL before any request, with or without an SO PIN', async () => {
        installRouter({ 'GET /health': { body: { status: 'ok' } } });
        const client = new RestClient('http://localhost:63002');
        await client.connection.connect('http://localhost:63002');
        const { calls } = installRouter({ 'POST /v1/barrier/change-passphrase': { status: 204 } });

        await expect(client.seal.changePassphrase('old-pass', 'new-pass', 'SO123'))
            .rejects.toBeInstanceOf(ConfigError);
        await expect(client.seal.changePassphrase('old-pass', 'new-pass'))
            .rejects.toBeInstanceOf(ConfigError);

        // Nothing was transmitted: no passphrase and no PIN left the process.
        expect(calls).toEqual([]);
    });

    it('fails on a redirect instead of resending the secrets to its target', async () => {
        const client = await connectedClient();
        // Models fetch's redirect modes: 'error' rejects on a 3xx, as the
        // Fetch standard requires, and 'follow' (the default) resends the
        // 308's body to the Location origin.
        const sent: { url: string; redirect: RequestRedirect | undefined; body: unknown }[] = [];
        vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            sent.push({ url, redirect: init?.redirect, body: init?.body });
            if (init?.redirect === 'error') {
                throw new TypeError('fetch failed: unexpected redirect');
            }
            sent.push({ url: 'https://attacker.example.com/v1/barrier/change-passphrase', redirect: init?.redirect, body: init?.body });
            return new Response(null, { status: 204 });
        });

        await expect(client.seal.changePassphrase('old-pass', 'new-pass', 'SO123'))
            .rejects.toBeInstanceOf(TransportError);

        // One request, to the configured origin, sent with redirect 'error'.
        expect(sent.map((s) => [s.url, s.redirect])).toEqual([
            [`${BASE}/v1/barrier/change-passphrase`, 'error'],
        ]);
    });
});

describe('seal client – unseal', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('posts the secret to /v1/barrier/unseal', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/unseal': { status: 204 },
        });

        const progress = await client.seal.unseal('my-passphrase');

        const unseal = calls.find((c) => c.path === '/v1/barrier/unseal');
        expect(unseal?.method).toBe('POST');
        // base64 on the wire; see the initialize contract test.
        expect(unseal?.body).toEqual({ secret: 'bXktcGFzc3BocmFzZQ==' });
        expect(progress.sealed).toBe(false);
    });

    it('never submits a share: a Shamir barrier refuses the request itself', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/barrier/unseal': {
                status: 412,
                body: { error: 'qrdb/seal: a Shamir barrier unseals through a holder quorum' },
            },
        });

        await expect(client.seal.unseal('not-a-share')).rejects.toThrow();

        // One request, to the credential route; no share route is named.
        expect(calls.map((c) => c.path)).toEqual(['/v1/barrier/unseal']);
    });
});

describe('seal client – status mapping', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('maps shamir_threshold/shamir_total into share counts and reads initialized', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/status': {
                body: {
                    sealed: false,
                    strategy: 'shamir',
                    hardware_backed: false,
                    initialized: true,
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

    it('reports uninitialized for a pre-init sealed node', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/status': { body: { sealed: true, initialized: false, strategy: '', hardware_backed: false } },
        });

        const status = await client.seal.status();
        expect(status.initialized).toBe(false);
        expect(status.strategy).toBe('');
        expect(status.sealed).toBe(true);
    });

    it('reports initialized for a sealed node whose answer carries no initialized_at', async () => {
        const client = await connectedClient();
        installRouter({
            'GET /v1/barrier/status': { body: { sealed: true, initialized: true, strategy: '', hardware_backed: false } },
        });

        const status = await client.seal.status();
        expect(status.initialized).toBe(true);
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

    it('sends the SO PIN in so_pin', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/seal': { status: 204 } });

        await client.seal.seal('SO123');

        const seal = calls.find((c) => c.path === '/v1/barrier/seal');
        expect(seal?.method).toBe('POST');
        expect(seal?.body).toEqual({ so_pin: 'SO123' });
    });

    it('sends no body without an SO PIN', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({ 'POST /v1/barrier/seal': { status: 204 } });

        await client.seal.seal();

        const seal = calls.find((c) => c.path === '/v1/barrier/seal');
        expect(seal?.body).toBeUndefined();
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

    it('base64-encodes the tenant barrier unseal key', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/acme/barrier/unseal': { body: {} },
        });

        await client.tenants.barrierUnseal({ tenantId: 'acme', key: 'hunter2' });

        const req = calls.find((c) => c.path === '/v1/tenants/acme/barrier/unseal');
        expect(req?.body).toEqual({ key: 'aHVudGVyMg==' });
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
            'POST /v1/tenants/acme/barrier/init/begin': { body: {} },
            'POST /v1/tenants/acme/barrier/init/complete': { status: 204 },
        });

        await client.tenants.barrierInit({
            tenantId: 'acme',
            passphrase: 'tenant-pw',
            threshold: 0,
            shares: 0,
            strategy: 'passphrase',
        });

        const req = calls.find((c) => c.path === '/v1/tenants/acme/barrier/init/complete');
        expect((req?.body as Record<string, unknown>)?.passphrase).toBe('dGVuYW50LXB3');
    });

    it('refuses tenant barrier-init when the node asks a proof a browser cannot give', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/tenants/acme/barrier/init/begin': { body: { nonce: 'AAAA', expires: 1 } },
        });

        await expect(client.tenants.barrierInit({ tenantId: 'acme', threshold: 0, shares: 0, strategy: 'none' }))
            .rejects.toBeInstanceOf(DesktopOnlyError);
        expect(calls.some((c) => c.path.endsWith('/init/complete'))).toBe(false);
    });
});
