/**
 * Contract tests for the REST client's tenant barrier init.
 *
 * A "shamir" init is two init/begin, init/complete pairs, as the node runs it
 * (tenant.WithPreparedShamir) and the Go transports drive it
 * (tenantinit.Run): the first prepares the split and answers with its roster
 * and unseal key check value, the barrier still sealed; the second echoes both
 * and keys the barrier. These tests pin that the client sends both rounds,
 * echoes the split it was given, and reports success only on the answer to the
 * second.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { ShamirSplitMismatchError, ShamirSplitMissingError, TransportError } from '../src/errors.js';

interface CapturedRequest {
    path: string;
    method: string;
    body: unknown;
}

interface RouteResponse {
    status?: number;
    body?: unknown;
}

/**
 * Stubs fetch with a router that answers each "METHOD path" key from its own
 * queue, one entry per call, repeating the last entry once the queue is down
 * to one.
 */
function installRouter(routes: Record<string, RouteResponse[]>): { calls: CapturedRequest[] } {
    const calls: CapturedRequest[] = [];
    const queues = new Map(Object.entries(routes).map(([k, v]) => [k, [...v]]));
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        const method = init?.method ?? 'GET';
        const path = new URL(url).pathname;
        let body: unknown;
        if (init?.body && typeof init.body === 'string') {
            body = JSON.parse(init.body);
        }
        calls.push({ path, method, body });
        const queue = queues.get(`${method} ${path}`) ?? [];
        const route = (queue.length > 1 ? queue.shift() : queue[0]) ?? {};
        const status = route.status ?? 200;
        return new Response(status === 204 ? null : JSON.stringify(route.body ?? {}), {
            status,
            headers: { 'Content-Type': 'application/json' },
        });
    });
    return { calls };
}

async function connectedClient(): Promise<RestClient> {
    installRouter({ 'GET /health': [{ body: { status: 'ok' } }] });
    const client = new RestClient('https://localhost:63002');
    await client.connection.connect('https://localhost:63002');
    return client;
}

const BEGIN = '/v1/tenants/acme/barrier/init/begin';
const COMPLETE = '/v1/tenants/acme/barrier/init/complete';
const ROSTER = 'cm9zdGVy';
const CHECK = 'Y2hlY2s=';
const HOLDER = { username: 'alice', role: 'so', cert_pem: '-----BEGIN CERTIFICATE-----' };

/** The node's answer to the first request: the split, barrier still sealed. */
const PREPARED = {
    tenant_id: 'acme',
    strategy: 'shamir',
    threshold: 2,
    total_shares: 3,
    share_count: 3,
    holders: [HOLDER],
    roster: ROSTER,
    unseal_key_check: CHECK,
};

/** The node's answer to the second request: the split it completed. */
const COMPLETED = {
    tenant_id: 'acme',
    strategy: 'shamir',
    threshold: 2,
    total_shares: 3,
    share_count: 3,
    roster: ROSTER,
    unseal_key_check: CHECK,
    admin_pending: true,
};

const SHAMIR = { tenantId: 'acme', threshold: 2, shares: 3, strategy: 'shamir' } as const;

function completes(calls: CapturedRequest[]): Record<string, unknown>[] {
    return calls.filter((c) => c.path === COMPLETE).map((c) => c.body as Record<string, unknown>);
}

describe('tenants.barrierInit – shamir runs the two-round protocol', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('prepares the split, then echoes its roster and check in the second request', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: PREPARED }, { body: COMPLETED }],
        });

        const result = await client.tenants.barrierInit(SHAMIR);

        // begin, complete, begin, complete: a fresh challenge for each round.
        expect(calls.map((c) => c.path)).toEqual([BEGIN, COMPLETE, BEGIN, COMPLETE]);
        const [first, second] = completes(calls);
        expect(first).toEqual({ tenant_id: 'acme', threshold: 2, shares: 3, strategy: 'shamir' });
        expect(second).toEqual({
            tenant_id: 'acme',
            threshold: 2,
            shares: 3,
            strategy: 'shamir',
            roster: ROSTER,
            unseal_key_check: CHECK,
        });
        expect(result).toEqual({
            tenantId: 'acme',
            strategy: 'shamir',
            threshold: 2,
            totalShares: 3,
            shareCount: 3,
            roster: ROSTER,
            unsealKeyCheck: CHECK,
            adminPending: true,
            // The holders' certificates are issued by the first request only.
            holders: [{ username: 'alice', role: 'so', certPem: '-----BEGIN CERTIFICATE-----' }],
        });
    });

    it('carries the passphrase base64 in both rounds', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: PREPARED }, { body: COMPLETED }],
        });

        await client.tenants.barrierInit({ ...SHAMIR, passphrase: 'tenant-pw' });

        expect(completes(calls).map((b) => b.passphrase)).toEqual(['dGVuYW50LXB3', 'dGVuYW50LXB3']);
    });

    it('refuses a first answer that carries no split, and sends no second round', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: { ...PREPARED, roster: undefined } }],
        });

        const err = await client.tenants.barrierInit(SHAMIR).catch((e: unknown) => e);

        expect(err).toBeInstanceOf(ShamirSplitMissingError);
        expect((err as ShamirSplitMissingError).tenantId).toBe('acme');
        expect(completes(calls)).toHaveLength(1);
    });

    it('refuses a first answer with no unseal key check', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: { ...PREPARED, unseal_key_check: undefined } }],
        });

        await expect(client.tenants.barrierInit(SHAMIR)).rejects.toBeInstanceOf(ShamirSplitMissingError);
        expect(completes(calls)).toHaveLength(1);
    });

    it('refuses a 204 to the first request: no split was prepared', async () => {
        const client = await connectedClient();
        installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ status: 204 }],
        });

        await expect(client.tenants.barrierInit(SHAMIR)).rejects.toBeInstanceOf(ShamirSplitMissingError);
    });

    it('surfaces the node refusing a mismatched roster on the second request', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            // seal.PreparedShamirMismatchError is FailedPrecondition: 409.
            [`POST ${COMPLETE}`]: [
                { body: PREPARED },
                { status: 409, body: { error: 'seal: prepared shamir split does not match the signed roster' } },
            ],
        });

        const err = await client.tenants.barrierInit(SHAMIR).catch((e: unknown) => e);

        expect(err).toBeInstanceOf(TransportError);
        expect((err as TransportError).statusCode).toBe(409);
        expect(completes(calls)).toHaveLength(2);
    });

    it('refuses a second answer whose roster differs from the one echoed', async () => {
        const client = await connectedClient();
        installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: PREPARED }, { body: { ...COMPLETED, roster: 'b3RoZXI=' } }],
        });

        const err = await client.tenants.barrierInit(SHAMIR).catch((e: unknown) => e);

        expect(err).toBeInstanceOf(ShamirSplitMismatchError);
        expect((err as ShamirSplitMismatchError).field).toBe('roster');
    });

    it('refuses a second answer whose check differs from the one echoed', async () => {
        const client = await connectedClient();
        installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: PREPARED }, { body: { ...COMPLETED, unseal_key_check: 'b3RoZXI=' } }],
        });

        const err = await client.tenants.barrierInit(SHAMIR).catch((e: unknown) => e);

        expect(err).toBeInstanceOf(ShamirSplitMismatchError);
        expect((err as ShamirSplitMismatchError).field).toBe('unseal key check');
    });

    it('refuses a 204 to the second request: no completed split was reported', async () => {
        const client = await connectedClient();
        installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: PREPARED }, { status: 204 }],
        });

        await expect(client.tenants.barrierInit(SHAMIR)).rejects.toBeInstanceOf(ShamirSplitMismatchError);
    });
});

describe('tenants.barrierInit – other strategies run one round', () => {
    beforeEach(() => vi.unstubAllGlobals());

    it('sends one complete for a passphrase init and synthesizes the 204 answer', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ status: 204 }],
        });

        const result = await client.tenants.barrierInit({
            tenantId: 'acme', passphrase: 'pw', threshold: 0, shares: 0, strategy: 'passphrase',
        });

        expect(calls.map((c) => c.path)).toEqual([BEGIN, COMPLETE]);
        expect(completes(calls)[0].roster).toBeUndefined();
        expect(result).toEqual({ tenantId: 'acme', strategy: 'passphrase' });
    });

    it('returns a decoded answer for a non-shamir init without asking for a split', async () => {
        const client = await connectedClient();
        const { calls } = installRouter({
            [`POST ${BEGIN}`]: [{ body: {} }],
            [`POST ${COMPLETE}`]: [{ body: { tenant_id: 'acme', strategy: 'none', admin_pending: true } }],
        });

        const result = await client.tenants.barrierInit({ tenantId: 'acme', threshold: 0, shares: 0, strategy: 'none' });

        expect(completes(calls)).toHaveLength(1);
        expect(result).toEqual({ tenantId: 'acme', strategy: 'none', adminPending: true });
    });
});
