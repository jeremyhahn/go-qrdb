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
 * Wire contract for database.barrierStatus on both transports: REST GETs
 * /v1/databases/{name}/barrier/status, the desktop calls
 * DatabaseBinding.BarrierStatus(name), and a barrier never initialized is
 * reported without `initializedAt` whichever way the server said so -- the
 * REST handler omits the time, the desktop binding carries the Go zero time.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { WailsClient } from '../src/wails.js';
import { normalizeDatabaseBarrierStatus } from '../src/transform.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function stubFetch(body: unknown, status = 200): { paths: string[] } {
    const paths: string[] = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        paths.push(`${init?.method ?? 'GET'} ${new URL(url).pathname}`);
        return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    });
    return { paths };
}

async function restClient(): Promise<RestClient> {
    const client = new RestClient('https://db.example.com:8443');
    stubFetch({ status: 'ok' });
    await client.connection.connect('https://db.example.com:8443');
    return client;
}

describe('RestClient database.barrierStatus', () => {
    beforeEach(() => vi.unstubAllGlobals());
    afterEach(() => vi.unstubAllGlobals());

    it('GETs the barrier status and maps an initialized barrier', async () => {
        const client = await restClient();
        const { paths } = stubFetch({
            sealed: false, strategy: 'shamir', hardware_backed: false, initialized_at: '2026-10-03T12:00:00Z',
        });

        const status = await client.database.barrierStatus('vault');

        expect(paths).toEqual(['GET /v1/databases/vault/barrier/status']);
        expect(status).toEqual({ sealed: false, strategy: 'shamir', hardwareBacked: false, initializedAt: '2026-10-03T12:00:00Z' });
    });

    it('reports a barrier never initialized without initializedAt', async () => {
        const client = await restClient();
        stubFetch({ sealed: true, strategy: 'shamir', hardware_backed: false });

        const status = await client.database.barrierStatus('vault');

        expect(status).not.toHaveProperty('initializedAt');
        expect(status.sealed).toBe(true);
    });

    it('path-escapes the database name', async () => {
        const client = await restClient();
        const { paths } = stubFetch({ sealed: true, strategy: '', hardware_backed: false });

        await client.database.barrierStatus('a/b');

        expect(paths).toEqual(['GET /v1/databases/a%2Fb/barrier/status']);
    });
});

describe('WailsClient database.barrierStatus', () => {
    beforeEach(() => vi.unstubAllGlobals());
    afterEach(() => vi.unstubAllGlobals());

    function installBinding(result: unknown): ReturnType<typeof vi.fn<BindingFn>> {
        const barrierStatus = vi.fn<BindingFn>(async () => result);
        vi.stubGlobal('window', { go: { main: { DatabaseBinding: { BarrierStatus: barrierStatus } } } });
        return barrierStatus;
    }

    it('calls DatabaseBinding.BarrierStatus and maps an initialized barrier', async () => {
        const spy = installBinding({ sealed: false, strategy: 'shamir', hardware_backed: false, initialized_at: '2026-10-03T12:00:00Z' });

        const status = await new WailsClient().database.barrierStatus('vault');

        expect(spy).toHaveBeenCalledWith('vault');
        expect(status).toEqual({ sealed: false, strategy: 'shamir', hardwareBacked: false, initializedAt: '2026-10-03T12:00:00Z' });
    });

    it('reports the Go zero time as never initialized', async () => {
        installBinding({ sealed: true, strategy: 'shamir', hardware_backed: false, initialized_at: '0001-01-01T00:00:00Z' });

        const status = await new WailsClient().database.barrierStatus('vault');

        expect(status).not.toHaveProperty('initializedAt');
        expect(status.sealed).toBe(true);
    });
});

describe('normalizeDatabaseBarrierStatus', () => {
    it('keeps a real time and drops an empty or zero one', () => {
        const base = { sealed: false, strategy: 'shamir', hardwareBacked: false };
        expect(normalizeDatabaseBarrierStatus({ ...base, initializedAt: '2026-10-03T12:00:00Z' }).initializedAt).toBe('2026-10-03T12:00:00Z');
        expect(normalizeDatabaseBarrierStatus({ ...base, initializedAt: '' })).not.toHaveProperty('initializedAt');
        expect(normalizeDatabaseBarrierStatus({ ...base, initializedAt: '0001-01-01T00:00:00Z' })).not.toHaveProperty('initializedAt');
        expect(normalizeDatabaseBarrierStatus(base)).toEqual(base);
    });
});

describe('WailsClient officers.unenrolDatabaseOfficer', () => {
    beforeEach(() => vi.unstubAllGlobals());
    afterEach(() => vi.unstubAllGlobals());

    it('calls OfficerBinding.UnenrolDatabaseHolder with the database and the holder', async () => {
        const unenrol = vi.fn<BindingFn>(async () => null);
        vi.stubGlobal('window', { go: { main: { OfficerBinding: { UnenrolDatabaseHolder: unenrol } } } });

        await expect(new WailsClient().officers.unenrolDatabaseOfficer({ database: 'vault', username: 'erin' })).resolves.toBeUndefined();

        expect(unenrol).toHaveBeenCalledWith('vault', 'erin');
    });

    it('reports the binding refusal', async () => {
        vi.stubGlobal('window', { go: { main: { OfficerBinding: {
            UnenrolDatabaseHolder: vi.fn<BindingFn>(async () => { throw new Error('revoke instead'); }),
        } } } });

        await expect(new WailsClient().officers.unenrolDatabaseOfficer({ database: 'vault', username: 'erin' })).rejects.toThrow('revoke instead');
    });
});
