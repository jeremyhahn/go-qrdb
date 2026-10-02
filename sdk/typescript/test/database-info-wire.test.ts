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
 * DatabaseInfo wire contract: the SDK's DatabaseInfo must name every key the
 * way the REST server writes it. The fixture is the server's real response --
 * the Go test TestDatabaseInfoWire_FixtureIsTheRESTResponse
 * (pkg/api/rest) fails when it drifts from what the handler sends -- so a key
 * the SDK misspells is one the fixture does not carry.
 *
 * The key tables are held to the interfaces by `satisfies`, which only the
 * typecheck enforces: `npm test` runs `tsc -p tsconfig.test.json` first.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import type {
    DatabaseBarrierConfig,
    DatabaseConnectionConfig,
    DatabaseInfo,
    DatabaseProtocolConfig,
    DatabaseRaftConfig,
    DatabaseSQLConfig,
} from '../src/types.js';
import restDatabaseInfo from './fixtures/rest-database-info.json';

const fixture: Record<string, unknown> = restDatabaseInfo;

/** Every key DatabaseInfo declares. */
const databaseInfoKeys = {
    name: true,
    engine: true,
    backend: true,
    status: true,
    protocol: true,
    sql: true,
    dataDir: true,
    shardId: true,
    mode: true,
    replicas: true,
    dedicatedWal: true,
    raft: true,
    barrier: true,
    connection: true,
} satisfies Record<keyof DatabaseInfo, true>;

const protocolKeys = { mysql: true, postgresql: true } satisfies Record<keyof DatabaseProtocolConfig, true>;

const raftKeys = {
    clusterMode: true,
    shardId: true,
    replicaId: true,
    raftAddress: true,
    peers: true,
    localNodeCount: true,
} satisfies Record<keyof DatabaseRaftConfig, true>;

const barrierKeys = {
    enabled: true,
    mode: true,
    sharesRequired: true,
    sharesTotal: true,
} satisfies Record<keyof DatabaseBarrierConfig, true>;

const connectionKeys = {
    transport: true,
    address: true,
    caFile: true,
    certFile: true,
    keyFile: true,
} satisfies Record<keyof DatabaseConnectionConfig, true>;

const sqlKeys = { concurrency: true } satisfies Record<keyof DatabaseSQLConfig, true>;

/**
 * Each object of the response, the keys the SDK declares for it, and the keys
 * REST sends that the SDK does not type yet. A key in neither list fails.
 */
const objects: ReadonlyArray<{
    path: string;
    declared: Record<string, true>;
    untyped: readonly string[];
}> = [
    { path: '', declared: databaseInfoKeys, untyped: [] },
    { path: 'protocol', declared: protocolKeys, untyped: ['etcd'] },
    { path: 'raft', declared: raftKeys, untyped: ['discovery', 'dns', 'multicast', 'static', 'token'] },
    { path: 'barrier', declared: barrierKeys, untyped: [] },
    { path: 'connection', declared: connectionKeys, untyped: [] },
    { path: 'sql', declared: sqlKeys, untyped: [] },
];

function objectAt(path: string): Record<string, unknown> {
    return (path === '' ? fixture : fixture[path]) as Record<string, unknown>;
}

describe('DatabaseInfo wire contract (REST)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    for (const { path, declared, untyped } of objects) {
        const where = path === '' ? 'DatabaseInfo' : `DatabaseInfo.${path}`;

        it(`${where}: every key the SDK declares is a key the server sends`, () => {
            const sent = Object.keys(objectAt(path));
            const missing = Object.keys(declared).filter((k) => !sent.includes(k));
            expect(missing, `${where} declares keys the REST response never carries`).toEqual([]);
        });

        it(`${where}: every key the server sends is declared or a listed gap`, () => {
            const sent = Object.keys(objectAt(path));
            const unknown = sent.filter((k) => !(k in declared)).sort();
            expect(unknown, `${where} receives keys the SDK does not type`).toEqual([...untyped].sort());
        });
    }

    it('database.get surfaces the shard ID and data directory', async () => {
        vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ status: 'ok' }), { status: 200 }));
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        vi.stubGlobal(
            'fetch',
            async () =>
                new Response(JSON.stringify(fixture), {
                    status: 200,
                    headers: { 'Content-Type': 'application/json' },
                }),
        );
        const info: DatabaseInfo = await client.database.get('orders');

        expect(info.shardId).toBe(1007);
        expect(info.dataDir).toBe('/var/lib/qrdb/databases/orders');
        expect(info.replicas).toBe(3);
        expect(info.dedicatedWal).toBe(true);
        expect(info.raft?.replicaId).toBe(2);
        expect(info.sql?.concurrency).toBe('pessimistic');
    });
});
