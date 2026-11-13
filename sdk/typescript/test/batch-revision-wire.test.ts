/**
 * Batch wire contract: POST /batch answers the revision the batch's writes
 * committed at (pkg/service/batch_service.go BatchResponse.Revision), next to
 * the per-op responses. The SDK surfaces it as BatchResponse.revision.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';

function jsonResponse(body: unknown): Response {
    return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
    });
}

describe('Batch revision wire contract (REST)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('batch.execute surfaces the committed revision', async () => {
        vi.stubGlobal('fetch', async () => jsonResponse({ status: 'ok' }));
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');

        vi.stubGlobal('fetch', async () =>
            jsonResponse({ responses: [{ succeeded: true }], revision: 29 }),
        );
        const resp = await client.batch.execute([{ type: 0, key: 'k', value: 'v' }]);
        expect(resp.revision).toBe(29);
        expect(resp.responses[0].succeeded).toBe(true);
    });
});
