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
