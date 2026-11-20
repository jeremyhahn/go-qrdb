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
 * Wire contract for a databaseScoped() WailsClient: the KV, lease and MVCC
 * bindings all take the scoped database first, so browsing, ordinary saves,
 * leased saves and historical reads reach the same database. The bindings
 * below keep one store per database, so a call routed to the wrong database
 * reads or writes a different store.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function b64(text: string): string {
    // Every key and value here is ASCII.
    return btoa(text);
}

/** Installs KV, Lease and MVCC bindings that keep one store per database. */
function installScopedBindings(): {
    stores: Map<string, Map<string, string>>;
    leases: Map<string, number[]>;
    kvGet: ReturnType<typeof vi.fn<BindingFn>>;
} {
    const stores = new Map<string, Map<string, string>>();
    const leases = new Map<string, number[]>();
    const store = (db: unknown): Map<string, string> => {
        const name = String(db);
        if (!stores.has(name)) stores.set(name, new Map());
        return stores.get(name) as Map<string, string>;
    };
    const kvGet = vi.fn<BindingFn>(async (db, key) => {
        const v = store(db).get(String(key));
        if (v === undefined) throw new Error(`transport: key not found: ${String(key)}`);
        return v;
    });
    let leaseId = 0;
    vi.stubGlobal('window', {
        go: {
            main: {
                KVBinding: {
                    Put: vi.fn<BindingFn>(async (db, key, value) => {
                        store(db).set(String(key), String(value));
                    }),
                    Get: kvGet,
                    Delete: vi.fn<BindingFn>(async (db, key) => {
                        store(db).delete(String(key));
                    }),
                    List: vi.fn<BindingFn>(async (db, prefix) =>
                        [...store(db).keys()].filter((k) => k.startsWith(String(prefix)))),
                    Exists: vi.fn<BindingFn>(async (db, key) => store(db).has(String(key))),
                    Scan: vi.fn<BindingFn>(async (db, prefix) =>
                        Object.fromEntries([...store(db)].filter(([k]) => k.startsWith(String(prefix))))),
                },
                LeaseBinding: {
                    Grant: vi.fn<BindingFn>(async (db, ttl) => {
                        const id = ++leaseId;
                        leases.set(String(db), [...(leases.get(String(db)) ?? []), id]);
                        return { id, ttl };
                    }),
                    Revoke: vi.fn<BindingFn>(async () => undefined),
                    KeepAlive: vi.fn<BindingFn>(async (_db, id) => ({ id })),
                    TimeToLive: vi.fn<BindingFn>(async (_db, id) => ({ id })),
                    List: vi.fn<BindingFn>(async (db) => (leases.get(String(db)) ?? []).map((id) => ({ id }))),
                },
                MVCCBinding: {
                    PutWithLease: vi.fn<BindingFn>(async (db, key, value, lease) => {
                        store(db).set(String(key), String(value));
                        return { key: b64(String(key)), value: b64(String(value)), revision: 1, leaseId: lease };
                    }),
                    GetRevision: vi.fn<BindingFn>(async (db, key) => {
                        const v = store(db).get(String(key));
                        if (v === undefined) throw new Error(`transport: key not found: ${String(key)}`);
                        return { key: b64(String(key)), value: b64(v), revision: 1 };
                    }),
                },
            },
        },
    });
    return { stores, leases, kvGet };
}

describe('WailsClient databaseScoped KV and lease', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('a named database sees its own KV writes and none of the default database', async () => {
        const { stores } = installScopedBindings();
        const base = new WailsClient();
        const orders = base.databaseScoped('orders');

        await orders.kv.put('k', 'named');
        await base.kv.put('k', 'default');

        expect(stores.get('orders')?.get('k')).toBe('named');
        expect(stores.get('')?.get('k')).toBe('default');
        expect(await orders.kv.get('k')).toBe('named');
        expect(await orders.kv.scan('')).toEqual({ k: 'named' });
        expect(await orders.kv.list('')).toEqual(['k']);
        expect(await base.kv.get('k')).toBe('default');
        expect(await base.databaseScoped('system').kv.get('k')).toBe('default');
    });

    it('a leased save in a named database is visible to its KV browse', async () => {
        const { leases } = installScopedBindings();
        const orders = new WailsClient().databaseScoped('orders');

        const lease = await orders.lease.grant(30);
        await orders.mvcc.putWithLease('leased', 'v', lease.id);

        expect(leases.get('orders')).toEqual([lease.id]);
        expect(leases.get('')).toBeUndefined();
        expect(await orders.kv.list('')).toEqual(['leased']);
        expect(await orders.kv.exists('leased')).toBe(true);
        expect(await orders.lease.list()).toEqual([{ id: lease.id }]);
    });

    it('editing a displayed key writes the database its revision came from', async () => {
        const { stores } = installScopedBindings();
        const base = new WailsClient();
        const orders = base.databaseScoped('orders');
        await orders.kv.put('k', 'v1');

        const shown = await orders.mvcc.getRevision('k');
        await orders.kv.put(shown.key, 'v2');
        await orders.kv.delete('gone');

        expect(stores.get('orders')?.get('k')).toBe('v2');
        expect(stores.get('')?.has('k') ?? false).toBe(false);
    });
});
