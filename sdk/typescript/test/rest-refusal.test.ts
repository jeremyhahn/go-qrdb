// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * A REST refusal carries the server's own message, not the raw JSON body.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { RestClient, serverRefusal } from '../src/rest.js';
import { TransportError } from '../src/errors.js';

describe('serverRefusal', () => {
    it('takes a string error field', () => {
        expect(serverRefusal('{"error":"database: database already exists: \\"x\\""}').message)
            .toBe('database: database already exists: "x"');
    });

    it('takes an error object message', () => {
        expect(serverRefusal('{"error":{"code":"conflict","message":"busy"}}').message).toBe('busy');
    });

    it('takes a top-level message', () => {
        expect(serverRefusal('{"message":"denied"}').message).toBe('denied');
    });

    it('keeps a body that is not a JSON error envelope', () => {
        expect(serverRefusal('plain text').message).toBe('plain text');
        expect(serverRefusal('{"other":1}').message).toBe('{"other":1}');
        expect(serverRefusal('{"error":""}').message).toBe('{"error":""}');
    });
});

describe('RestClient refusal', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('a 409 TransportError shows the server message once, unescaped', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => new Response(
            JSON.stringify({ status: 'ok' }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
        )));
        const client = new RestClient('https://localhost:63002');
        await client.connection.connect('https://localhost:63002');
        vi.stubGlobal('fetch', vi.fn(async () => new Response(
            JSON.stringify({ error: 'database: database already exists: "x"' }),
            { status: 409, headers: { 'Content-Type': 'application/json' } },
        )));
        const err = await client.database.create({ name: 'x', backend: 'raft' }).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(TransportError);
        const te = err as TransportError;
        expect(te.statusCode).toBe(409);
        expect(te.message).toBe(
            'transport: request POST /v1/databases failed (status 409): database: database already exists: "x"',
        );
    });
});
