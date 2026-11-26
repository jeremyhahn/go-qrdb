/**
 * REST contract for the vector maintenance calls: index rebuild and
 * namespace compaction.
 *
 * The response bodies below are the exact JSON the Go handlers emit
 * (pkg/api/rest/handlers_vector.go: vectorRebuildIndexResponse,
 * vectorRebuildProgressResponse, vectorCompactResponse,
 * vectorCompactionProgressResponse); the request side checks the paths,
 * the snake_case body fields and the query parameters those handlers read
 * (job_id for rebuild progress, operation_id for compaction progress).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

interface CapturedRequest {
    url: string;
    method: string;
    body: unknown;
}

/** Stubs fetch with one JSON answer and records every request. */
function stubFetch(responseBody: unknown): CapturedRequest[] {
    const calls: CapturedRequest[] = [];
    vi.stubGlobal(
        'fetch',
        async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
            const url = typeof input === 'string' ? input : (input as URL).toString();
            let body: unknown = undefined;
            if (typeof init?.body === 'string') body = JSON.parse(init.body);
            calls.push({ url, method: init?.method ?? 'GET', body });
            return new Response(JSON.stringify(responseBody), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        },
    );
    return calls;
}

async function connectedClient(): Promise<RestClient> {
    stubFetch({ status: 'ok' });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

function vectorOf(client: RestClient) {
    const v = client.vector;
    if (v === null) throw new Error('vector client unavailable');
    return v;
}

describe('vector index rebuild — REST wire', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('rebuildIndex POSTs snake_case fields and maps job_id/success/timestamp', async () => {
        const client = await connectedClient();
        const calls = stubFetch({
            job_id: 'rb-1',
            status: 'running',
            vectors_processed: 0,
            total_vectors: 100,
            percent_complete: 0,
            estimated_time_remaining: 0,
            timestamp: 1700000000,
            success: true,
        });

        const resp = await vectorOf(client).rebuildIndex({ namespace: 'docs', shardId: 3 });

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/vector/index/rebuild');
        expect(calls[0].method).toBe('POST');
        expect(calls[0].body).toEqual({ namespace: 'docs', shard_id: 3 });
        expect(resp).toEqual({ jobId: 'rb-1', started: true, timestamp: 1700000000 });
    });

    it('getRebuildProgress sends job_id and maps a running job', async () => {
        const client = await connectedClient();
        const calls = stubFetch({
            job_id: 'rb-1',
            namespace: 'docs',
            status: 'running',
            vectors_processed: 25,
            total_vectors: 100,
            percent_complete: 25,
            elapsed_time_ms: 10,
            estimated_time_remaining_ms: 30,
            started_at: 1700000000,
            completed_at: 0,
        });

        const p = await vectorOf(client).getRebuildProgress({ namespace: 'docs', jobId: 'rb-1', shardId: 2 });

        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/vector/index/rebuild/progress');
        expect(url.searchParams.get('namespace')).toBe('docs');
        expect(url.searchParams.get('job_id')).toBe('rb-1');
        expect(url.searchParams.get('shard_id')).toBe('2');
        expect(url.searchParams.get('jobId')).toBeNull();
        expect(p).toEqual({
            jobId: 'rb-1',
            status: 'running',
            progress: 0.25,
            startedAt: 1700000000,
            finishedAt: undefined,
            error: undefined,
        });
    });

    it('getRebuildProgress maps a completed job', async () => {
        const client = await connectedClient();
        stubFetch({
            job_id: 'rb-1',
            namespace: 'docs',
            status: 'completed',
            vectors_processed: 100,
            total_vectors: 100,
            percent_complete: 100,
            elapsed_time_ms: 40,
            estimated_time_remaining_ms: 0,
            started_at: 1700000000,
            completed_at: 1700000040,
        });

        const p = await vectorOf(client).getRebuildProgress({ namespace: 'docs', jobId: 'rb-1' });

        expect(p.status).toBe('completed');
        expect(p.progress).toBe(1);
        expect(p.finishedAt).toBe(1700000040);
        expect(p.error).toBeUndefined();
    });

    it('getRebuildProgress maps a failed job with its error', async () => {
        const client = await connectedClient();
        stubFetch({
            job_id: 'rb-1',
            namespace: 'docs',
            status: 'failed',
            vectors_processed: 10,
            total_vectors: 100,
            percent_complete: 10,
            elapsed_time_ms: 5,
            estimated_time_remaining_ms: 0,
            started_at: 1700000000,
            completed_at: 1700000005,
            error: 'disk full',
        });

        const p = await vectorOf(client).getRebuildProgress({ namespace: 'docs', jobId: 'rb-1' });

        expect(p.status).toBe('failed');
        expect(p.progress).toBeCloseTo(0.1);
        expect(p.finishedAt).toBe(1700000005);
        expect(p.error).toBe('disk full');
    });
});

describe('vector namespace compaction — REST wire', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('compactNamespace POSTs snake_case fields and maps operation_id/success', async () => {
        const client = await connectedClient();
        const calls = stubFetch({
            operation_id: 'cp-9',
            started_at: 1700000000,
            bytes_compacted: 0,
            success: true,
        });

        const resp = await vectorOf(client).compactNamespace({ namespace: 'docs', shardId: 1 });

        expect(calls).toHaveLength(1);
        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/vector/compact');
        expect(calls[0].method).toBe('POST');
        expect(calls[0].body).toEqual({ namespace: 'docs', shard_id: 1 });
        expect(resp).toEqual({ jobId: 'cp-9', started: true });
    });

    it('getCompactionProgress sends operation_id, not job_id, and maps a running job', async () => {
        const client = await connectedClient();
        const calls = stubFetch({
            operation_id: 'cp-9',
            namespace: 'docs',
            bytes_compacted: 512,
            total_bytes: 1024,
            percent_complete: 50,
            in_progress: true,
            started_at: 1700000000,
        });

        const p = await vectorOf(client).getCompactionProgress({ namespace: 'docs', jobId: 'cp-9', shardId: 4 });

        const url = new URL(calls[0].url);
        expect(url.pathname).toBe('/vector/compact/progress');
        expect(url.searchParams.get('namespace')).toBe('docs');
        expect(url.searchParams.get('operation_id')).toBe('cp-9');
        expect(url.searchParams.get('shard_id')).toBe('4');
        expect(url.searchParams.get('job_id')).toBeNull();
        expect(url.searchParams.get('jobId')).toBeNull();
        expect(p).toEqual({
            jobId: 'cp-9',
            status: 'running',
            progress: 0.5,
            startedAt: 1700000000,
            finishedAt: undefined,
            error: undefined,
        });
    });

    it('getCompactionProgress infers completed when not in progress and no error', async () => {
        const client = await connectedClient();
        stubFetch({
            operation_id: 'cp-9',
            namespace: 'docs',
            bytes_compacted: 1024,
            total_bytes: 1024,
            percent_complete: 100,
            in_progress: false,
            started_at: 1700000000,
            completed_at: 1700000020,
        });

        const p = await vectorOf(client).getCompactionProgress({ namespace: 'docs', jobId: 'cp-9' });

        expect(p.status).toBe('completed');
        expect(p.progress).toBe(1);
        expect(p.finishedAt).toBe(1700000020);
        expect(p.error).toBeUndefined();
    });

    it('getCompactionProgress infers failed from the error field', async () => {
        const client = await connectedClient();
        stubFetch({
            operation_id: 'cp-9',
            namespace: 'docs',
            bytes_compacted: 0,
            total_bytes: 1024,
            percent_complete: 0,
            in_progress: false,
            started_at: 1700000000,
            completed_at: 1700000001,
            error: 'namespace locked',
        });

        const p = await vectorOf(client).getCompactionProgress({ namespace: 'docs', jobId: 'cp-9' });

        expect(p.jobId).toBe('cp-9');
        expect(p.status).toBe('failed');
        expect(p.progress).toBe(0);
        expect(p.error).toBe('namespace locked');
    });
});
