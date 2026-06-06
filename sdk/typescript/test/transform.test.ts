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
 * Unit tests for the snake↔camel key transformer in transform.ts.
 *
 * These tests verify that the transformer correctly handles all shapes that
 * appear in the QRDB REST wire format so that a regression in the transformer
 * would be caught before it reaches the SDK consumers.
 */

import { describe, it, expect } from 'vitest';
import {
    snakeToCamelKey,
    camelToSnakeKey,
    deepSnakeToCamel,
    deepCamelToSnake,
} from '../src/transform.js';

// ---------------------------------------------------------------------------
// snakeToCamelKey
// ---------------------------------------------------------------------------

describe('snakeToCamelKey', () => {
    it('converts a simple snake_case key', () => {
        expect(snakeToCamelKey('is_leader')).toBe('isLeader');
    });

    it('converts multi-segment keys', () => {
        expect(snakeToCamelKey('node_id')).toBe('nodeId');
        expect(snakeToCamelKey('raft_address')).toBe('raftAddress');
        expect(snakeToCamelKey('shard_count')).toBe('shardCount');
        expect(snakeToCamelKey('is_draining')).toBe('isDraining');
        expect(snakeToCamelKey('leader_id')).toBe('leaderId');
        expect(snakeToCamelKey('replica_id')).toBe('replicaId');
        expect(snakeToCamelKey('shard_id')).toBe('shardId');
        expect(snakeToCamelKey('create_revision')).toBe('createRevision');
        expect(snakeToCamelKey('mod_revision')).toBe('modRevision');
        expect(snakeToCamelKey('lease_id')).toBe('leaseId');
        expect(snakeToCamelKey('watch_id')).toBe('watchId');
        expect(snakeToCamelKey('cancel_reason')).toBe('cancelReason');
        expect(snakeToCamelKey('timestamp_ns')).toBe('timestampNs');
        expect(snakeToCamelKey('granted_ttl')).toBe('grantedTtl');
        expect(snakeToCamelKey('remaining_ttl')).toBe('remainingTtl');
    });

    it('preserves keys without underscores', () => {
        expect(snakeToCamelKey('status')).toBe('status');
        expect(snakeToCamelKey('kv')).toBe('kv');
        expect(snakeToCamelKey('mvcc')).toBe('mvcc');
        expect(snakeToCamelKey('id')).toBe('id');
        expect(snakeToCamelKey('ttl')).toBe('ttl');
    });

    it('preserves keys that are already camelCase (Go camelCase json tags)', () => {
        // Auth types use camelCase json tags: "roleIDs", "tenantID", etc.
        // These must pass through unchanged so we do not double-transform.
        expect(snakeToCamelKey('roleIDs')).toBe('roleIDs');
        expect(snakeToCamelKey('tenantID')).toBe('tenantID');
        expect(snakeToCamelKey('hasMore')).toBe('hasMore');
        expect(snakeToCamelKey('createdAt')).toBe('createdAt');
    });

    it('handles trailing underscores without transforming them', () => {
        // A trailing underscore has no following [a-z] so it is left alone.
        expect(snakeToCamelKey('end_')).toBe('end_');
    });

    it('handles digit-adjacent underscores', () => {
        // Keys like start_ns: only matches _[a-z], digits are not transformed.
        expect(snakeToCamelKey('start_ns')).toBe('startNs');
        expect(snakeToCamelKey('end_ns')).toBe('endNs');
        expect(snakeToCamelKey('interval_ns')).toBe('intervalNs');
    });
});

// ---------------------------------------------------------------------------
// camelToSnakeKey
// ---------------------------------------------------------------------------

describe('camelToSnakeKey', () => {
    it('converts camelCase keys to snake_case', () => {
        expect(camelToSnakeKey('isLeader')).toBe('is_leader');
        expect(camelToSnakeKey('nodeId')).toBe('node_id');
        expect(camelToSnakeKey('raftAddress')).toBe('raft_address');
        expect(camelToSnakeKey('shardCount')).toBe('shard_count');
        expect(camelToSnakeKey('leaderId')).toBe('leader_id');
    });

    it('preserves already snake_case keys', () => {
        expect(camelToSnakeKey('status')).toBe('status');
        expect(camelToSnakeKey('id')).toBe('id');
    });
});

// ---------------------------------------------------------------------------
// deepSnakeToCamel
// ---------------------------------------------------------------------------

describe('deepSnakeToCamel', () => {
    it('returns primitives unchanged', () => {
        expect(deepSnakeToCamel(42)).toBe(42);
        expect(deepSnakeToCamel('hello')).toBe('hello');
        expect(deepSnakeToCamel(true)).toBe(true);
        expect(deepSnakeToCamel(null)).toBeNull();
        expect(deepSnakeToCamel(undefined)).toBeUndefined();
    });

    it('transforms top-level object keys', () => {
        const input = { is_leader: true, node_id: 'n1', status: 'ok' };
        const out = deepSnakeToCamel(input) as Record<string, unknown>;
        expect(out['isLeader']).toBe(true);
        expect(out['nodeId']).toBe('n1');
        expect(out['status']).toBe('ok');
        // Original keys must not appear
        expect(out['is_leader']).toBeUndefined();
        expect(out['node_id']).toBeUndefined();
    });

    it('recursively transforms nested objects', () => {
        const input = {
            shards: [
                {
                    shard_id: 1,
                    leader_id: 1,
                    peers: [
                        { replica_id: 1, address: 'host:1', is_leader: true },
                    ],
                },
            ],
        };
        const out = deepSnakeToCamel(input) as {
            shards: Array<{
                shardId: number;
                leaderId: number;
                peers: Array<{ replicaId: number; address: string; isLeader: boolean }>;
            }>;
        };
        expect(out.shards[0].shardId).toBe(1);
        expect(out.shards[0].leaderId).toBe(1);
        expect(out.shards[0].peers[0].replicaId).toBe(1);
        expect(out.shards[0].peers[0].isLeader).toBe(true);
    });

    it('handles arrays of primitives without modification', () => {
        const input = { keys: ['a', 'b', 'c'] };
        const out = deepSnakeToCamel(input) as { keys: string[] };
        expect(out.keys).toEqual(['a', 'b', 'c']);
    });

    it('handles null values within objects', () => {
        const input = { some_field: null, another_field: 42 };
        const out = deepSnakeToCamel(input) as Record<string, unknown>;
        expect(out['someField']).toBeNull();
        expect(out['anotherField']).toBe(42);
    });

    it('handles empty object', () => {
        expect(deepSnakeToCamel({})).toEqual({});
    });

    it('handles empty array', () => {
        expect(deepSnakeToCamel([])).toEqual([]);
    });

    it('already-camelCase keys pass through without double-transformation', () => {
        // Auth types use Go camelCase json tags: roleIDs, tenantID, hasMore.
        const input = { roleIDs: ['r1'], tenantID: 't1', hasMore: false };
        const out = deepSnakeToCamel(input) as Record<string, unknown>;
        expect(out['roleIDs']).toEqual(['r1']);
        expect(out['tenantID']).toBe('t1');
        expect(out['hasMore']).toBe(false);
    });

    it('transforms deeply nested MVCC key-value', () => {
        const input = {
            kvs: [
                {
                    key: 'aGVsbG8=',
                    value: 'd29ybGQ=',
                    create_revision: 1,
                    mod_revision: 2,
                    version: 1,
                    lease_id: 0,
                },
            ],
            count: 1,
        };
        const out = deepSnakeToCamel(input) as {
            kvs: Array<{
                key: string;
                value: string;
                createRevision: number;
                modRevision: number;
                version: number;
                leaseId: number;
            }>;
            count: number;
        };
        expect(out.kvs[0].createRevision).toBe(1);
        expect(out.kvs[0].modRevision).toBe(2);
        expect(out.kvs[0].leaseId).toBe(0);
        expect(out.count).toBe(1);
    });
});

// ---------------------------------------------------------------------------
// deepCamelToSnake
// ---------------------------------------------------------------------------

describe('deepCamelToSnake', () => {
    it('converts top-level camelCase keys to snake_case', () => {
        const input = { shardId: 1, replicaId: 2, address: 'host:1' };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['shard_id']).toBe(1);
        expect(out['replica_id']).toBe(2);
        expect(out['address']).toBe('host:1');
    });

    it('round-trips snake→camel→snake', () => {
        const original = { is_leader: true, node_id: 'n1', shard_count: 3 };
        const camel = deepSnakeToCamel(original);
        const backToSnake = deepCamelToSnake(camel) as Record<string, unknown>;
        expect(backToSnake['is_leader']).toBe(true);
        expect(backToSnake['node_id']).toBe('n1');
        expect(backToSnake['shard_count']).toBe(3);
    });
});

// ---------------------------------------------------------------------------
// TestJsonBody_TransformsCamelToSnake
// Verifies that outbound request bodies are converted from camelCase to
// snake_case before serialization, matching Go's json:"snake_case" struct tags.
// ---------------------------------------------------------------------------

describe('TestJsonBody_TransformsCamelToSnake', () => {
    it('converts isolationType to isolation_type for tenant create', () => {
        const input = { id: 't1', name: 'Test', isolationType: 'prefix' };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['isolation_type']).toBe('prefix');
        expect(out['id']).toBe('t1');
        expect(out['name']).toBe('Test');
        expect(out['isolationType']).toBeUndefined();
    });

    it('converts successOps/failureOps to snake_case for transactions', () => {
        const input = {
            compares: [],
            successOps: [{ type: 'put', key: 'k', leaseId: 7, rangeEnd: 'z' }],
            failureOps: [],
        };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(Object.keys(out)).toContain('success_ops');
        expect(Object.keys(out)).toContain('failure_ops');
        expect(Object.keys(out)).not.toContain('successOps');
        expect(Object.keys(out)).not.toContain('failureOps');

        const ops = out['success_ops'] as Array<Record<string, unknown>>;
        expect(ops[0]['lease_id']).toBe(7);
        expect(ops[0]['range_end']).toBe('z');
        expect(ops[0]['leaseId']).toBeUndefined();
        expect(ops[0]['rangeEnd']).toBeUndefined();
    });

    it('converts tenantId to tenant_id for barrier requests', () => {
        const input = { tenantId: 'abc', threshold: 2, shares: 3 };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['tenant_id']).toBe('abc');
        expect(out['tenantId']).toBeUndefined();
    });

    it('converts maxDatabases to max_databases for quota requests', () => {
        const input = { maxDatabases: 10, maxKeys: 1000, maxStorageBytes: 5000 };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['max_databases']).toBe(10);
        expect(out['max_keys']).toBe(1000);
        expect(out['max_storage_bytes']).toBe(5000);
    });
});

// ---------------------------------------------------------------------------
// TestJsonBody_PreservesExistingSnakeKeys
// Verifies that keys already in snake_case are not double-transformed.
// Critical for membership endpoints that hand-write snake_case payloads.
// ---------------------------------------------------------------------------

describe('TestJsonBody_PreservesExistingSnakeKeys', () => {
    it('shard_id remains shard_id after deepCamelToSnake', () => {
        const input = { shard_id: 1, replica_id: 2, address: 'host:1' };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['shard_id']).toBe(1);
        expect(out['replica_id']).toBe(2);
        expect(out['address']).toBe('host:1');
    });

    it('already snake_case keys do not gain extra underscores', () => {
        // Ensure camelToSnakeKey does not corrupt snake keys like is_leader.
        const input = { is_leader: true, node_id: 'n1', shard_count: 3 };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['is_leader']).toBe(true);
        expect(out['node_id']).toBe('n1');
        expect(out['shard_count']).toBe(3);
    });

    it('mixed object with both camel and snake keys transforms only camel', () => {
        // shard_id stays, shardCount becomes shard_count
        const input = { shard_id: 1, shardCount: 3 };
        const out = deepCamelToSnake(input) as Record<string, unknown>;
        expect(out['shard_id']).toBe(1);
        expect(out['shard_count']).toBe(3);
        expect(out['shardCount']).toBeUndefined();
    });
});

// ---------------------------------------------------------------------------
// Round-trip invariant: deepSnakeToCamel(deepCamelToSnake(camelObj)) == camelObj
// ---------------------------------------------------------------------------

describe('TestJsonBody_RoundTrip camel→snake→camel', () => {
    it('round-trips a CreateTenantRequest shape', () => {
        const original = { id: 't1', name: 'Test Tenant', isolationType: 'prefix' };
        const snake = deepCamelToSnake(original);
        const backToCamel = deepSnakeToCamel(snake) as Record<string, unknown>;
        expect(backToCamel['id']).toBe('t1');
        expect(backToCamel['name']).toBe('Test Tenant');
        expect(backToCamel['isolationType']).toBe('prefix');
    });

    it('round-trips a transaction Op shape', () => {
        const original = {
            compares: [{ key: 'k', target: 0, result: 0, createRevision: 5 }],
            successOps: [{ type: 'put', key: 'x', leaseId: 99 }],
            failureOps: [],
        };
        const snake = deepCamelToSnake(original);
        const backToCamel = deepSnakeToCamel(snake) as {
            compares: Array<Record<string, unknown>>;
            successOps: Array<Record<string, unknown>>;
            failureOps: unknown[];
        };
        expect(backToCamel.compares[0]['createRevision']).toBe(5);
        expect(backToCamel.successOps[0]['leaseId']).toBe(99);
        expect(backToCamel.failureOps).toHaveLength(0);
    });
});
