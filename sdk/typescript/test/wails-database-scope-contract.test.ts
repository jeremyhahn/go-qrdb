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

/**
 * Cross-tab contract: DatabaseWorkspace hands one databaseScoped() client to
 * every data tab. The transaction, batch, watch and lock bindings take the
 * database first, as the KV, MVCC and lease bindings do, so every tab of a
 * named-database workspace reads and writes that database and never a
 * same-named key in the default database.
 */
describe('WailsClient databaseScoped data-plane operations stay in the database', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    /** Adds the transaction, batch, watch and lock bindings over the same per-database stores. */
    function installDataPlaneBindings(stores: Map<string, Map<string, string>>): {
        txExecute: ReturnType<typeof vi.fn<BindingFn>>;
        batchExecute: ReturnType<typeof vi.fn<BindingFn>>;
        importFile: ReturnType<typeof vi.fn<BindingFn>>;
        watch: ReturnType<typeof vi.fn<BindingFn>>;
        watchPrefix: ReturnType<typeof vi.fn<BindingFn>>;
        lock: ReturnType<typeof vi.fn<BindingFn>>;
    } {
        const storeOf = (database: unknown): Map<string, string> => {
            const name = database as string;
            if (!stores.has(name)) stores.set(name, new Map());
            return stores.get(name) as Map<string, string>;
        };
        // Both apply put ops to the named database, as the Go bindings do.
        const applyPuts = (database: unknown, ops: unknown): void => {
            for (const op of ops as { type: number; key: string; value?: string }[]) {
                if (op.type === 0) storeOf(database).set(op.key, op.value ?? '');
            }
        };
        const txExecute = vi.fn<BindingFn>(async (database, req) => {
            applyPuts(database, (req as { success_ops: unknown }).success_ops);
            return { succeeded: true, responses: [] };
        });
        const batchExecute = vi.fn<BindingFn>(async (database, ops) => {
            applyPuts(database, ops);
            return { responses: [] };
        });
        const importFile = vi.fn<BindingFn>(async () => ({ total: 0, succeeded: 0, failed: 0, errors: [] }));
        let nextWatchId = 0;
        const watch = vi.fn<BindingFn>(async () => ++nextWatchId);
        const watchPrefix = vi.fn<BindingFn>(async () => ++nextWatchId);
        const lock = vi.fn<BindingFn>(async () => ({ locks: [] }));
        const main = (globalThis as unknown as { window: { go: { main: Record<string, unknown> } } }).window.go
            .main;
        main.TransactionBinding = { Execute: txExecute };
        main.BatchBinding = { Execute: batchExecute, ImportFile: importFile };
        main.WatchBinding = { Watch: watch, WatchPrefix: watchPrefix, CancelWatch: vi.fn<BindingFn>(async () => null) };
        main.LockBinding = {
            Acquire: lock,
            TryLock: lock,
            Release: lock,
            Extend: lock,
            Status: lock,
            List: lock,
        };
        (globalThis as unknown as { window: { runtime: unknown } }).window.runtime = {
            EventsOnMultiple: () => () => undefined,
        };
        return { txExecute, batchExecute, importFile, watch, watchPrefix, lock };
    }

    const putK = { type: 0 as const, key: 'k', value: 'from-tx' };

    it('a transaction or batch in a named database writes that database, not the default', async () => {
        const { stores } = installScopedBindings();
        const { txExecute, batchExecute } = installDataPlaneBindings(stores);
        const base = new WailsClient();
        const orders = base.databaseScoped('orders');

        // The default database has its own k; the Data tab of orders reads k.
        await base.kv.put('k', 'default');

        await orders.transaction.execute({ compares: [], successOps: [putK], failureOps: [] });
        expect(txExecute.mock.calls[0]?.[0]).toBe('orders');
        expect(await orders.kv.get('k')).toBe('from-tx');

        await orders.batch.execute([{ type: 0, key: 'k', value: 'from-batch' }]);
        expect(batchExecute.mock.calls[0]?.[0]).toBe('orders');
        expect(await orders.kv.get('k')).toBe('from-batch');

        expect(stores.get('')?.get('k')).toBe('default');
        expect(await base.kv.get('k')).toBe('default');
    });

    it('import, watch and lock calls carry the named database', async () => {
        const { stores } = installScopedBindings();
        const { importFile, watch, watchPrefix, lock } = installDataPlaneBindings(stores);
        const orders = new WailsClient().databaseScoped('orders');

        await orders.batch.importFile('/data.csv', { format: 'csv', key_field: 'id', value_mode: 'row' });
        await orders.watch.watch('k', {});
        await orders.watch.watchPrefix('p/', {});
        await orders.lock.acquire({ key: 'k', ttl: 1 });
        await orders.lock.tryLock({ key: 'k', ttl: 1 });
        await orders.lock.release({ key: 'k', fencing_token: 1 });
        await orders.lock.extend({ key: 'k', fencing_token: 1 });
        await orders.lock.status('k');
        await orders.lock.list('p/');

        expect(importFile.mock.calls[0]?.slice(0, 2)).toEqual(['orders', '/data.csv']);
        expect(watch.mock.calls[0]?.slice(0, 2)).toEqual(['orders', 'k']);
        expect(watchPrefix.mock.calls[0]?.slice(0, 2)).toEqual(['orders', 'p/']);
        expect(lock).toHaveBeenCalledTimes(6);
        for (const call of lock.mock.calls) {
            expect(call[0]).toBe('orders');
        }
        expect(lock.mock.calls[4]?.[1]).toBe('k');
        expect(lock.mock.calls[5]?.[1]).toBe('p/');
    });

    it('the default database (and "system") passes the empty database name', async () => {
        const { stores } = installScopedBindings();
        const { txExecute, batchExecute, watch, lock } = installDataPlaneBindings(stores);
        const base = new WailsClient();

        await base.databaseScoped('system').transaction.execute({
            compares: [],
            successOps: [putK],
            failureOps: [],
        });
        await base.batch.execute([putK]);
        await base.watch.watch('k', {});
        await base.databaseScoped('system').lock.status('k');

        expect(txExecute.mock.calls[0]?.[0]).toBe('');
        expect(batchExecute.mock.calls[0]?.[0]).toBe('');
        expect(watch.mock.calls[0]?.[0]).toBe('');
        expect(lock.mock.calls[0]?.slice(0, 2)).toEqual(['', 'k']);
        expect(stores.get('')?.get('k')).toBe('from-tx');
    });

    it('index calls carry the named database, so indexes see the scoped KV writes', async () => {
        installScopedBindings();
        const index = vi.fn<BindingFn>(async () => ({ names: [], keys: [], entries: [] }));
        const main = (globalThis as unknown as { window: { go: { main: Record<string, unknown> } } }).window.go
            .main;
        main.IndexBinding = { Create: index, Drop: index, Query: index, Scan: index, List: index };
        const base = new WailsClient();
        const orders = base.databaseScoped('orders');

        await orders.index.create({ name: 'by_status', fieldName: 'status', unique: true });
        await orders.index.query('by_status', 'open');
        await orders.index.scan('by_status', 'a', 'z');
        await orders.index.list();
        await orders.index.drop('by_status');
        await base.databaseScoped('system').index.list();
        await base.index.query('by_status', 'open');

        expect(index).toHaveBeenCalledTimes(7);
        expect(index.mock.calls[0]?.[0]).toBe('orders');
        expect(index.mock.calls[1]?.slice(0, 3)).toEqual(['orders', 'by_status', 'open']);
        expect(index.mock.calls[2]?.slice(0, 4)).toEqual(['orders', 'by_status', 'a', 'z']);
        expect(index.mock.calls[3]?.[0]).toBe('orders');
        expect(index.mock.calls[4]?.slice(0, 2)).toEqual(['orders', 'by_status']);
        expect(index.mock.calls[5]?.[0]).toBe('');
        expect(index.mock.calls[6]?.slice(0, 3)).toEqual(['', 'by_status', 'open']);
    });
});
