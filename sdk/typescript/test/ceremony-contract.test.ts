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
 * Initialization ceremony REST contract tests.
 *
 * `qrdb init` is the ONLY system bootstrap, so its officers carry the same
 * full holder shape OfficerRegisterRequest does (go-qrdb#4578 review item
 * 1): a separate share-envelope key, additional authentication factors, and
 * the designated factor. The tests pin the exact JSON body ceremony.
 * initialize sends, in particular that a factor's own `params` are passed
 * through verbatim -- they belong to the factor, not the SDK, so their keys
 * must never be re-cased the way the request's OTHER fields are (the same
 * property officer-contract.test.ts pins for registerOfficer).
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

// The request the SDK's public API is called with: officers[1]'s
// encryptionKey is already base64 at this boundary (the same convention
// OfficerRegisterRequest.encryptionKey uses), secret is plain text.
const INIT_REQUEST = {
    soPin: 'so-pin',
    userPin: 'user-pin',
    threshold: 3,
    policy: 'designated' as const,
    officers: [
        { username: 'alice', role: 'so' as const, csrPem: '-----BEGIN CERTIFICATE REQUEST-----\nalice\n' },
        {
            username: 'carol',
            role: 'custodian' as const,
            csrPem: '-----BEGIN CERTIFICATE REQUEST-----\ncarol\n',
            encryptionKem: 'dhkem-x25519',
            encryptionKey: 'cHVibGljLWtleQ==',
            factors: [{ name: 'fido2', params: { credentialId: 'Y3JlZA==', rpId: 'db.example.com' } }],
            designated: 'fido2',
        },
    ],
    hostname: 'db.example.com',
    sans: ['db.example.com'],
    adminGenerate: true,
    secret: 'barrier-secret',
};

// The wire body ceremony.initialize must produce: snake_case field names
// throughout, officers[1]'s factor params passed through verbatim, and
// secret base64-encoded (the server field is []byte so it can be zeroized).
const INIT_WIRE = {
    so_pin: 'so-pin',
    user_pin: 'user-pin',
    threshold: 3,
    policy: 'designated',
    officers: [
        { username: 'alice', role: 'so', csr_pem: '-----BEGIN CERTIFICATE REQUEST-----\nalice\n' },
        {
            username: 'carol',
            role: 'custodian',
            csr_pem: '-----BEGIN CERTIFICATE REQUEST-----\ncarol\n',
            encryption_kem: 'dhkem-x25519',
            encryption_key: 'cHVibGljLWtleQ==',
            factors: [{ name: 'fido2', params: { credentialId: 'Y3JlZA==', rpId: 'db.example.com' } }],
            designated: 'fido2',
        },
    ],
    hostname: 'db.example.com',
    sans: ['db.example.com'],
    admin_generate: true,
    secret: 'YmFycmllci1zZWNyZXQ=',
};

describe('ceremony – initialize', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('initialize POSTs the full-shape request to /v1/ceremony/initialize', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/ceremony/initialize': {
                body: {
                    spki_pin: 'pin123',
                    ca_cert_pem: '-----BEGIN CERTIFICATE-----\nca\n',
                    tls_cert_pem: '-----BEGIN CERTIFICATE-----\ntls\n',
                    state: 'operational',
                    share_count: 0,
                    admin_cert_pem: '-----BEGIN CERTIFICATE-----\nadmin\n',
                    admin_key_pem: 'YWRtaW4ta2V5',
                    admin_credential_source: 'generated',
                },
            },
        });

        const result = await client.ceremony.initialize(INIT_REQUEST);

        const req = calls.find((c) => c.path === '/v1/ceremony/initialize');
        expect(req?.method).toBe('POST');
        expect(req?.body).toEqual(INIT_WIRE);
        expect(result.spkiPin).toBe('pin123');
        expect(result.adminCredentialSource).toBe('generated');
        // admin_key_pem is base64 on the wire for the same reason secret is
        // on the way in; the SDK decodes it back to plain PEM text so the
        // caller never deals with base64 either way.
        expect(result.adminKeyPem).toBe('admin-key');
    });

    it('initialize omits secret when not given', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/ceremony/initialize': { body: { spki_pin: '', ca_cert_pem: '', tls_cert_pem: '', state: 'operational', share_count: 0 } },
        });

        await client.ceremony.initialize({ soPin: 'p', userPin: 'p', adminGenerate: true });

        const req = calls.find((c) => c.path === '/v1/ceremony/initialize');
        expect(req?.body).toEqual({ so_pin: 'p', user_pin: 'p', admin_generate: true });
    });

    it('status GETs /v1/ceremony/status', async () => {
        const client = await connectedClient();
        installRouter({ 'GET /v1/ceremony/status': { body: { state: 'enrolling', spki_pin: 'pin123' } } });

        const result = await client.ceremony.status();

        expect(result).toEqual({ state: 'enrolling', spkiPin: 'pin123' });
    });

    it('signCSR POSTs to /v1/ceremony/sign-csr', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            'POST /v1/ceremony/sign-csr': { body: { cert_pem: '-----BEGIN CERTIFICATE-----\nso\n' } },
        });

        const result = await client.ceremony.signCSR({
            username: 'alice',
            soPin: 'so-pin',
            csrPem: '-----BEGIN CERTIFICATE REQUEST-----\nalice\n',
            role: 'so',
        });

        const req = calls.find((c) => c.path === '/v1/ceremony/sign-csr');
        expect(req?.body).toEqual({
            username: 'alice',
            so_pin: 'so-pin',
            csr_pem: '-----BEGIN CERTIFICATE REQUEST-----\nalice\n',
            role: 'so',
        });
        expect(result.certPem).toBe('-----BEGIN CERTIFICATE-----\nso\n');
    });
});
