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
 * Raft request wire contract: the SDK's DatabaseRaftConfig must name every key
 * the way the REST server reads it, discovery blocks and their write-only
 * secrets included. The fixture is the Go SDK's encoding of a request setting
 * every field -- TestDatabaseRaftRequestWire_Fixture (pkg/api/rest) fails
 * when it drifts, and decodes it through the REST handler refusing unknown
 * keys -- so a key this SDK misspells is one the fixture does not carry.
 *
 * The key tables are held to the interfaces by `satisfies`, which only the
 * typecheck enforces: `npm test` runs `tsc -p tsconfig.test.json` first.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import type {
    DatabaseRaftConfig,
    DNSDiscoveryConfig,
    MulticastDiscoveryConfig,
    StaticDiscoveryConfig,
    TokenDiscoveryConfig,
} from '../src/types.js';
import restRaftRequest from './fixtures/rest-database-raft-request.json';

const fixture: Record<string, unknown> = restRaftRequest;

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
} satisfies Record<keyof DatabaseRaftConfig, true>;

const staticKeys = { peers: true, sharedSecret: true } satisfies Record<keyof StaticDiscoveryConfig, true>;

const multicastKeys = {
    sharedSecret: true,
    multicastGroup: true,
    interface: true,
    minPeers: true,
    announceInterval: true,
    discoverTimeout: true,
} satisfies Record<keyof MulticastDiscoveryConfig, true>;

const dnsKeys = {
    service: true,
    proto: true,
    domain: true,
    lookupTimeout: true,
} satisfies Record<keyof DNSDiscoveryConfig, true>;

const tokenKeys = {
    endpoint: true,
    token: true,
    clusterSize: true,
    timeout: true,
} satisfies Record<keyof TokenDiscoveryConfig, true>;

/** Each object of the request and the keys the SDK declares for it. */
const objects: ReadonlyArray<{ path: string; declared: Record<string, true> }> = [
    { path: '', declared: raftKeys },
    { path: 'static', declared: staticKeys },
    { path: 'multicast', declared: multicastKeys },
    { path: 'dns', declared: dnsKeys },
    { path: 'token', declared: tokenKeys },
];

function objectAt(path: string): Record<string, unknown> {
    return path === '' ? fixture : (fixture[path] as Record<string, unknown>);
}

describe('DatabaseRaftConfig wire contract (REST request)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    for (const { path, declared } of objects) {
        const where = path === '' ? 'DatabaseRaftConfig' : `DatabaseRaftConfig.${path}`;

        it(`${where}: the SDK declares exactly the keys the server reads`, () => {
            expect(Object.keys(declared).sort(), where).toEqual(Object.keys(objectAt(path)).sort());
        });
    }

    it('database.updateRaft sends every field, secrets included, as the server reads it', async () => {
        vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ status: 'ok' }), { status: 200 }));
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        let sent: unknown;
        vi.stubGlobal('fetch', async (_url: string, init?: RequestInit) => {
            sent = JSON.parse(String(init?.body));
            return new Response(JSON.stringify({ name: 'orders' }), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        });
        const raft = fixture as unknown as DatabaseRaftConfig;
        await client.database.updateRaft('orders', raft);

        expect(sent).toEqual(fixture);
    });
});
