// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Wire contract for time-series tag maps on the REST transport: tag names
 * are user data, not struct fields, so the SDK's camelCase/snake_case key
 * conversion must never touch them -- in requests or in responses.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { RestClient } from '../src/rest.js';
import { ConfigError } from '../src/errors.js';

const base = 'https://localhost:63002';

async function connected(): Promise<RestClient> {
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ status: 'ok' }), { status: 200 }));
    const client = new RestClient(base);
    await client.connection.connect(base);
    return client;
}

function capture(response: unknown): { sent: () => Record<string, unknown> } {
    let body: Record<string, unknown> = {};
    vi.stubGlobal('fetch', async (_url: string, init?: RequestInit) => {
        body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
        return new Response(JSON.stringify(response), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
        });
    });
    return { sent: () => body };
}

const userTags = [
    { key: 'hostName', value: 'node1' },
    { key: 'data_center', value: 'east' },
    { key: 'Region_ID', value: 'r1' },
];
const userTagMap = { hostName: 'node1', data_center: 'east', Region_ID: 'r1' };

describe('time-series tag keys (REST)', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('richQuery sends user tag keys verbatim and returns them verbatim', async () => {
        const client = await connected();
        const wire = capture({
            metric: 'cpu',
            tags: userTagMap,
            points: [{ metric: 'cpu', value: 1, timestamp: 5, tags: userTagMap }],
            aggregated_value: 2.5,
            grouped_results: [{ tags: userTagMap, points: [], aggregated_value: 3 }],
        });

        const resp = await client.timeSeries!.richQuery({ metric: 'cpu', startNs: 0, endNs: 10, tags: userTags });

        expect(wire.sent().tags).toEqual(userTagMap);
        expect(resp.tags).toEqual(userTags);
        expect(resp.points[0].tags).toEqual(userTags);
        expect(resp.aggregatedValue).toBe(2.5);
        expect(resp.groupedResults?.[0].tags).toEqual(userTags);
        expect(resp.groupedResults?.[0].aggregatedValue).toBe(3);
    });

    it('deleteSeries refuses a tag filter the server cannot parse and sends nothing', async () => {
        const client = await connected();
        const urls: string[] = [];
        vi.stubGlobal('fetch', async (url: string) => {
            urls.push(url);
            return new Response(JSON.stringify({ status: 'ok' }), { status: 200 });
        });

        for (const tags of [
            [{ key: 'host', value: 'a,b' }],
            [{ key: 'host', value: 'a=b' }],
            [{ key: 'ho=st', value: 'a' }],
            [{ key: 'ho,st', value: 'a' }],
            [{ key: 'host', value: ' a' }],
            [{ key: 'host ', value: 'a' }],
            [{ key: '', value: 'a' }],
        ]) {
            await expect(client.timeSeries!.deleteSeries({ metric: 'cpu', tags })).rejects.toThrow(ConfigError);
        }
        expect(urls).toEqual([]);
    });

    it('deleteSeries sends a plain tag filter as DELETE /ts/series/<metric>?tags=', async () => {
        const client = await connected();
        const sent: { url: string; method: string }[] = [];
        vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
            sent.push({ url, method: init?.method ?? 'GET' });
            return new Response(JSON.stringify({ status: 'ok' }), {
                status: 200,
                headers: { 'Content-Type': 'application/json' },
            });
        });

        await client.timeSeries!.deleteSeries({ metric: 'cpu', tags: [{ key: 'hostName', value: 'node1' }] });

        expect(sent.length).toBe(1);
        expect(sent[0]!.method).toBe('DELETE');
        const u = new URL(sent[0]!.url);
        expect(`${u.origin}${u.pathname}`).toBe(`${base}/ts/series/cpu`);
        expect(u.searchParams.get('tags')).toBe('hostName=node1');
    });

    it('writePoint and writeBatch send user tag keys verbatim', async () => {
        const client = await connected();
        const wire = capture({});
        await client.timeSeries!.writePoint({ metric: 'cpu', value: 1, timestampNs: 5, tags: userTags });
        expect(wire.sent().tags).toEqual(userTagMap);

        await client.timeSeries!.writeBatch([{ metric: 'cpu', value: 1, timestampNs: 5, tags: userTags }]);
        const points = wire.sent().points as Record<string, unknown>[];
        expect(points[0].tags).toEqual(userTagMap);
    });
});
