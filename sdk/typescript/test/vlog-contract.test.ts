// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Wire-format contract tests for the verifiable-log client: the REST client
 * against the /vlog routes of pkg/api/rest/server.go and the vlogwire JSON
 * shapes, and the Wails client against cmd/qrdb's VlogBinding.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { WailsClient } from '../src/wails.js';

interface CapturedRequest {
    url: string;
    method: string;
    body: unknown;
}

const BASE = 'https://localhost:63002';

function installMockFetch(responseBody: unknown = {}): CapturedRequest[] {
    const calls: CapturedRequest[] = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        const body = init?.body && typeof init.body === 'string' ? JSON.parse(init.body) : undefined;
        calls.push({ url, method: init?.method ?? 'GET', body });
        return new Response(JSON.stringify(responseBody), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
        });
    });
    return calls;
}

async function connectedClient(): Promise<RestClient> {
    installMockFetch({ status: 'ok' });
    const client = new RestClient(BASE);
    await client.connection.connect(BASE);
    return client;
}

describe('VlogClient — REST wire contract', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('create POSTs /vlog/log with a snake_case body and camelCases the result', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            tenant_id: 't1',
            database: 'db',
            log: 'events',
            key: { version: 1, public_key: 'cGs=' },
        });
        const res = await client.vlog.create({
            log: 'events',
            origin: 'example.com/events',
            verifierKeys: ['w+1+abc'],
            allowExpireBodies: true,
            signingKeyType: 'ed25519',
        });
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toBe(`${BASE}/vlog/log`);
        expect(calls[0].body).toEqual({
            log: 'events',
            origin: 'example.com/events',
            verifier_keys: ['w+1+abc'],
            allow_expire_bodies: true,
            signing_key_type: 'ed25519',
        });
        expect(res).toEqual({
            tenantId: 't1',
            database: 'db',
            log: 'events',
            key: { version: 1, publicKey: 'cGs=' },
        });
    });

    it('list GETs /vlog/logs', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ logs: ['a', 'b'] });
        const res = await client.vlog.list();
        expect(calls[0].method).toBe('GET');
        expect(calls[0].url).toBe(`${BASE}/vlog/logs`);
        expect(res.logs).toEqual(['a', 'b']);
    });

    it('append POSTs /vlog/append with base64 entries', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ first_index: 4, count: 2 });
        const res = await client.vlog.append({ log: 'events', entries: ['YQ==', 'Yg=='] });
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toBe(`${BASE}/vlog/append`);
        expect(calls[0].body).toEqual({ log: 'events', entries: ['YQ==', 'Yg=='] });
        expect(res).toEqual({ firstIndex: 4, count: 2 });
    });

    it('get GETs /vlog/entries with log, from and limit query params', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ entries: [{ index: 3, body: 'YQ==' }, { index: 4, expired: true }] });
        const res = await client.vlog.get({ log: 'events', from: 3, limit: 2 });
        expect(calls[0].url).toBe(`${BASE}/vlog/entries?log=events&from=3&limit=2`);
        expect(res.entries).toEqual([{ index: 3, body: 'YQ==' }, { index: 4, expired: true }]);
    });

    it('get omits absent from and limit', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ entries: [] });
        await client.vlog.get({ log: 'a b' });
        expect(calls[0].url).toBe(`${BASE}/vlog/entries?log=a%20b`);
    });

    it('proveInclusion GETs /vlog/proof/inclusion', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ index: 1, size: 4, hashes: ['aA==', 'aQ=='] });
        const res = await client.vlog.proveInclusion({ log: 'events', index: 1, size: 4 });
        expect(calls[0].url).toBe(`${BASE}/vlog/proof/inclusion?log=events&index=1&size=4`);
        expect(res).toEqual({ index: 1, size: 4, hashes: ['aA==', 'aQ=='] });
    });

    it('proveConsistency GETs /vlog/proof/consistency', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ size1: 2, size2: 5, hashes: ['aA=='] });
        const res = await client.vlog.proveConsistency({ log: 'events', size1: 2, size2: 5 });
        expect(calls[0].url).toBe(`${BASE}/vlog/proof/consistency?log=events&size1=2&size2=5`);
        expect(res).toEqual({ size1: 2, size2: 5, hashes: ['aA=='] });
    });

    it('head GETs /vlog/head and camelCases the head', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            size: 5,
            root: 'cm9vdA==',
            bodies_from: 2,
            checkpoint: 'Y2s=',
            checkpoint_size: 5,
        });
        const res = await client.vlog.head('events');
        expect(calls[0].url).toBe(`${BASE}/vlog/head?log=events`);
        expect(res).toEqual({ size: 5, root: 'cm9vdA==', bodiesFrom: 2, checkpoint: 'Y2s=', checkpointSize: 5 });
    });

    it('signHead POSTs /vlog/head/sign with the log name', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            head: { size: 5, root: 'cm9vdA==', key_version: 2, signature_type: 'ed25519', signature: 'c2ln' },
            checkpoint: 'Y2s=',
        });
        const res = await client.vlog.signHead('events');
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toBe(`${BASE}/vlog/head/sign`);
        expect(calls[0].body).toEqual({ log: 'events' });
        expect(res.head).toEqual({
            size: 5,
            root: 'cm9vdA==',
            keyVersion: 2,
            signatureType: 'ed25519',
            signature: 'c2ln',
        });
        expect(res.checkpoint).toBe('Y2s=');
    });

    it('rotateKey POSTs /vlog/key/rotate with signing_key_type', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ key: { version: 2, public_key: 'cGs=' } });
        const res = await client.vlog.rotateKey({ log: 'events', signingKeyType: 'ecdsa-p256-sha256' });
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toBe(`${BASE}/vlog/key/rotate`);
        expect(calls[0].body).toEqual({ log: 'events', signing_key_type: 'ecdsa-p256-sha256' });
        expect(res.key).toEqual({ version: 2, publicKey: 'cGs=' });
    });

    it('expireBodies POSTs /vlog/expire with the cutoff', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ expired: 3 });
        const res = await client.vlog.expireBodies({ log: 'events', cutoff: 3 });
        expect(calls[0].method).toBe('POST');
        expect(calls[0].url).toBe(`${BASE}/vlog/expire`);
        expect(calls[0].body).toEqual({ log: 'events', cutoff: 3 });
        expect(res.expired).toBe(3);
    });

    it('rejects before any request when not connected', async () => {
        const calls = installMockFetch({});
        const client = new RestClient(BASE);
        await expect(client.vlog.head('events')).rejects.toThrow();
        expect(calls).toHaveLength(0);
    });
});

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installVlogBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { VlogBinding: binding } } });
}

describe('VlogClient — Wails binding contract', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('create() calls VlogBinding.Create with the snake_case vlogwire request', async () => {
        const create = vi.fn<BindingFn>().mockResolvedValue({
            tenant_id: 't1',
            database: 'db',
            log: 'events',
            key: { version: 1, public_key: 'cGs=' },
        });
        installVlogBinding({ Create: create });
        const res = await new WailsClient().vlog.create({
            log: 'events',
            allowExpireBodies: true,
            signingKeyType: 'ed25519',
        });
        expect(create).toHaveBeenCalledWith({
            log: 'events',
            allow_expire_bodies: true,
            signing_key_type: 'ed25519',
        });
        expect(res).toEqual({ tenantId: 't1', database: 'db', log: 'events', key: { version: 1, publicKey: 'cGs=' } });
    });

    it('list() calls VlogBinding.List with no arguments', async () => {
        const list = vi.fn<BindingFn>().mockResolvedValue({ logs: ['a'] });
        installVlogBinding({ List: list });
        const res = await new WailsClient().vlog.list();
        expect(list).toHaveBeenCalledWith();
        expect(res.logs).toEqual(['a']);
    });

    it('append(), get() and the proofs pass their requests through', async () => {
        const append = vi.fn<BindingFn>().mockResolvedValue({ first_index: 0, count: 1 });
        const get = vi.fn<BindingFn>().mockResolvedValue({ entries: [{ index: 0, body: 'YQ==' }] });
        const inc = vi.fn<BindingFn>().mockResolvedValue({ index: 0, size: 1, hashes: [] });
        const con = vi.fn<BindingFn>().mockResolvedValue({ size1: 1, size2: 2, hashes: ['aA=='] });
        installVlogBinding({ Append: append, Get: get, ProveInclusion: inc, ProveConsistency: con });
        const vlog = new WailsClient().vlog;

        expect(await vlog.append({ log: 'l', entries: ['YQ=='] })).toEqual({ firstIndex: 0, count: 1 });
        expect(append).toHaveBeenCalledWith({ log: 'l', entries: ['YQ=='] });

        expect((await vlog.get({ log: 'l', from: 0, limit: 1 })).entries).toEqual([{ index: 0, body: 'YQ==' }]);
        expect(get).toHaveBeenCalledWith({ log: 'l', from: 0, limit: 1 });

        await vlog.proveInclusion({ log: 'l', index: 0, size: 1 });
        expect(inc).toHaveBeenCalledWith({ log: 'l', index: 0, size: 1 });

        expect((await vlog.proveConsistency({ log: 'l', size1: 1, size2: 2 })).hashes).toEqual(['aA==']);
        expect(con).toHaveBeenCalledWith({ log: 'l', size1: 1, size2: 2 });
    });

    it('head() and signHead() send a LogRequest', async () => {
        const head = vi.fn<BindingFn>().mockResolvedValue({ size: 1, root: 'cg==', bodies_from: 0, checkpoint_size: 0 });
        const sign = vi.fn<BindingFn>().mockResolvedValue({
            head: { size: 1, root: 'cg==', key_version: 1, signature_type: 'ed25519', signature: 'cw==' },
            checkpoint: 'Yw==',
        });
        installVlogBinding({ Head: head, SignHead: sign });
        const vlog = new WailsClient().vlog;

        expect(await vlog.head('l')).toEqual({ size: 1, root: 'cg==', bodiesFrom: 0, checkpointSize: 0 });
        expect(head).toHaveBeenCalledWith({ log: 'l' });

        const signed = await vlog.signHead('l');
        expect(sign).toHaveBeenCalledWith({ log: 'l' });
        expect(signed.head.keyVersion).toBe(1);
        expect(signed.head.signatureType).toBe('ed25519');
    });

    it('rotateKey() and expireBodies() send snake_case requests', async () => {
        const rotate = vi.fn<BindingFn>().mockResolvedValue({ key: { version: 2, public_key: 'cGs=' } });
        const expire = vi.fn<BindingFn>().mockResolvedValue({ expired: 4 });
        installVlogBinding({ RotateKey: rotate, ExpireBodies: expire });
        const vlog = new WailsClient().vlog;

        expect((await vlog.rotateKey({ log: 'l', signingKeyType: 'ed25519' })).key.publicKey).toBe('cGs=');
        expect(rotate).toHaveBeenCalledWith({ log: 'l', signing_key_type: 'ed25519' });

        expect((await vlog.expireBodies({ log: 'l', cutoff: 4 })).expired).toBe(4);
        expect(expire).toHaveBeenCalledWith({ log: 'l', cutoff: 4 });
    });
});
