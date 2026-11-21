/**
 * Audit query wire contract: AuditFilter.from/to travel unchanged as the
 * `from`/`to` query parameters of GET /v1/audit/events. The server bounds the
 * range [from, to): pkg/audit matchesFilter drops an event before `from` and
 * any event not strictly before `to`, so the SDK must not shift either bound.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AuditClient } from '../src/audit.js';

describe('Audit time range wire contract', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('query sends from and to verbatim', async () => {
        let seen = '';
        vi.stubGlobal('fetch', async (url: string) => {
            seen = url;
            return new Response(JSON.stringify({ events: [], total: 0 }), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        });
        const client = new AuditClient('https://localhost:63002/');
        await client.query({
            from: '2026-11-12T00:00:00Z',
            to: '2026-11-12T01:00:00Z',
        });
        const u = new URL(seen);
        expect(u.pathname).toBe('/v1/audit/events');
        expect(u.searchParams.get('from')).toBe('2026-11-12T00:00:00Z');
        expect(u.searchParams.get('to')).toBe('2026-11-12T01:00:00Z');
    });
});
