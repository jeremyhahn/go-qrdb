/**
 * Tenant wire contract: GET /v1/tenants/{id} carries the tenant's
 * operational status (pkg/api/rest/handlers_tenants.go tenantResponse.Status,
 * pkg/tenant Status: active | suspended | disabled). The SDK surfaces it as
 * TenantInfo.status.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import type { TenantStatus } from '../src/index.js';

function jsonResponse(body: unknown): Response {
    return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
    });
}

describe('Tenant status wire contract (REST)', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    const statuses: TenantStatus[] = ['active', 'suspended', 'disabled'];
    for (const status of statuses) {
        it(`tenants.get surfaces status ${status}`, async () => {
            vi.stubGlobal('fetch', async () => jsonResponse({ status: 'ok' }));
            const client = new RestClient('https://localhost:63002');
            await client.connection.connect('https://localhost:63002');

            vi.stubGlobal('fetch', async () =>
                jsonResponse({
                    id: 't1',
                    name: 'acme',
                    isolation_type: 'prefix',
                    status,
                    createdAt: '2026-11-12T00:00:00Z',
                    updatedAt: '2026-11-12T00:00:00Z',
                }),
            );
            const { tenant } = await client.tenants.get('t1');
            expect(tenant.status).toBe(status);
        });
    }
});
