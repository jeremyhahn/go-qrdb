/**
 * Wails tenants.create contract: the desktop TenantBinding.Create decodes
 * sdk/go/transport CreateTenantRequest, whose security-officer fields are
 * so_username, so_csr_pem, so_cert_pem, so_chain_pem and so_pin; it answers
 * CreateTenantResponse {tenant, admin, so}. The Wails client must send the
 * same snake_case body the REST client sends and surface the SO credential
 * camelCased, as REST does.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { WailsClient } from '../src/wails.js';
import type { CreateTenantRequest } from '../src/index.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installTenantBinding(binding: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { TenantBinding: binding } } });
}

describe('WailsClient tenants.create', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });

    it('sends the security-officer fields under the Go transport keys', async () => {
        const Create = vi.fn(async (..._args: unknown[]): Promise<unknown> => ({
            tenant: {
                id: 't1',
                name: 'acme',
                isolation_type: 'prefix',
                status: 'active',
                created_at: '2026-11-12T00:00:00Z',
                updated_at: '2026-11-12T00:00:00Z',
            },
            admin: { username: 'alice' },
            so: { username: 'officer' },
        }));
        installTenantBinding({ Create });

        const req: CreateTenantRequest = {
            id: 't1',
            name: 'acme',
            adminUsername: 'alice',
            adminCsrPem: 'ADMIN-CSR',
            soUsername: 'officer',
            soCsrPem: 'SO-CSR',
            soPin: '123456',
        };
        const resp = await new WailsClient().tenants.create(req);

        expect(Create).toHaveBeenCalledTimes(1);
        expect(Create.mock.calls[0][0]).toEqual({
            id: 't1',
            name: 'acme',
            admin_username: 'alice',
            admin_csr_pem: 'ADMIN-CSR',
            so_username: 'officer',
            so_csr_pem: 'SO-CSR',
            so_pin: '123456',
        });
        expect(resp.tenant.isolationType).toBe('prefix');
        expect(resp.tenant.status).toBe('active');
        expect(resp.tenant.createdAt).toBe('2026-11-12T00:00:00Z');
        expect(resp.admin.username).toBe('alice');
        expect(resp.so?.username).toBe('officer');
    });

    it('maps soCertPem and soChainPem to so_cert_pem and so_chain_pem', async () => {
        const Create = vi.fn(async (..._args: unknown[]): Promise<unknown> => ({ tenant: { id: 't2' }, admin: {} }));
        installTenantBinding({ Create });

        const resp = await new WailsClient().tenants.create({
            id: 't2',
            name: 'beta',
            adminUsername: 'bob',
            adminCertPem: 'ADMIN-CERT',
            soUsername: 'sec',
            soCertPem: 'SO-CERT',
            soChainPem: 'SO-CHAIN',
            soPin: '654321',
        });

        const sent = Create.mock.calls[0][0] as Record<string, unknown>;
        expect(sent.so_cert_pem).toBe('SO-CERT');
        expect(sent.so_chain_pem).toBe('SO-CHAIN');
        expect(resp.so).toBeUndefined();
    });
});
