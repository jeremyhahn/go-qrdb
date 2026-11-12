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

// Contract tests for WailsClient.volumePools: each method must call the
// matching main.VolumePoolBinding method with the argument shape the Go
// binding (cmd/qrdb/bindings_volume_pool.go) decodes, and map its result
// to the VolumePoolClient response types.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installVolumePoolBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { VolumePoolBinding: binding } } });
}

const pool = {
    name: 'fast',
    provider: 'dir',
    capacityBytes: 2048,
    reservedBytes: 1024,
    usedBytes: 512,
    tenantCount: 1,
    nodes: { ready: 1, locked: 0, unavailable: 0, unmapped: 0 },
    overcommit: 1.5,
};

describe('WailsClient volumePools', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('list() wraps the binding slice in { pools }', async () => {
        const List = vi.fn().mockResolvedValue([pool]);
        installVolumePoolBinding({ List });
        const resp = await new WailsClient().volumePools.list();
        expect(List).toHaveBeenCalledWith();
        expect(resp).toEqual({ pools: [pool] });
    });

    it('list() returns an empty array when the binding returns null', async () => {
        installVolumePoolBinding({ List: vi.fn().mockResolvedValue(null) });
        expect(await new WailsClient().volumePools.list()).toEqual({ pools: [] });
    });

    it('get() passes the pool name and returns the detail', async () => {
        const detail = { ...pool, nodeReports: [{ nodeId: 'node-1', state: 'ready' }] };
        const Get = vi.fn().mockResolvedValue(detail);
        installVolumePoolBinding({ Get });
        const resp = await new WailsClient().volumePools.get('fast');
        expect(Get).toHaveBeenCalledWith('fast');
        expect(resp).toEqual(detail);
    });

    it('create() passes the request with its camelCase keys unchanged', async () => {
        const Create = vi.fn().mockResolvedValue(pool);
        installVolumePoolBinding({ Create });
        const req = { name: 'fast', provider: 'dir', capacityBytes: 2048, overcommit: 1.5 };
        const resp = await new WailsClient().volumePools.create(req);
        expect(Create).toHaveBeenCalledWith(req);
        expect(resp).toEqual(pool);
    });

    it('update() passes the pool name and the request carrying it', async () => {
        const Update = vi.fn().mockResolvedValue({ ...pool, description: 'hot' });
        installVolumePoolBinding({ Update });
        const resp = await new WailsClient().volumePools.update('fast', { description: 'hot', capacityBytes: 4096 });
        expect(Update).toHaveBeenCalledWith('fast', { name: 'fast', description: 'hot', capacityBytes: 4096 });
        expect(resp.description).toBe('hot');
    });

    it('delete() passes the pool name and resolves to undefined', async () => {
        const Delete = vi.fn().mockResolvedValue(undefined);
        installVolumePoolBinding({ Delete });
        await expect(new WailsClient().volumePools.delete('fast')).resolves.toBeUndefined();
        expect(Delete).toHaveBeenCalledWith('fast');
    });

    it('listTenants() passes positional paging and camelCases the page token', async () => {
        const ListTenants = vi.fn().mockResolvedValue({
            tenants: [{ tenantId: 'acme', reservedBytes: 10 }],
            next_page_token: 'tok2',
        });
        installVolumePoolBinding({ ListTenants });
        const resp = await new WailsClient().volumePools.listTenants('fast', { pageSize: 5, pageToken: 'tok1' });
        expect(ListTenants).toHaveBeenCalledWith('fast', 5, 'tok1');
        expect(resp).toEqual({ tenants: [{ tenantId: 'acme', reservedBytes: 10 }], nextPageToken: 'tok2' });
    });

    it('listTenants() defaults paging and omits an empty page token', async () => {
        const ListTenants = vi.fn().mockResolvedValue({ tenants: null });
        installVolumePoolBinding({ ListTenants });
        const resp = await new WailsClient().volumePools.listTenants('fast');
        expect(ListTenants).toHaveBeenCalledWith('fast', 0, '');
        expect(resp).toEqual({ tenants: [], nextPageToken: undefined });
    });

    it('propagates a binding rejection', async () => {
        installVolumePoolBinding({ Delete: vi.fn().mockRejectedValue(new Error('pool has tenants: acme')) });
        await expect(new WailsClient().volumePools.delete('fast')).rejects.toThrow('pool has tenants: acme');
    });
});
