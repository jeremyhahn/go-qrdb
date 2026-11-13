// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { describe, it, expect, vi, afterEach } from 'vitest';
import { parsePrometheusText } from '../src/metrics.js';
import { RestClient } from '../src/rest.js';
import { TransportError } from '../src/errors.js';

const EXPOSITION = [
    '# HELP qrdb_requests_total Total requests.\\nSecond line with \\\\ backslash.',
    '# TYPE qrdb_requests_total counter',
    'qrdb_requests_total{method="GET",path="/kv/\\"a\\"\\\\b\\nc"} 1027 1395066363000',
    'qrdb_requests_total{method="PUT"} 3',
    '# a plain comment',
    '# HELP qrdb_latency_seconds Request latency.',
    '# TYPE qrdb_latency_seconds histogram',
    'qrdb_latency_seconds_bucket{le="0.1"} 5',
    'qrdb_latency_seconds_bucket{le="+Inf"} 7',
    'qrdb_latency_seconds_sum 1.5e-1',
    'qrdb_latency_seconds_count 7',
    '# TYPE qrdb_temperature gauge',
    'qrdb_temperature{zone="a", } NaN',
    'qrdb_temperature{zone="b"} -Inf',
    'go_goroutines 42',
    '',
].join('\n');

describe('parsePrometheusText', () => {
    const families = parsePrometheusText(EXPOSITION);

    it('returns families in order with HELP and TYPE', () => {
        expect(families.map((f) => [f.name, f.type])).toEqual([
            ['qrdb_requests_total', 'counter'],
            ['qrdb_latency_seconds', 'histogram'],
            ['qrdb_temperature', 'gauge'],
            ['go_goroutines', 'untyped'],
        ]);
        expect(families[0]!.help).toBe('Total requests.\nSecond line with \\ backslash.');
        expect(families[2]!.help).toBe('');
    });

    it('decodes escaped label values and ignores timestamps', () => {
        const s = families[0]!.samples;
        expect(s).toHaveLength(2);
        expect(s[0]).toEqual({
            name: 'qrdb_requests_total',
            labels: { method: 'GET', path: '/kv/"a"\\b\nc' },
            value: 1027,
        });
        expect(s[1]!.labels).toEqual({ method: 'PUT' });
    });

    it('groups histogram samples and decodes +Inf', () => {
        const h = families[1]!.samples;
        expect(h.map((x) => x.name)).toEqual([
            'qrdb_latency_seconds_bucket',
            'qrdb_latency_seconds_bucket',
            'qrdb_latency_seconds_sum',
            'qrdb_latency_seconds_count',
        ]);
        expect(h[1]!.labels).toEqual({ le: '+Inf' });
        expect(h[2]!.value).toBeCloseTo(0.15);
    });

    it('decodes NaN and -Inf and tolerates a trailing comma', () => {
        const g = families[2]!.samples;
        expect(g[0]!.labels).toEqual({ zone: 'a' });
        expect(Number.isNaN(g[0]!.value)).toBe(true);
        expect(g[1]!.value).toBe(Number.NEGATIVE_INFINITY);
        expect(families[3]!.samples[0]).toEqual({ name: 'go_goroutines', labels: {}, value: 42 });
    });

    it('rejects malformed lines', () => {
        expect(() => parsePrometheusText('x{a="b} 1')).toThrow(/unterminated/);
        expect(() => parsePrometheusText('x 1abc')).toThrow(/invalid sample value/);
        expect(() => parsePrometheusText('x')).toThrow(/missing sample value/);
    });

    it('returns no families for an empty body', () => {
        expect(parsePrometheusText('')).toEqual([]);
    });
});

describe('RestClient admin.metrics', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    const BASE = 'https://localhost:63002';

    /** Routes /metrics to the given response; everything else answers health. */
    function stubFetch(metrics: () => Response): ReturnType<typeof vi.fn> {
        const fetchMock = vi.fn(async (input: unknown) => {
            const url = String(input instanceof Request ? input.url : input);
            if (url.endsWith('/metrics')) return metrics();
            return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
        });
        vi.stubGlobal('fetch', fetchMock);
        return fetchMock;
    }

    it('GETs /metrics and parses the exposition', async () => {
        const fetchMock = stubFetch(() => new Response(EXPOSITION, { status: 200 }));
        const client = new RestClient(BASE);
        await client.connection.connect(BASE);
        const families = await client.admin.metrics();
        expect(families.map((f) => f.name)).toEqual([
            'qrdb_requests_total',
            'qrdb_latency_seconds',
            'qrdb_temperature',
            'go_goroutines',
        ]);
        const urls = fetchMock.mock.calls.map((c) => String(c[0]));
        expect(urls).toContain(`${BASE}/metrics`);
    });

    it('surfaces a server error as TransportError', async () => {
        stubFetch(() => new Response('boom', { status: 500 }));
        const client = new RestClient(BASE);
        await client.connection.connect(BASE);
        await expect(client.admin.metrics()).rejects.toBeInstanceOf(TransportError);
    });
});
