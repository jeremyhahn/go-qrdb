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
 * The Wails vector adapter speaks the Go wire shapes of VectorBinding:
 * transport.HybridSearchRequest reads query_vector and metadata_filter, and
 * the batch calls answer the Go BatchUpsertResponse (inserted_count) and
 * BatchDeleteVectorsResponse (deleted_count, failed_ids). The adapter maps
 * both to the shared UI shapes exactly as the REST adapter does.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { WailsClient } from '../src/wails.js';

type BindingFn = (...args: unknown[]) => Promise<unknown>;

function installVectorBinding(fns: Record<string, BindingFn>): void {
    vi.stubGlobal('window', { go: { main: { VectorBinding: fns } } });
}

describe('WailsClient vector wire shapes', () => {
    beforeEach(() => {
        vi.unstubAllGlobals();
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('hybridSearch sends query_vector and metadata_filter', async () => {
        const spy = vi.fn<BindingFn>(async () => ({ results: [{ id: 7, score: 0.9 }], took: 1 }));
        installVectorBinding({ HybridSearch: spy });

        const resp = await new WailsClient().vector!.hybridSearch({
            namespace: 'ns',
            vector: [0.1, 0.2],
            metadataFilters: { color: 'blue' },
            k: 3,
        });

        expect(spy).toHaveBeenCalledTimes(1);
        const sent = spy.mock.calls[0]![0] as Record<string, unknown>;
        expect(sent).toMatchObject({
            namespace: 'ns',
            query_vector: [0.1, 0.2],
            metadata_filter: { color: 'blue' },
            k: 3,
        });
        expect(sent).not.toHaveProperty('vector');
        expect(sent).not.toHaveProperty('metadata_filters');
        expect(resp.results).toHaveLength(1);
    });

    it('getVector asks the binding for the vector values and the metadata', async () => {
        // The Go GetVectorRequest defaults include_vector and
        // include_metadata to false, which omits both from the answer; the
        // detail editor would then show no values and save {} over the
        // stored metadata.
        const spy = vi.fn<BindingFn>(async () => ({ id: 12, vector: [0.5], metadata: { k: 'v' } }));
        installVectorBinding({ GetVector: spy });

        const vec = await new WailsClient().vector!.getVector({ namespace: 'ns', id: '12' });

        expect(spy).toHaveBeenCalledTimes(1);
        const sent = spy.mock.calls[0]![0] as Record<string, unknown>;
        expect(sent).toMatchObject({ namespace: 'ns', include_vector: true, include_metadata: true });
        expect(vec.vector).toEqual([0.5]);
        expect(vec.metadata).toEqual({ k: 'v' });
    });

    it('batchUpsert maps inserted_count to succeeded and failed', async () => {
        installVectorBinding({
            BatchUpsert: vi.fn<BindingFn>(async () => ({
                ids: [1, 2], versions: [1, 1], timestamp: 5, success: true, inserted_count: 2,
            })),
        });

        const resp = await new WailsClient().vector!.batchUpsert({
            namespace: 'ns',
            vectors: [
                { id: '1', vector: [0.1] },
                { id: '2', vector: [0.2] },
                { id: '3', vector: [0.3] },
            ],
        });

        expect(resp).toEqual({ succeeded: 2, failed: 1 });
    });

    it('batchDeleteVectors maps deleted_count and failed_ids', async () => {
        installVectorBinding({
            BatchDeleteVectors: vi.fn<BindingFn>(async () => ({
                deleted_count: 1, failed_ids: [9], timestamp: 5, success: false,
            })),
        });

        const resp = await new WailsClient().vector!.batchDeleteVectors({ namespace: 'ns', ids: ['8', '9'] });

        expect(resp.deleted).toBe(1);
        expect(resp.failed).toBe(1);
        expect(resp.errors).toEqual([{ id: '9', error: 'not deleted' }]);
    });

    it('batchDeleteVectors reports every ID deleted when failed_ids is empty', async () => {
        installVectorBinding({
            BatchDeleteVectors: vi.fn<BindingFn>(async () => ({ deleted_count: 2, success: true })),
        });

        const resp = await new WailsClient().vector!.batchDeleteVectors({ namespace: 'ns', ids: ['a', 'b'] });

        expect(resp).toEqual({ deleted: 2, failed: 0, errors: [] });
    });
});
