/**
 * Every RestClient call whose request body carries a secret -- a barrier
 * secret or passphrase, a PIN, a share, a recovery key or a token -- goes
 * through the secret guard, as the Go REST transport's GuardBarrierSecret
 * does: an http:// base URL is refused before fetch is called, a redirect is
 * refused instead of followed, and the https:// path still succeeds.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { ConfigError, TransportError } from '../src/errors.js';

const HTTPS = 'https://localhost:63002';
const HTTP = 'http://localhost:63002';

interface SentRequest {
    readonly url: string;
    readonly method: string;
    readonly redirect: RequestRedirect | undefined;
}

interface SecretCase {
    readonly name: string;
    /** Method and path of the request that carries the secret. */
    readonly request: string;
    /** Answers by "METHOD path"; anything unlisted answers 204. */
    readonly answers?: Record<string, unknown>;
    readonly call: (c: RestClient) => Promise<unknown>;
}

const SECRET_CASES: readonly SecretCase[] = [
    {
        name: 'seal.initialize',
        request: 'POST /v1/barrier/initialize',
        call: (c) => c.seal.initialize('pass'),
    },
    {
        name: 'seal.initializeShamir with an SO PIN',
        request: 'POST /v1/barrier/shamir/initialize',
        answers: { 'POST /v1/barrier/shamir/initialize': { threshold: 2, total_shares: 3, share_count: 3 } },
        call: (c) => c.seal.initializeShamir({ threshold: 2, totalShares: 3, soPin: 'SO123' }),
    },
    {
        name: 'seal.initializeShamir with a User PIN',
        request: 'POST /v1/barrier/shamir/initialize',
        answers: { 'POST /v1/barrier/shamir/initialize': { threshold: 2, total_shares: 3, share_count: 3 } },
        call: (c) => c.seal.initializeShamir({ threshold: 2, totalShares: 3, userPin: 'U123' }),
    },
    {
        name: 'seal.unseal',
        request: 'POST /v1/barrier/unseal',
        call: (c) => c.seal.unseal('pass'),
    },
    {
        name: 'seal.changePassphrase',
        request: 'POST /v1/barrier/change-passphrase',
        call: (c) => c.seal.changePassphrase('old-pass', 'new-pass', 'SO123'),
    },
    {
        name: 'seal.seal with an SO PIN',
        request: 'POST /v1/barrier/seal',
        call: (c) => c.seal.seal('SO123'),
    },
    {
        name: 'seal.enableEncryption with a passphrase',
        request: 'POST /v1/barrier/enable-encryption',
        answers: { 'POST /v1/barrier/enable-encryption': {} },
        call: (c) => c.seal.enableEncryption({ strategy: 'passphrase', passphrase: 'pass' }),
    },
    {
        name: 'ceremony.initialize with a barrier secret',
        request: 'POST /v1/ceremony/initialize',
        answers: { 'POST /v1/ceremony/initialize': {} },
        call: (c) => c.ceremony.initialize({ adminGenerate: true, secret: 'pass' }),
    },
    {
        name: 'ceremony.initialize with an SO PIN',
        request: 'POST /v1/ceremony/initialize',
        answers: { 'POST /v1/ceremony/initialize': {} },
        call: (c) => c.ceremony.initialize({ adminGenerate: true, soPin: 'SO123' }),
    },
    {
        name: 'ceremony.signCSR',
        request: 'POST /v1/ceremony/sign-csr',
        answers: { 'POST /v1/ceremony/sign-csr': {} },
        call: (c) => c.ceremony.signCSR({ username: 'so', soPin: 'SO123', csrPem: 'csr', role: 'so' }),
    },
    {
        name: 'officers.startRekey with an SO PIN',
        request: 'POST /v1/barrier/rekey/start',
        answers: { 'POST /v1/barrier/rekey/start': {} },
        call: (c) => c.officers.startRekey({ threshold: 2, soPin: 'SO123' }),
    },
    {
        name: 'officers.revokeOfficer with an SO PIN',
        request: 'POST /v1/barrier/officers/revoke',
        answers: { 'POST /v1/barrier/officers/revoke': {} },
        call: (c) => c.officers.revokeOfficer({ username: 'alice', soPin: 'SO123' }),
    },
    {
        name: 'tenants.barrierInit with a passphrase',
        request: 'POST /v1/tenants/acme/barrier/init/begin',
        answers: { 'POST /v1/tenants/acme/barrier/init/begin': {} },
        call: (c) => c.tenants.barrierInit({
            tenantId: 'acme', strategy: 'passphrase', passphrase: 'pass', threshold: 1, shares: 1,
        }),
    },
    {
        name: 'tenants.barrierUnseal',
        request: 'POST /v1/tenants/acme/barrier/unseal',
        answers: { 'POST /v1/tenants/acme/barrier/unseal': {} },
        call: (c) => c.tenants.barrierUnseal({ tenantId: 'acme', key: 'pass' }),
    },
    {
        name: 'databases.create with a barrier secret',
        request: 'POST /v1/databases',
        answers: { 'POST /v1/databases': {} },
        call: (c) => c.database.create({
            name: 'db', backend: 'raft', barrier: { enabled: true, mode: 'passphrase', secret: 'pass' },
        }),
    },
    {
        name: 'databases.create with a discovery join token',
        request: 'POST /v1/databases',
        answers: { 'POST /v1/databases': {} },
        call: (c) => c.database.create({
            name: 'db', backend: 'raft', raft: { clusterMode: 'multi', token: { token: 'join', endpoint: 'https://discovery.example.com', clusterSize: 3 } },
        }),
    },
    {
        name: 'databases.updateRaft with a multicast shared secret',
        request: 'PATCH /v1/databases/db/raft',
        answers: { 'PATCH /v1/databases/db/raft': {} },
        call: (c) => c.database.updateRaft('db', { clusterMode: 'multi', multicast: { sharedSecret: 'hmac' } }),
    },
    {
        name: 'invitations.redeem',
        request: 'POST /invitations/redeem',
        answers: { 'POST /invitations/redeem': {} },
        call: (c) => c.invitations.redeem({ tenantID: 'acme', token: 'invite', csrPEM: 'csr' }),
    },
    {
        name: 'credentials.submit',
        request: 'POST /v1/credentials/submit',
        call: (c) => c.credentials.submit('api', 'value'),
    },
    {
        name: 'credentials.store',
        request: 'POST /v1/credentials/store',
        call: (c) => c.credentials.store('api', 'value'),
    },
    {
        name: 'credentials.rotate',
        request: 'POST /v1/credentials/api/rotate',
        answers: { 'POST /v1/credentials/api/rotate': {} },
        call: (c) => c.credentials.rotate('api', 'value'),
    },
];

/**
 * Installs a fetch double that records every request and answers from
 * answers. It models fetch's redirect modes when redirectTo is set: 'error'
 * rejects on the 3xx, as the Fetch standard requires, and any other mode
 * follows it, resending the request to redirectTo.
 */
function installFetch(answers: Record<string, unknown> = {}, redirectTo?: string): SentRequest[] {
    const sent: SentRequest[] = [];
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = typeof input === 'string' ? input : (input as URL).toString();
        const method = init?.method ?? 'GET';
        sent.push({ url, method, redirect: init?.redirect });
        if (redirectTo !== undefined) {
            if (init?.redirect === 'error') {
                throw new TypeError('fetch failed: unexpected redirect');
            }
            sent.push({ url: redirectTo + new URL(url).pathname, method, redirect: init?.redirect });
            return new Response(null, { status: 204 });
        }
        const body = answers[`${method} ${new URL(url).pathname}`];
        return body === undefined
            ? new Response(null, { status: 204 })
            : new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    return sent;
}

async function connected(base: string): Promise<RestClient> {
    installFetch({ 'GET /health': { status: 'ok' } });
    const client = new RestClient(base);
    await client.connection.connect(base);
    return client;
}

describe('secret-bearing calls go only over https and never follow a redirect', () => {
    beforeEach(() => {
        vi.restoreAllMocks();
    });

    it.each(SECRET_CASES)('$name refuses an http:// base URL before fetch is called', async (tc) => {
        const client = await connected(HTTP);
        const sent = installFetch(tc.answers);

        await expect(tc.call(client)).rejects.toBeInstanceOf(ConfigError);
        expect(sent).toEqual([]);
    });

    it.each(SECRET_CASES)('$name refuses a redirect instead of following it', async (tc) => {
        const client = await connected(HTTPS);
        const sent = installFetch(tc.answers, 'https://attacker.example.com');

        await expect(tc.call(client)).rejects.toBeInstanceOf(TransportError);
        const [method, path] = tc.request.split(' ');
        expect(sent).toEqual([{ url: HTTPS + path, method, redirect: 'error' }]);
    });

    it.each(SECRET_CASES)('$name succeeds over https', async (tc) => {
        const client = await connected(HTTPS);
        const sent = installFetch(tc.answers);

        await expect(tc.call(client)).resolves.not.toBeInstanceOf(Error);
        const [method, path] = tc.request.split(' ');
        expect(sent[0]).toEqual({ url: HTTPS + path, method, redirect: 'error' });
        expect(sent.every((s) => s.redirect === 'error')).toBe(true);
    });
});

describe('a call whose optional secret is absent is not guarded', () => {
    const cases: readonly { name: string; call: (c: RestClient) => Promise<unknown> }[] = [
        { name: 'seal.initialize with no secret', call: (c) => c.seal.initialize('') },
        { name: 'seal.seal with no SO PIN', call: (c) => c.seal.seal() },
        { name: 'officers.startRekey with no SO PIN', call: (c) => c.officers.startRekey({ threshold: 2 }) },
        {
            name: 'databases.create with no secret',
            call: (c) => c.database.create({ name: 'db', backend: 'memory' }),
        },
    ];

    it.each(cases)('$name is sent over http://', async (tc) => {
        const client = await connected(HTTP);
        const sent = installFetch({
            'POST /v1/barrier/rekey/start': {},
            'POST /v1/databases': {},
        });

        await expect(tc.call(client)).resolves.not.toBeInstanceOf(Error);
        expect(sent).toHaveLength(1);
        expect(sent[0].redirect).toBeUndefined();
    });
});
