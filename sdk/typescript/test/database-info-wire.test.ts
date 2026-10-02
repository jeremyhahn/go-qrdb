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
    DatabaseCompactionConfig,
    DatabaseConnectionConfig,
    DatabaseEtcdProtocolInfo,
    DatabaseInfo,
    DatabaseProtocolInfo,
    DatabaseRaftInfo,
    DatabaseSQLConfig,
    DNSDiscoveryInfo,
    MulticastDiscoveryInfo,
    StaticDiscoveryInfo,
    TokenDiscoveryInfo,
} from '../src/types.js';
import restDatabaseInfo from './fixtures/rest-database-info.json';

const fixture: Record<string, unknown> = restDatabaseInfo;

/** Every key DatabaseInfo declares. */
const databaseInfoKeys = {
    tenantId: true,
    name: true,
    engine: true,
    backend: true,
    status: true,
    protocol: true,
    sql: true,
    compaction: true,
    dataDir: true,
    shardId: true,
    mode: true,
    storageCodec: true,
    replicas: true,
    dedicatedWal: true,
    raft: true,
    barrier: true,
    connection: true,
} satisfies Record<keyof DatabaseInfo, true>;

const protocolKeys = { mysql: true, postgresql: true, etcd: true } satisfies Record<keyof DatabaseProtocolInfo, true>;

const etcdKeys = {
    enabled: true,
    listenAddr: true,
    advertiseClientUrls: true,
} satisfies Record<keyof DatabaseEtcdProtocolInfo, true>;

const raftKeys = {
    clusterMode: true,
    replicaId: true,
    raftAddress: true,
    peers: true,
    localNodeCount: true,
    discovery: true,
    static: true,
    multicast: true,
    dns: true,
    token: true,
} satisfies Record<keyof DatabaseRaftInfo, true>;

const staticKeys = { peers: true, sharedSecretSet: true } satisfies Record<keyof StaticDiscoveryInfo, true>;

const multicastKeys = {
    sharedSecretSet: true,
    multicastGroup: true,
    interface: true,
    minPeers: true,
    announceInterval: true,
    discoverTimeout: true,
} satisfies Record<keyof MulticastDiscoveryInfo, true>;

const dnsKeys = {
    service: true,
    proto: true,
    domain: true,
    lookupTimeout: true,
} satisfies Record<keyof DNSDiscoveryInfo, true>;

const tokenKeys = {
    endpoint: true,
    tokenSet: true,
    clusterSize: true,
    timeout: true,
} satisfies Record<keyof TokenDiscoveryInfo, true>;

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

const compactionKeys = {
    mode: true,
    retainRevisions: true,
    retainPeriod: true,
} satisfies Record<keyof DatabaseCompactionConfig, true>;

/**
 * Each object of the response and the keys the SDK declares for it. There are
 * no listed gaps: a key the server sends that the SDK does not type fails, as
 * does a key the SDK types that the server does not send.
 */
const objects: ReadonlyArray<{
    path: string;
    declared: Record<string, true>;
    untyped: readonly string[];
}> = [
    { path: '', declared: databaseInfoKeys, untyped: [] },
    { path: 'protocol', declared: protocolKeys, untyped: [] },
    { path: 'protocol.etcd', declared: etcdKeys, untyped: [] },
    { path: 'raft', declared: raftKeys, untyped: [] },
    { path: 'raft.static', declared: staticKeys, untyped: [] },
    { path: 'raft.multicast', declared: multicastKeys, untyped: [] },
    { path: 'raft.dns', declared: dnsKeys, untyped: [] },
    { path: 'raft.token', declared: tokenKeys, untyped: [] },
    { path: 'barrier', declared: barrierKeys, untyped: [] },
    { path: 'connection', declared: connectionKeys, untyped: [] },
    { path: 'sql', declared: sqlKeys, untyped: [] },
    { path: 'compaction', declared: compactionKeys, untyped: [] },
];

function objectAt(path: string): Record<string, unknown> {
    let at: Record<string, unknown> = fixture;
    for (const key of path === '' ? [] : path.split('.')) {
        at = at[key] as Record<string, unknown>;
    }
    return at;
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
        expect(info.compaction?.retainPeriod).toBe('720h');
        expect(info.tenantId).toBe('acme');
        expect(info.protocol?.etcd?.listenAddr).toBe('0.0.0.0:2379');
        expect(info.raft?.static?.sharedSecretSet).toBe(true);
        expect(info.raft?.multicast?.sharedSecretSet).toBe(true);
        expect(info.raft?.token?.tokenSet).toBe(true);
        expect(info.raft?.dns?.domain).toBe('example.com');
    });
});
