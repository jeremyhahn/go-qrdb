// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Wire-name tests for the vector index_config, rebuild and compaction
 * routes (#1478): the field names and types are those of the Go REST
 * handlers in pkg/api/rest/handlers_vector.go.
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { RestClient } from '../src/rest.js';

interface Captured {
    url: string;
    body: unknown;
}

function mockFetch(responseBody: unknown): Captured[] {
    const calls: Captured[] = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
        calls.push({ url, body });
        return new Response(JSON.stringify(responseBody), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
        });
    });
    return calls;
}

async function client(): Promise<RestClient> {
    mockFetch({ status: 'ok' });
    const c = new RestClient('https://localhost:63002');
    await c.connection.connect('https://localhost:63002');
    return c;
}

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('vector index_config wire type', () => {
    it('sends numeric and boolean index params as strings (index_config is map[string]string)', async () => {
        const c = await client();
        const calls = mockFetch({ success: true });
        await c.vector!.createNamespace({
            namespace: 'ns',
            dimension: 3,
            distanceMetric: 0,
            indexType: 1,
            indexParams: { m: 8, ef_construction: 64, normalize: true, label: 'x' },
        });
        expect((calls[0].body as Record<string, unknown>).index_config).toEqual({
            m: '8',
            ef_construction: '64',
            normalize: 'true',
            label: 'x',
        });
    });
});

describe('vector rebuild wire names', () => {
    it('reads success as started', async () => {
        const c = await client();
        mockFetch({ job_id: 'r1', status: 'running', timestamp: 5, success: true });
        expect(await c.vector!.rebuildIndex({ namespace: 'ns' })).toEqual({
            jobId: 'r1',
            started: true,
            timestamp: 5,
        });
    });

    it('reads percent_complete and completed_at from the progress route', async () => {
        const c = await client();
        const calls = mockFetch({
            job_id: 'r1',
            namespace: 'ns',
            status: 'completed',
            percent_complete: 100,
            started_at: 10,
            completed_at: 20,
        });
        const p = await c.vector!.getRebuildProgress({ namespace: 'ns', jobId: 'r1' });
        expect(calls[0].url).toContain('job_id=r1');
        expect(p).toEqual({ jobId: 'r1', status: 'completed', progress: 1, startedAt: 10, finishedAt: 20, error: undefined });
    });
});

describe('vector compaction wire names', () => {
    it('reads operation_id as jobId and success as started', async () => {
        const c = await client();
        mockFetch({ operation_id: 'c1', started_at: 1, bytes_compacted: 0, success: true });
        expect(await c.vector!.compactNamespace({ namespace: 'ns' })).toEqual({ jobId: 'c1', started: true });
    });

    it('sends the job id as operation_id and maps the progress fields', async () => {
        const c = await client();
        const calls = mockFetch({
            operation_id: 'c1',
            namespace: 'ns',
            percent_complete: 50,
            in_progress: true,
            started_at: 3,
        });
        const p = await c.vector!.getCompactionProgress({ namespace: 'ns', jobId: 'c1' });
        expect(calls[0].url).toContain('operation_id=c1');
        expect(calls[0].url).not.toContain('job_id');
        expect(p).toEqual({ jobId: 'c1', status: 'running', progress: 0.5, startedAt: 3, finishedAt: undefined, error: undefined });
    });

    it('reports a finished compaction as completed, a failed one as failed', async () => {
        const c = await client();
        mockFetch({ operation_id: 'c1', percent_complete: 100, in_progress: false, started_at: 3, completed_at: 9 });
        const done = await c.vector!.getCompactionProgress({ namespace: 'ns', jobId: 'c1' });
        expect(done.status).toBe('completed');
        expect(done.finishedAt).toBe(9);
        mockFetch({ operation_id: 'c1', in_progress: false, error: 'boom' });
        const failed = await c.vector!.getCompactionProgress({ namespace: 'ns', jobId: 'c1' });
        expect(failed.status).toBe('failed');
        expect(failed.error).toBe('boom');
    });
});

describe('vector index stats wire names', () => {
    it('reads memory_usage_bytes and index_size_bytes', async () => {
        const c = await client();
        mockFetch({
            namespace: 'ns',
            index_type: 'hnsw',
            vector_count: 4,
            dimension: 3,
            index_size_bytes: 2048,
            memory_usage_bytes: 1024,
            timestamp: 7,
        });
        const s = await c.vector!.getIndexStats({ namespace: 'ns' });
        expect(s.vectorCount).toBe(4);
        expect(s.dimension).toBe(3);
        expect(s.memoryUsage).toBe(1024);
        expect(s.diskUsage).toBe(2048);
        expect(s).not.toHaveProperty('memoryUsageBytes');
        expect(s).not.toHaveProperty('indexSizeBytes');
    });
});

describe('vector search and list wire names', () => {
    it('sends the searchSimilar query vector as query_vector and reads query_duration as took', async () => {
        const c = await client();
        const calls = mockFetch({ results: [{ id: 7, score: 1 }], timestamp: 1, query_duration: 42 });
        const r = await c.vector!.searchSimilar({ namespace: 'ns', vector: [1, 0], k: 1, efSearch: 16, minScore: 0.5 });
        const body = calls[0].body as Record<string, unknown>;
        expect(body.query_vector).toEqual([1, 0]);
        expect(body).not.toHaveProperty('vector');
        expect(body.ef_search).toBe(16);
        expect(body.min_score).toBe(0.5);
        expect(r.took).toBe(42);
        expect(r.results[0].id).toBe('7');
    });

    it('reads took from hybridSearch and searchByID', async () => {
        const c = await client();
        mockFetch({ results: [], query_duration: 9 });
        const h = await c.vector!.hybridSearch({
            namespace: 'ns', vector: [1], metadataFilters: {}, k: 1, vectorWeight: 1, metadataWeight: 0,
        });
        expect(h.took).toBe(9);
        const b = await c.vector!.searchByID({ namespace: 'ns', id: '1', k: 1 });
        expect(b.took).toBe(9);
    });

    it('reads total_count as total for searchByMetadata and listVectors', async () => {
        const c = await client();
        mockFetch({ results: [{ id: 1, vector: [1] }], total_count: 5, timestamp: 1 });
        expect((await c.vector!.searchByMetadata({ namespace: 'ns', filters: {} })).total).toBe(5);
        mockFetch({ vectors: [{ id: 1, vector: [1] }], next_cursor: 'abc', total_count: 3, timestamp: 1 });
        const l = await c.vector!.listVectors({ namespace: 'ns', limit: 1 });
        expect(l.total).toBe(3);
        expect(l.nextCursor).toBe('abc');
        expect(l.vectors[0].id).toBe('1');
    });

    it('reads createNamespace success as created', async () => {
        const c = await client();
        mockFetch({ namespace: 'ns', dimension: 3, timestamp: 11, success: true });
        expect(await c.vector!.createNamespace({ namespace: 'ns', dimension: 3, distanceMetric: 0, indexType: 0 }))
            .toEqual({ created: true, timestamp: 11 });
    });
});

describe('vector upsert, stats metric and cursor paging (#1478)', () => {
    it('reads upsertVector created from the wire', async () => {
        const c = await client();
        mockFetch({ id: 9, version: 1, timestamp: 3, success: true, created: true });
        const r = await c.vector!.upsertVector({ namespace: 'ns', id: '9', vector: [1, 0] });
        expect(r.created).toBe(true);
        mockFetch({ id: 9, version: 1, timestamp: 4, success: true, created: false });
        expect((await c.vector!.upsertVector({ namespace: 'ns', id: '9', vector: [1, 0] })).created).toBe(false);
    });

    it('reads the index stats distance_metric as distanceMetric', async () => {
        const c = await client();
        mockFetch({ namespace: 'ns', index_type: 'hnsw', distance_metric: 'L2', vector_count: 1, dimension: 2 });
        const s = await c.vector!.getIndexStats({ namespace: 'ns' });
        expect(s.distanceMetric).toBe(1);
    });

    it('pages listVectors by cursor only, never sending offset', async () => {
        const c = await client();
        const calls = mockFetch({ vectors: [], total_count: 0 });
        await c.vector!.listVectors({ namespace: 'ns', limit: 2, cursor: 'next1' });
        expect(calls[0].url).toContain('cursor=next1');
        expect(calls[0].url).not.toContain('offset');
    });
});
