/**
 * Backup wire contract: the server's backup and restore responses carry the
 * duration as `duration_ms`, an integer count of milliseconds
 * (pkg/api/rest/handlers_backup.go backupResponse.DurationMs and
 * restoreResultResponse.DurationMs). The SDK surfaces it as `durationMs`.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import type { BackupInfo, RestoreResult } from '../src/types.js';

function jsonResponse(body: unknown): Response {
    return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
    });
}

async function connectedClient(): Promise<RestClient> {
    vi.stubGlobal('fetch', async () => jsonResponse({ status: 'ok' }));
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

describe('Backup duration wire contract (REST)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('backups.get reads duration_ms as durationMs', async () => {
        const client = await connectedClient();
        vi.stubGlobal('fetch', async () =>
            jsonResponse({
                id: 'b-1',
                timestamp: '2026-11-12T00:00:00Z',
                size: 10,
                shard_count: 1,
                status: 'completed',
                checksum: 'ab',
                destination: 'local:///tmp/b',
                compression: 'zstd',
                encryption: 'none',
                duration_ms: 1234,
            }),
        );
        const info: BackupInfo = await client.backups.get('b-1');
        expect(info.durationMs).toBe(1234);
        expect(info.shardCount).toBe(1);
    });

    it('backups.restore reads duration_ms as durationMs', async () => {
        const client = await connectedClient();
        vi.stubGlobal('fetch', async () =>
            jsonResponse({ backup_id: 'b-1', shards_restored: 2, duration_ms: 56 }),
        );
        const result: RestoreResult = await client.backups.restore({ backupId: 'b-1' });
        expect(result.durationMs).toBe(56);
        expect(result.shardsRestored).toBe(2);
    });
});
