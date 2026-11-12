// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * The REST backup client returns GET /v1/backups/{id} and /v1/backups/list
 * as BackupInfo: the server sends the backup's duration as `duration_ms`
 * (milliseconds), which reaches the caller as `durationMs`.
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { RestClient } from '../src/index.js';
import type { BackupInfo } from '../src/index.js';

function stubServer(routes: Record<string, unknown>): void {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input)).pathname;
        if (path === '/health') {
            return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
        }
        if (path in routes) {
            return new Response(JSON.stringify(routes[path]), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        }
        return new Response('not found', { status: 404 });
    }));
}

async function connected(): Promise<RestClient> {
    const c = new RestClient('http://qrdb.test');
    await c.connection.connect('http://qrdb.test');
    return c;
}

// The JSON pkg/api/rest/handlers_backup.go backupResponse encodes.
const wire = {
    id: 'b-1',
    timestamp: '2026-11-12T05:23:50Z',
    size: 29280,
    shard_count: 1,
    status: 'complete',
    checksum: 'abc',
    destination: '/var/backups/b-1',
    compression: 'zstd',
    encryption: 'none',
    duration_ms: 8,
};

describe('REST backup client wire shape', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('get and list return the backup duration in milliseconds', async () => {
        stubServer({
            '/v1/backups/b-1': wire,
            '/v1/backups/list': { backups: [wire], total: 1 },
        });
        const c = await connected();
        const got: BackupInfo = await c.backups.get('b-1');
        expect(got.durationMs).toBe(8);
        expect(got.shardCount).toBe(1);
        expect(got).not.toHaveProperty('duration');
        const list = await c.backups.list();
        expect(list.backups[0].durationMs).toBe(8);
    });
});
