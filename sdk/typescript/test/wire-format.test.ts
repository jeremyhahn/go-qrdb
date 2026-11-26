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
 * Wire-format contract tests: verify that the SDK correctly parses the exact
 * snake_case JSON the Go REST server emits and returns properly-populated
 * camelCase TypeScript types to callers.
 *
 * Each test feeds the SDK a literal JSON fixture that matches what the Go
 * transport structs produce via encoding/json (with snake_case json tags) and
 * asserts that every camelCase field in the declared SDK type is populated.
 *
 * This catches regressions where:
 *  - The transformer is removed or broken.
 *  - A new Go type field is added with a snake_case tag but no corresponding
 *    camelCase field is added to the TS type.
 *  - An existing test mock uses camelCase (lying about the wire format) and
 *    masks a real parsing bug.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import type {
    HealthResponse,
    StatusResponse,
    PeersResponse,
    CapabilitiesResponse,
    MVCCKeyValue,
    LeaseInfo,
    WatchEvent,
    WatchResponse,
} from '../src/types.js';

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

/**
 * Installs a global mock fetch that responds with the given snake_case JSON
 * body (status 200) and returns the RestClient already connected.
 */
async function makeConnectedClient(wireResponse: unknown): Promise<RestClient> {
    // First: mock for the connect() call (health check + status).
    vi.stubGlobal(
        'fetch',
        async (): Promise<Response> =>
            new Response(
                JSON.stringify({ status: 'ok', is_leader: true }),
                { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
    );

    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');

    // Now: install the actual fixture for the method under test.
    vi.stubGlobal(
        'fetch',
        async (): Promise<Response> =>
            new Response(
                JSON.stringify(wireResponse),
                { status: 200, headers: { 'Content-Type': 'application/json' } },
            ),
    );

    return client;
}

// ---------------------------------------------------------------------------
// Fixture definitions — these are the exact JSON shapes the Go server emits.
// Field names match the `json:"..."` struct tags in sdk/go/transport/types.go.
// ---------------------------------------------------------------------------

/** Exact wire JSON for GET /health */
const healthWire = {
    status: 'ok',
    is_leader: true,
};

/** Exact wire JSON for GET /status */
const statusWire = {
    version: '1.2.3-test',
    node_id: 'node-abc-123',
    raft_address: '127.0.0.1:63001',
    is_leader: true,
    leader_id: 1,
    shard_count: 1,
    is_draining: false,
    shards: null,
};

/** Exact wire JSON for GET /peers */
const peersWire = {
    shards: [
        {
            shard_id: 1,
            leader_id: 1,
            peers: [
                { replica_id: 1, address: '127.0.0.1:63001', is_leader: true },
                { replica_id: 2, address: '127.0.0.1:63011', is_leader: false },
            ],
        },
    ],
};

/** Exact wire JSON for GET /capabilities */
const capabilitiesWire = {
    kv: true,
    mvcc: true,
    transaction: true,
    batch: true,
    watch: true,
    lease: true,
    index: true,
    timeSeries: false,
    vector: false,
};

/** Exact wire JSON for MVCC key-value (from range/get endpoints) */
const mvccKVWire = {
    key: 'aGVsbG8=',
    value: 'd29ybGQ=',
    create_revision: 5,
    mod_revision: 10,
    version: 3,
    lease_id: 0,
};

/** Exact wire JSON for GET /lease/list */
const leaseListWire = {
    leases: [
        {
            id: 7549794765944713000,
            ttl: 25,
            granted_ttl: 30,
            remaining_ttl: 25,
            keys: [],
        },
    ],
};

/** Exact wire JSON for a WatchResponse SSE frame */
const watchResponseWire = {
    watch_id: 42,
    events: [
        {
            type: 'PUT',
            kv: {
                key: 'aGVsbG8=',
                value: 'd29ybGQ=',
                create_revision: 1,
                mod_revision: 1,
                version: 1,
                lease_id: 0,
            },
            prev_kv: null,
        },
    ],
    canceled: false,
    cancel_reason: '',
    revision: 1,
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('Wire-format contract: admin endpoints', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('GET /health — snake_case wire → camelCase HealthResponse', async () => {
        const client = await makeConnectedClient(healthWire);
        const health: HealthResponse = await client.admin.health();

        expect(health.status).toBe('ok');
        // The critical field: was undefined before the fix.
        expect(health.isLeader).toBe(true);
    });

    it('GET /status — snake_case wire → camelCase StatusResponse', async () => {
        const client = await makeConnectedClient(statusWire);
        const status: StatusResponse = await client.admin.status();

        expect(status.version).toBe('1.2.3-test');
        expect(status.nodeId).toBe('node-abc-123');
        expect(status.raftAddress).toBe('127.0.0.1:63001');
        expect(status.isLeader).toBe(true);
        expect(status.leaderId).toBe(1);
        expect(status.shardCount).toBe(1);
        expect(status.isDraining).toBe(false);
    });

    it('GET /peers — snake_case wire → camelCase PeersResponse', async () => {
        const client = await makeConnectedClient(peersWire);
        const peers: PeersResponse = await client.admin.peers();

        expect(peers.shards).toHaveLength(1);
        const shard = peers.shards[0];
        expect(shard.shardId).toBe(1);
        expect(shard.leaderId).toBe(1);

        expect(shard.peers).toHaveLength(2);
        const leader = shard.peers[0];
        expect(leader.replicaId).toBe(1);
        expect(leader.address).toBe('127.0.0.1:63001');
        expect(leader.isLeader).toBe(true);

        const follower = shard.peers[1];
        expect(follower.replicaId).toBe(2);
        expect(follower.isLeader).toBe(false);
    });

    it('GET /capabilities — already-camelCase timeSeries field passes through', async () => {
        const client = await makeConnectedClient(capabilitiesWire);
        const caps: CapabilitiesResponse = await client.admin.capabilities();

        expect(caps.kv).toBe(true);
        expect(caps.mvcc).toBe(true);
        // timeSeries has a camelCase json tag in Go — transformer must not mangle it.
        expect(caps.timeSeries).toBe(false);
        expect(caps.vector).toBe(false);
    });
});

describe('Wire-format contract: MVCC endpoints', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('GET /kv/:key?revision=true — snake_case KV wire → camelCase MVCCKeyValue, base64 key/value decoded to plaintext', async () => {
        const client = await makeConnectedClient(mvccKVWire);
        const kv: MVCCKeyValue = await client.mvcc.getRevision('hello');

        // Go marshals MVCCKeyValue.Key/Value ([]byte) as base64
        // ("aGVsbG8=" === base64("hello"), "d29ybGQ=" === base64("world")).
        // The SDK decodes both to plaintext before returning, matching
        // kv.get/kv.scan.
        expect(kv.key).toBe('hello');
        expect(kv.value).toBe('world');
        expect(kv.createRevision).toBe(5);
        expect(kv.modRevision).toBe(10);
        expect(kv.version).toBe(3);
        expect(kv.leaseId).toBe(0);
    });

    it('range response — snake_case kvs array → camelCase MVCCKeyValue[], base64 key/value decoded to plaintext', async () => {
        const wire = { kvs: [mvccKVWire], count: 1 };
        const client = await makeConnectedClient(wire);
        const resp = await client.mvcc.range('hello', 'hellz', {});

        expect(resp.count).toBe(1);
        const kv = resp.kvs[0];
        expect(kv.key).toBe('hello');
        expect(kv.value).toBe('world');
        expect(kv.createRevision).toBe(5);
        expect(kv.modRevision).toBe(10);
        expect(kv.leaseId).toBe(0);
    });
});

describe('Wire-format contract: lease endpoint', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('GET /lease/list — snake_case wire → camelCase LeaseInfo', async () => {
        const client = await makeConnectedClient(leaseListWire);
        const leases: readonly LeaseInfo[] = await client.lease.list();

        expect(leases).toHaveLength(1);
        const lease = leases[0];
        expect(lease.id).toBeDefined();
        expect(lease.ttl).toBe(25);
        // These were the broken fields: undefined before the fix.
        expect(lease.grantedTtl).toBe(30);
        expect(lease.remainingTtl).toBe(25);
    });
});

describe('Wire-format contract: watch SSE stream', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('SSE WatchResponse — snake_case wire → camelCase WatchResponse', async () => {
        // consumeSSE applies deepSnakeToCamel before calling the callback.
        // Simulate that path directly using the exported transformer.
        const { deepSnakeToCamel } = await import('../src/transform.js');

        const transformed = deepSnakeToCamel(watchResponseWire) as WatchResponse;

        expect(transformed.watchId).toBe(42);
        expect(transformed.canceled).toBe(false);
        expect(transformed.cancelReason).toBe('');
        expect(transformed.revision).toBe(1);

        expect(transformed.events).toHaveLength(1);
        const evt = transformed.events[0];
        // type starts as string "PUT" — normalizeWatchEventTypes in consumeSSE
        // converts it to the numeric EventType. The kv fields are the focus here.
        const kv = evt.kv as MVCCKeyValue;
        expect(kv.createRevision).toBe(1);
        expect(kv.modRevision).toBe(1);
        expect(kv.leaseId).toBe(0);
    });

    it('consumeSSE delivers base64 kv.key/kv.value to the callback as plaintext', async () => {
        // End-to-end: exercise the real SSE consumption loop (not just
        // deepSnakeToCamel in isolation) so a regression in consumeSSE's
        // base64 decoding step is actually caught.

        // First: mock for the connect() call (health check + status).
        vi.stubGlobal(
            'fetch',
            async (): Promise<Response> =>
                new Response(
                    JSON.stringify({ status: 'ok', is_leader: true }),
                    { status: 200, headers: { 'Content-Type': 'application/json' } },
                ),
        );

        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        // Now: install a deferred mock for the GET /watch SSE call. Resolving
        // it only after onEvent() is registered below avoids a race between
        // callback registration and the background SSE consumption loop
        // (createSSEWatchHandle fires consumeSSE without awaiting it).
        let resolveFetch!: (resp: Response) => void;
        const deferredFetch = new Promise<Response>((resolve) => {
            resolveFetch = resolve;
        });
        vi.stubGlobal('fetch', async (): Promise<Response> => deferredFetch);

        const handle = await client.watch.watch('hello', {});

        const received: WatchEvent[] = [];
        const delivered = new Promise<void>((resolve) => {
            handle.onEvent((events) => {
                received.push(...events);
                resolve();
            });
        });

        const sseFrame = `data: ${JSON.stringify(watchResponseWire)}\n\n`;
        resolveFetch(
            new Response(sseFrame, {
                status: 200,
                headers: { 'Content-Type': 'text/event-stream' },
            }),
        );

        await delivered;
        await handle.cancel();

        expect(received).toHaveLength(1);
        // watchResponseWire's kv is { key: "aGVsbG8=", value: "d29ybGQ=" }
        // ("hello"/"world" base64). consumeSSE must decode both to plaintext
        // before invoking onEvent callbacks — this is what the GUI's
        // WatchView (cmd/qrdb/frontend/src/views/user/WatchView.svelte)
        // renders directly.
        expect(received[0].kv.key).toBe('hello');
        expect(received[0].kv.value).toBe('world');
    });
});

describe('Wire-format contract: connect() reads nodeId from status', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('connection.getVersion() returns the status build version as server version after connect', async () => {
        // The connect() method reads /health then /status and stores status.version,
        // the server's build version (not its node id).
        let callCount = 0;
        vi.stubGlobal(
            'fetch',
            async (): Promise<Response> => {
                callCount++;
                if (callCount === 1) {
                    // /health response
                    return new Response(
                        JSON.stringify({ status: 'ok', is_leader: true }),
                        { status: 200, headers: { 'Content-Type': 'application/json' } },
                    );
                }
                // /status response
                return new Response(
                    JSON.stringify(statusWire),
                    { status: 200, headers: { 'Content-Type': 'application/json' } },
                );
            },
        );

        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        const version = client.connection.getVersion();
        expect(version['server']).toBe('1.2.3-test');
    });
});
