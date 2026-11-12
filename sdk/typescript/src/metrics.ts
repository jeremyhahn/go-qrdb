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

/**
 * Parser for the Prometheus text exposition format (version 0.0.4), used
 * by AdminClient.metrics() over REST.
 */

import type { MetricFamily, MetricSample } from './types.js';

interface MutableFamily {
    name: string;
    help: string;
    type: string;
    samples: MetricSample[];
}

/** Suffixes that attach a sample to its histogram/summary family. */
const FAMILY_SUFFIXES = ['_bucket', '_sum', '_count', '_total', '_created', '_gsum', '_gcount'];

/** Decodes a HELP text: `\\` and `\n` escapes. */
function unescapeHelp(s: string): string {
    return s.replace(/\\(\\|n)/g, (_m, c: string) => (c === 'n' ? '\n' : '\\'));
}

/** Parses a sample value, including +Inf, -Inf and NaN. */
export function parseMetricValue(s: string): number {
    switch (s) {
        case '+Inf':
        case 'Inf':
            return Number.POSITIVE_INFINITY;
        case '-Inf':
            return Number.NEGATIVE_INFINITY;
        case 'NaN':
            return Number.NaN;
        default: {
            const v = Number(s);
            if (s.trim() === '' || (Number.isNaN(v) && s !== 'NaN')) {
                throw new Error(`metrics: invalid sample value ${JSON.stringify(s)}`);
            }
            return v;
        }
    }
}

/**
 * Parses a `{...}` label set starting at `line[start] === '{'`. Returns the
 * labels and the index just past the closing brace.
 */
function parseLabels(line: string, start: number): { labels: Record<string, string>; end: number } {
    const labels: Record<string, string> = {};
    let i = start + 1;
    const skipSpace = (): void => {
        while (i < line.length && (line[i] === ' ' || line[i] === '\t')) i++;
    };
    for (;;) {
        skipSpace();
        if (i >= line.length) throw new Error(`metrics: unterminated label set: ${line}`);
        if (line[i] === '}') return { labels, end: i + 1 };
        const nameStart = i;
        while (i < line.length && /[A-Za-z0-9_:]/.test(line[i]!)) i++;
        const name = line.slice(nameStart, i);
        if (name === '') throw new Error(`metrics: invalid label name: ${line}`);
        skipSpace();
        if (line[i] !== '=') throw new Error(`metrics: expected '=' after label ${name}: ${line}`);
        i++;
        skipSpace();
        if (line[i] !== '"') throw new Error(`metrics: expected '"' for label ${name}: ${line}`);
        i++;
        let value = '';
        for (;;) {
            if (i >= line.length) throw new Error(`metrics: unterminated label value: ${line}`);
            const c = line[i]!;
            if (c === '\\') {
                const n = line[i + 1];
                if (n === 'n') value += '\n';
                else if (n === '\\' || n === '"') value += n;
                else value += '\\' + (n ?? '');
                i += 2;
                continue;
            }
            if (c === '"') {
                i++;
                break;
            }
            value += c;
            i++;
        }
        labels[name] = value;
        skipSpace();
        if (line[i] === ',') i++;
    }
}

/** Finds the family a sample name belongs to, given the known families. */
function familyFor(sampleName: string, families: Map<string, MutableFamily>): string {
    if (families.has(sampleName)) return sampleName;
    for (const suffix of FAMILY_SUFFIXES) {
        if (sampleName.endsWith(suffix)) {
            const base = sampleName.slice(0, -suffix.length);
            if (families.has(base)) return base;
        }
    }
    return sampleName;
}

/**
 * Parses a Prometheus text exposition into metric families, in the order
 * they first appear. HELP and TYPE lines create or annotate a family;
 * histogram and summary samples (`_bucket`, `_sum`, `_count`) join their
 * family. Samples without a declared family form an untyped family.
 */
export function parsePrometheusText(text: string): MetricFamily[] {
    const families = new Map<string, MutableFamily>();
    const get = (name: string): MutableFamily => {
        let f = families.get(name);
        if (f === undefined) {
            f = { name, help: '', type: 'untyped', samples: [] };
            families.set(name, f);
        }
        return f;
    };
    for (const raw of text.split('\n')) {
        const line = raw.replace(/\r$/, '').trim();
        if (line === '') continue;
        if (line.startsWith('#')) {
            const m = /^#\s+(HELP|TYPE)\s+(\S+)(?:\s(.*))?$/.exec(line);
            if (m === null) continue; // plain comment
            const fam = get(m[2]!);
            if (m[1] === 'HELP') fam.help = unescapeHelp(m[3] ?? '');
            else fam.type = (m[3] ?? 'untyped').trim() || 'untyped';
            continue;
        }
        let i = 0;
        while (i < line.length && /[A-Za-z0-9_:]/.test(line[i]!)) i++;
        const name = line.slice(0, i);
        if (name === '') throw new Error(`metrics: invalid sample line: ${line}`);
        let labels: Record<string, string> = {};
        if (line[i] === '{') {
            const parsed = parseLabels(line, i);
            labels = parsed.labels;
            i = parsed.end;
        }
        const rest = line.slice(i).trim().split(/\s+/);
        if (rest[0] === undefined || rest[0] === '') {
            throw new Error(`metrics: missing sample value: ${line}`);
        }
        const value = parseMetricValue(rest[0]);
        get(familyFor(name, families)).samples.push({ name, labels, value });
    }
    return [...families.values()].map((f) => ({
        name: f.name,
        help: f.help,
        type: f.type,
        samples: f.samples,
    }));
}
