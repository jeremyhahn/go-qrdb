/**
 * Route parity: every REST call the SDK makes names a method and path the
 * server registers (#1478). The server is the contract: the routes come from
 * pkg/api/rest/server.go, the calls from src/rest.ts, both read as text.
 *
 * A call is a path literal passed straight to a call (`this.fetchJSON('/x')`,
 * `list('/v1/...')`) or a suffix given to the tenantPath/databasePath helpers.
 * Its method is the `method: '...'` inside the same call, GET when absent.
 * A `${...}` filling a whole path segment is a path parameter; one appended to
 * a segment (a query string) is dropped.
 */
import { describe, expect, it } from 'vitest';
import restSource from '../src/rest.ts?raw';
import serverSource from '../../../pkg/api/rest/server.go?raw';
import publicVerbsSource from '../../../pkg/api/publicverbs/publicverbs.go?raw';

interface Route {
    readonly method: string;
    readonly path: string;
}

/** SDK calls with no server route yet; each needs a server change. */
const KNOWN_UNSERVED: readonly string[] = [];

function serverRoutes(): Route[] {
    const routes: Route[] = [];
    const re = /http\.Method(\w+), "(\/[^"]*)"/g;
    for (const m of serverSource.matchAll(re)) {
        routes.push({ method: m[1].toUpperCase(), path: m[2] });
    }
    // Public verbs (health, invitation redeem, ...) are registered by
    // publicVerb from the publicverbs table, not by a literal route call.
    for (const m of publicVerbsSource.matchAll(/REST:\s+"(\w+) (\/[^"]*)"/g)) {
        routes.push({ method: m[1], path: m[2] });
    }
    return routes;
}

/** Index just past the `}` closing the `${` that starts at `start`. */
function skipInterpolation(src: string, start: number): number {
    let depth = 0;
    for (let i = start + 1; i < src.length; i++) {
        if (src[i] === '{') depth++;
        else if (src[i] === '}' && --depth === 0) return i + 1;
    }
    throw new Error(`unbalanced interpolation at ${start}`);
}

/** Normalise a path literal's body: parameters become `{}`, queries go. */
function normalisePath(body: string): string {
    let out = '';
    for (let i = 0; i < body.length;) {
        if (body.startsWith('${', i)) {
            const end = skipInterpolation(body, i);
            const wholeSegment = out.endsWith('/') && (end === body.length || body[end] === '/');
            if (wholeSegment) out += '{}';
            i = end;
        } else if (body[i] === '?') {
            break;
        } else {
            out += body[i++];
        }
    }
    return out;
}

/** The text of the call whose `(` is at `open`, through its closing `)`. */
function callText(src: string, open: number): string {
    let depth = 0;
    for (let i = open; i < src.length; i++) {
        if (src[i] === '(') depth++;
        else if (src[i] === ')' && --depth === 0) return src.slice(open, i + 1);
    }
    throw new Error(`unbalanced call at ${open}`);
}

function methodOf(call: string): string {
    return /method: '(\w+)'/.exec(call)?.[1] ?? 'GET';
}

function sdkCalls(): Route[] {
    const calls: Route[] = [];
    const literal = /\(\s*(?:'(\/[a-z][^']*)'|`(\/[a-z][^`]*)`)/g;
    for (const m of restSource.matchAll(literal)) {
        const open = m.index;
        // A local `post<T>(path, body)` helper (the vlog client) sends POST.
        const viaPost = /\bpost<[^<>()]*>$/.test(restSource.slice(Math.max(0, open - 120), open));
        const method = viaPost ? 'POST' : methodOf(callText(restSource, open));
        calls.push({ method, path: normalisePath(m[1] ?? m[2]) });
    }
    const helpers: Record<string, string> = {
        tenantPath: '/v1/tenants/{}/barrier',
        databasePath: '/v1/databases/{}/barrier',
    };
    const helper = /(tenantPath|databasePath)\([^,()]+, '([^']+)'\)/g;
    for (const m of restSource.matchAll(helper)) {
        const open = restSource.lastIndexOf('(', m.index);
        calls.push({ method: methodOf(callText(restSource, open)), path: helpers[m[1]] + m[2] });
    }
    return calls;
}

function segmentMatches(call: string, route: string): boolean {
    if (/^\{\w+\}$/.test(route)) return true;
    return call !== '{}' && call === route;
}

function served(call: Route, routes: readonly Route[]): boolean {
    const want = call.path.split('/');
    return routes.some((r) => {
        if (r.method !== call.method) return false;
        const have = r.path.split('/');
        if (have[have.length - 1] === '*') {
            const prefix = have.slice(0, -1);
            return want.length > prefix.length && prefix.every((s, i) => segmentMatches(want[i], s));
        }
        return have.length === want.length && have.every((s, i) => segmentMatches(want[i], s));
    });
}

describe('REST route parity with pkg/api/rest/server.go', () => {
    const routes = serverRoutes();
    const calls = sdkCalls();

    it('reads both sources', () => {
        expect(routes.length).toBeGreaterThan(250);
        expect(calls.length).toBeGreaterThan(150);
    });

    it('every SDK call names a served method and path', () => {
        const unserved = calls
            .filter((c) => !served(c, routes))
            .map((c) => `${c.method} ${c.path}`);
        expect([...new Set(unserved)].sort()).toEqual([...KNOWN_UNSERVED].sort());
    });

    it.each([
        ['GET', '/v1/rebalance/shards/{}'],
        ['GET', '/ts/series/{}/retention'],
        ['PUT', '/ts/series/{}/retention'],
        ['DELETE', '/ts/series/{}'],
        ['POST', '/ts/query-rich'],
        ['GET', '/kv'],
        ['PUT', '/kv/{}'],
        ['POST', '/v1/databases/{}/barrier/officers/unenrol'],
        ['POST', '/v1/tenants/{}/barrier/rekey/start'],
        ['POST', '/vlog/append'],
        ['GET', '/vlog/head'],
        ['GET', '/health'],
        ['POST', '/invitations/redeem'],
    ])('pins %s %s as called and served', (method, path) => {
        expect(calls).toContainEqual({ method, path });
        expect(served({ method, path }, routes)).toBe(true);
    });

    it('rejects a call the server does not serve', () => {
        expect(served({ method: 'GET', path: '/v1/rebalance/stats/{}' }, routes)).toBe(false);
        expect(served({ method: 'POST', path: '/kv/{}' }, routes)).toBe(false);
        expect(served({ method: 'GET', path: '/kv/range' }, routes)).toBe(true);
        expect(served({ method: 'GET', path: '/kv/{}' }, routes)).toBe(true);
    });
});
