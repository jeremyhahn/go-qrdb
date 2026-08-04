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
 * Vector enum wire-format tests: the SDK's public vector API uses numeric
 * proto enums (DistanceMetric / VectorIndexType) while the Go REST API
 * carries them as strings. These tests pin the REST transport's enum
 * adaptation in both directions:
 *
 *   - requests: numeric enums serialize to the canonical service tokens
 *     ("COSINE"/"L2"/"DOT_PRODUCT", "HNSW"/"FLAT"/"IVF") derived from
 *     pkg/api/grpc/service_vector.go;
 *   - responses: wire strings (any accepted casing/alias) parse back to
 *     numeric enums.
 *
 * Regression protection for the web-UI bug where createNamespace sent
 * {"distance_metric":0} and the Go handler rejected it with HTTP 400.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import {
    distanceMetricToRestWire,
    indexTypeToRestWire,
    distanceMetricToIpcWire,
    indexTypeToIpcWire,
    wireToDistanceMetric,
    wireToIndexType,
    vectorIDToWire,
    vectorIDFromWire,
} from '../src/transform.js';

// ---------------------------------------------------------------------------
// Fetch mock helpers
// ---------------------------------------------------------------------------

/** Recorded details of a single fetch call. */
interface CapturedRequest {
    url: string;
    method: string;
    body: unknown;
}

/**
 * Installs a global mock fetch that records every call and responds with the
 * provided JSON body (status 200). Returns an array that accumulates records.
 */
function installMockFetch(responseBody: unknown = {}): CapturedRequest[] {
    const calls: CapturedRequest[] = [];

    vi.stubGlobal(
        'fetch',
        async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            const method = init?.method ?? 'GET';
            let body: unknown = undefined;
            if (init?.body && typeof init.body === 'string') {
                try {
                    body = JSON.parse(init.body);
                } catch {
                    body = init.body;
                }
            }
            calls.push({ url, method, body });

            return new Response(JSON.stringify(responseBody), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );

    return calls;
}

/** Creates a RestClient already in the "connected" state. */
async function connectedClient(): Promise<RestClient> {
    installMockFetch({ status: 'ok' });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

// ---------------------------------------------------------------------------
// Request direction: numeric enums → REST wire strings
// ---------------------------------------------------------------------------

describe('vector createNamespace request wire format', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('serializes distance_metric and index_type as canonical service strings', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            namespace: 'docs',
            dimension: 4,
            timestamp: 1,
            success: true,
        });

        await client.vector!.createNamespace({
            namespace: 'docs',
            dimension: 4,
            distanceMetric: 0,
            indexType: 0,
        });

        expect(calls).toHaveLength(1);
        const req = calls[0];
        expect(req.url).toBe('https://localhost:63002/vector/namespace');
        expect(req.method).toBe('POST');

        const body = req.body as Record<string, unknown>;
        expect(body.namespace).toBe('docs');
        expect(body.dimension).toBe(4);
        expect(body.distance_metric).toBe('COSINE');
        expect(body.index_type).toBe('HNSW');
        // Numbers must never reach the wire: the Go handler decodes these
        // fields into `string` and rejects JSON numbers with HTTP 400.
        expect(typeof body.distance_metric).toBe('string');
        expect(typeof body.index_type).toBe('string');
    });

    it('maps every DistanceMetric and VectorIndexType value to its wire token', async () => {
        const cases: Array<{
            metric: 0 | 1 | 2;
            index: 0 | 1 | 2;
            wireMetric: string;
            wireIndex: string;
        }> = [
            { metric: 0, index: 0, wireMetric: 'COSINE', wireIndex: 'HNSW' },
            { metric: 1, index: 1, wireMetric: 'L2', wireIndex: 'FLAT' },
            { metric: 2, index: 2, wireMetric: 'DOT_PRODUCT', wireIndex: 'IVF' },
        ];

        for (const c of cases) {
            vi.unstubAllGlobals();
            const client = await connectedClient();
            const calls = installMockFetch({ success: true });

            await client.vector!.createNamespace({
                namespace: 'ns',
                dimension: 8,
                distanceMetric: c.metric,
                indexType: c.index,
            });

            const body = calls[0].body as Record<string, unknown>;
            expect(body.distance_metric).toBe(c.wireMetric);
            expect(body.index_type).toBe(c.wireIndex);
        }
    });

    it('sends index params under index_config', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ success: true });

        await client.vector!.createNamespace({
            namespace: 'ns',
            dimension: 8,
            distanceMetric: 1,
            indexType: 0,
            indexParams: { m: '16' },
        });

        const body = calls[0].body as Record<string, unknown>;
        expect(body.index_config).toEqual({ m: '16' });
        expect(body.index_params).toBeUndefined();
    });
});

// ---------------------------------------------------------------------------
// Response direction: wire strings → numeric enums
// ---------------------------------------------------------------------------

describe('vector response enum parsing', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('parses getNamespaceInfo wire strings back to numeric enums', async () => {
        const client = await connectedClient();
        installMockFetch({
            namespace: 'docs',
            dimension: 4,
            vector_count: 2,
            distance_metric: 'COSINE',
            index_type: 'HNSW',
            created_at: 1,
            updated_at: 2,
        });

        const info = await client.vector!.getNamespaceInfo({ namespace: 'docs' });
        expect(info.distanceMetric).toBe(0);
        expect(info.indexType).toBe(0);
    });

    it('parses lowercase and alias wire tokens case-insensitively', async () => {
        const client = await connectedClient();
        installMockFetch({
            namespace: 'docs',
            dimension: 4,
            distance_metric: 'cosine',
            index_type: 'ivf_flat',
        });

        const info = await client.vector!.getNamespaceInfo({ namespace: 'docs' });
        expect(info.distanceMetric).toBe(0);
        expect(info.indexType).toBe(2);
    });

    it('converts enum strings inside listNamespaces info entries', async () => {
        const client = await connectedClient();
        installMockFetch({
            namespaces: [
                {
                    namespace: 'a',
                    dimension: 4,
                    distance_metric: 'DOT_PRODUCT',
                    index_type: 'FLAT',
                },
            ],
            total_count: 1,
            timestamp: 3,
        });

        const resp = await client.vector!.listNamespaces({});
        const entry = resp.namespaces[0] as unknown as Record<string, unknown>;
        expect(entry.namespace).toBe('a');
        expect(entry.distanceMetric).toBe(2);
        expect(entry.indexType).toBe(1);
    });

    it('converts index_type in getIndexStats responses', async () => {
        const client = await connectedClient();
        installMockFetch({
            namespace: 'docs',
            index_type: 'IVF',
            vector_count: 10,
            dimension: 4,
            index_size_bytes: 100,
            memory_usage_bytes: 200,
            timestamp: 4,
        });

        const stats = await client.vector!.getIndexStats({ namespace: 'docs' });
        expect(stats.indexType).toBe(2);
    });
});

// ---------------------------------------------------------------------------
// Codec unit coverage
// ---------------------------------------------------------------------------

describe('vector enum codecs', () => {
    it('maps numeric enums to REST wire tokens (pkg/api/grpc/service_vector.go canon)', () => {
        expect(distanceMetricToRestWire(0)).toBe('COSINE');
        expect(distanceMetricToRestWire(1)).toBe('L2');
        expect(distanceMetricToRestWire(2)).toBe('DOT_PRODUCT');
        expect(indexTypeToRestWire(0)).toBe('HNSW');
        expect(indexTypeToRestWire(1)).toBe('FLAT');
        expect(indexTypeToRestWire(2)).toBe('IVF');
    });

    it('maps numeric enums to Wails IPC tokens (sdk/go/transport/vector_types.go canon)', () => {
        expect(distanceMetricToIpcWire(0)).toBe('cosine');
        expect(distanceMetricToIpcWire(1)).toBe('euclidean');
        expect(distanceMetricToIpcWire(2)).toBe('dot_product');
        expect(indexTypeToIpcWire(0)).toBe('hnsw');
        expect(indexTypeToIpcWire(1)).toBe('flat');
        expect(indexTypeToIpcWire(2)).toBe('ivf_flat');
    });

    it('parses wire strings case-insensitively with aliases', () => {
        expect(wireToDistanceMetric('COSINE')).toBe(0);
        expect(wireToDistanceMetric('cosine')).toBe(0);
        expect(wireToDistanceMetric('L2')).toBe(1);
        expect(wireToDistanceMetric('euclidean')).toBe(1);
        expect(wireToDistanceMetric('EUCLIDEAN')).toBe(1);
        expect(wireToDistanceMetric('dot_product')).toBe(2);
        expect(wireToDistanceMetric('DOT_PRODUCT')).toBe(2);
        expect(wireToIndexType('HNSW')).toBe(0);
        expect(wireToIndexType('hnsw')).toBe(0);
        expect(wireToIndexType('FLAT')).toBe(1);
        expect(wireToIndexType('ivf')).toBe(2);
        expect(wireToIndexType('IVF_FLAT')).toBe(2);
    });

    it('falls back to server defaults for unknown values (L2, HNSW)', () => {
        expect(wireToDistanceMetric('bogus')).toBe(1);
        expect(wireToDistanceMetric(undefined)).toBe(1);
        expect(wireToIndexType('bogus')).toBe(0);
        expect(wireToIndexType(undefined)).toBe(0);
    });

    it('passes through numeric values idempotently', () => {
        expect(wireToDistanceMetric(2)).toBe(2);
        expect(wireToDistanceMetric(7)).toBe(1);
        expect(wireToIndexType(1)).toBe(1);
        expect(wireToIndexType(9)).toBe(0);
    });
});

// ---------------------------------------------------------------------------
// Vector ID wire format
// ---------------------------------------------------------------------------

describe('vector ID wire format', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('converts SDK string IDs to numbers on JSON request bodies', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ id: 41, version: 1, timestamp: 2, success: true });

        await client.vector!.upsertVector({
            namespace: 'docs',
            id: '41',
            vector: [0.1, 0.2],
        });

        const body = calls[0].body as Record<string, unknown>;
        // The Go handlers decode `id` into uint64; a JSON string would be
        // rejected with HTTP 400.
        expect(body.id).toBe(41);
        expect(typeof body.id).toBe('number');
    });

    it('converts searchByID request IDs and response result IDs', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({
            results: [{ id: 41, score: 0.9, distance: 0.1, rank: 1 }],
            timestamp: 2,
            query_duration: 3,
        });

        const resp = await client.vector!.searchByID({ namespace: 'docs', id: '41', k: 5 });

        const body = calls[0].body as Record<string, unknown>;
        expect(body.id).toBe(41);
        expect(resp.results[0].id).toBe('41');
    });

    it('passes non-numeric vector IDs through for authoritative server rejection', async () => {
        const client = await connectedClient();
        const calls = installMockFetch({ success: true });

        await client.vector!.upsertVector({ namespace: 'docs', id: 'vec-alpha', vector: [1] });

        const body = calls[0].body as Record<string, unknown>;
        expect(body.id).toBe('vec-alpha');
    });

    it('codec converts decimal strings and stringifies wire numbers', () => {
        expect(vectorIDToWire('0')).toBe(0);
        expect(vectorIDToWire('123')).toBe(123);
        expect(vectorIDToWire('abc')).toBe('abc');
        expect(vectorIDToWire('-1')).toBe('-1');
        expect(vectorIDToWire('')).toBe('');
        expect(vectorIDFromWire(41)).toBe('41');
        expect(vectorIDFromWire('41')).toBe('41');
    });
});
