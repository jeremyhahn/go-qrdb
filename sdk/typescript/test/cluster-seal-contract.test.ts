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
 * Cluster sealing contract tests (docs/barrier-seal), for both clients:
 *   - seal.status() carries the node barrier's cluster seal state;
 *   - seal.clusterUnseal() clears the node record and returns its new state,
 *     and surfaces the server's refusal while cluster sealing is not active.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { WailsClient } from '../src/wails.js';

const BASE = 'https://localhost:63002';

/** Routes `${METHOD} ${path}` to a status and JSON body; records each call. */
function installRouter(routes: Record<string, { status?: number; body?: unknown }>): { method: string; path: string }[] {
    const calls: { method: string; path: string }[] = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        const method = init?.method ?? 'GET';
        const path = new URL(url).pathname;
        calls.push({ method, path });
        const route = routes[`${method} ${path}`];
        const status = route?.status ?? 200;
        return new Response(status === 204 ? null : JSON.stringify(route?.body ?? {}), {
            status,
            headers: { 'Content-Type': 'application/json' },
        });
    });
    return calls;
}

async function connectedRest(): Promise<RestClient> {
    installRouter({ 'GET /health': { body: { status: 'ok' } } });
    const client = new RestClient(BASE);
    await client.connection.connect(BASE);
    return client;
}

const heldRecord = {
    active: true,
    sealed: true,
    epoch: 3,
    principal: 'ops',
    principal_tenant: 'system',
    node: '192.0.2.1:5000',
    at: '2026-10-02T12:00:00Z',
    local: 'held',
};

describe('rest seal client – cluster sealing', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('status() carries the node barrier cluster seal state', async () => {
        const client = await connectedRest();
        installRouter({
            'GET /v1/barrier/status': {
                body: { sealed: false, strategy: 'passphrase', hardware_backed: false, initialized_at: '2026-10-01T00:00:00Z', cluster_seal: heldRecord },
            },
        });
        const status = await client.seal.status();
        expect(status.clusterSeal).toEqual({
            active: true,
            sealed: true,
            epoch: 3,
            principal: 'ops',
            principalTenant: 'system',
            node: '192.0.2.1:5000',
            at: '2026-10-02T12:00:00Z',
            local: 'held',
        });
    });

    it('status() reports no cluster seal from a server that sends none', async () => {
        const client = await connectedRest();
        installRouter({ 'GET /v1/barrier/status': { body: { sealed: false, strategy: 'none', hardware_backed: false } } });
        const status = await client.seal.status();
        expect(status.clusterSeal).toBeUndefined();
    });

    it('clusterUnseal() POSTs /v1/barrier/cluster-unseal and returns the cleared record', async () => {
        const client = await connectedRest();
        const calls = installRouter({
            'POST /v1/barrier/cluster-unseal': {
                body: { ...heldRecord, sealed: false, epoch: 4, local: 'serving' },
            },
        });
        const got = await client.seal.clusterUnseal();
        expect(calls.find((c) => c.path === '/v1/barrier/cluster-unseal')?.method).toBe('POST');
        expect(got.sealed).toBe(false);
        expect(got.epoch).toBe(4);
        expect(got.local).toBe('serving');
        expect(got.principalTenant).toBe('system');
    });

    it('clusterUnseal() surfaces the refusal while cluster sealing is not active', async () => {
        const client = await connectedRest();
        installRouter({
            'POST /v1/barrier/cluster-unseal': {
                status: 409,
                body: { error: 'clusterseal: cluster sealing is not active in this cluster', code: 'FAILED_PRECONDITION' },
            },
        });
        await expect(client.seal.clusterUnseal()).rejects.toThrow(/not active/);
    });
});

describe('wails seal client – cluster sealing', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('clusterUnseal() calls the desktop ClusterUnseal binding and camel-cases its result', async () => {
        const ClusterUnseal = vi.fn(async () => ({ ...heldRecord, sealed: false, epoch: 4, local: 'serving' }));
        vi.stubGlobal('window', { go: { main: { SealBinding: { ClusterUnseal } } } });
        const got = await new WailsClient().seal.clusterUnseal();
        expect(ClusterUnseal).toHaveBeenCalledOnce();
        expect(got.principalTenant).toBe('system');
        expect(got.epoch).toBe(4);
    });

    it('status() passes the desktop binding cluster seal through', async () => {
        const Status = vi.fn(async () => ({ sealed: false, initialized: true, strategy: 'passphrase', clusterSeal: heldRecord }));
        vi.stubGlobal('window', { go: { main: { SealBinding: { Status } } } });
        const status = await new WailsClient().seal.status();
        expect(status.clusterSeal?.local).toBe('held');
        expect(status.clusterSeal?.principalTenant).toBe('system');
    });
});
