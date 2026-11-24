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
 * Wire contract for WailsClient.groups: every call goes straight through to
 * the desktop GroupBinding (cmd/qrdb/bindings_groups.go), whose arguments
 * are the Go transport group types. Their JSON keys are camelCase with
 * Go-style ID suffixes (tenantID, roleIDs, pageSize), so requests must reach
 * the binding in exactly that form -- never snake-cased -- and responses map
 * onto the SDK's Group types the way the REST client maps them.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Client } from '../src/interfaces.js';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installGroupBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { GroupBinding: binding } } });
}

const wireGroup = {
    id: 'g1',
    tenantID: 'acme',
    name: 'eng',
    description: 'engineering',
    roleIDs: ['reader'],
    createdAt: '2026-11-24T00:00:00Z',
    updatedAt: '2026-11-24T01:00:00Z',
};

const sdkGroup = {
    id: 'g1',
    tenantId: 'acme',
    name: 'eng',
    description: 'engineering',
    roleIds: ['reader'],
    createdAt: '2026-11-24T00:00:00Z',
    updatedAt: '2026-11-24T01:00:00Z',
};

describe('WailsClient groups', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('is reachable through the shared Client interface', () => {
        installGroupBinding({});
        const client: Client = new WailsClient();
        expect(typeof client.groups.create).toBe('function');
    });

    it('create() sends the camelCase wire request and maps the group back', async () => {
        const create = vi.fn<BindingFn>().mockResolvedValue(wireGroup);
        installGroupBinding({ Create: create });

        const got = await new WailsClient().groups.create({
            name: 'eng',
            tenantId: 'acme',
            description: 'engineering',
            roleIds: ['reader'],
        });

        expect(create).toHaveBeenCalledTimes(1);
        expect(create).toHaveBeenCalledWith({
            name: 'eng',
            tenantID: 'acme',
            description: 'engineering',
            roleIDs: ['reader'],
        });
        expect(got).toEqual(sdkGroup);
    });

    it('get() and getByName() call Get and GetByName', async () => {
        const get = vi.fn<BindingFn>().mockResolvedValue(wireGroup);
        const getByName = vi.fn<BindingFn>().mockResolvedValue(wireGroup);
        installGroupBinding({ Get: get, GetByName: getByName });
        const groups = new WailsClient().groups;

        expect(await groups.get('g1')).toEqual(sdkGroup);
        expect(get).toHaveBeenCalledWith('g1');

        expect(await groups.getByName('eng', 'acme')).toEqual(sdkGroup);
        expect(getByName).toHaveBeenCalledWith({ name: 'eng', tenantID: 'acme' });

        await groups.getByName('eng');
        expect(getByName).toHaveBeenLastCalledWith({ name: 'eng' });
    });

    it('update() sends only the set fields with the id', async () => {
        const update = vi.fn<BindingFn>().mockResolvedValue({ ...wireGroup, name: 'platform' });
        installGroupBinding({ Update: update });

        const got = await new WailsClient().groups.update({ id: 'g1', name: 'platform', roleIds: [] });

        expect(update).toHaveBeenCalledWith({ id: 'g1', name: 'platform', roleIDs: [] });
        expect(got.name).toBe('platform');
    });

    it('delete() calls Delete with the id', async () => {
        const del = vi.fn<BindingFn>().mockResolvedValue(undefined);
        installGroupBinding({ Delete: del });

        await new WailsClient().groups.delete('g1');

        expect(del).toHaveBeenCalledWith('g1');
    });

    it('list() sends tenantID/page/pageSize and maps the page', async () => {
        const list = vi.fn<BindingFn>().mockResolvedValue({ groups: [wireGroup], total: 3, hasMore: true });
        installGroupBinding({ List: list });
        const groups = new WailsClient().groups;

        const page = await groups.list({ tenantId: 'acme', page: 2, pageSize: 1 });

        expect(list).toHaveBeenCalledWith({ tenantID: 'acme', page: 2, pageSize: 1 });
        expect(page).toEqual({ groups: [sdkGroup], total: 3, hasMore: true });

        list.mockResolvedValueOnce({ groups: null });
        expect(await groups.list()).toEqual({ groups: [], total: 0, hasMore: false });
        expect(list).toHaveBeenLastCalledWith({});
    });

    it('addMember() and removeMember() pass the user id first, as GroupBinding takes it', async () => {
        const add = vi.fn<BindingFn>().mockResolvedValue(undefined);
        const remove = vi.fn<BindingFn>().mockResolvedValue(undefined);
        installGroupBinding({ AddMember: add, RemoveMember: remove });
        const groups = new WailsClient().groups;

        // 2^53+1: the first integer a JavaScript number cannot hold.
        await groups.addMember('g1', '9007199254740993');
        await groups.removeMember('g1', '9007199254740993');

        expect(add).toHaveBeenCalledWith('9007199254740993', 'g1');
        expect(remove).toHaveBeenCalledWith('9007199254740993', 'g1');
    });

    it('members() keeps the decimal-string user id exact above 2^53-1', async () => {
        const members = vi.fn<BindingFn>().mockResolvedValue({
            members: [{ userID: '9007199254740993', username: 'alice', tenantID: 'acme' }],
        });
        installGroupBinding({ Members: members });

        const got = await new WailsClient().groups.members('g1');

        expect(members).toHaveBeenCalledWith('g1');
        expect(got).toEqual([{ userId: '9007199254740993', username: 'alice', tenantId: 'acme' }]);
    });

    it('a binding rejection propagates', async () => {
        installGroupBinding({ Get: vi.fn<BindingFn>().mockRejectedValue(new Error('group not found')) });

        await expect(new WailsClient().groups.get('nope')).rejects.toThrow('group not found');
    });
});
