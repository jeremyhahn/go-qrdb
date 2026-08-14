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
 * SearchBar type definitions.
 *
 * Provides comprehensive TypeScript interfaces for the SearchBar component
 * and its engine-specific variants (KV, TimeSeries, Vector).
 */

// ---------------------------------------------------------------------------
// KV/MVCC Search Options
// ---------------------------------------------------------------------------

/** Search mode for KV operations. */
export type SearchMode = 'prefix' | 'range' | 'exact';

/**
 * Options for KV/MVCC search operations.
 */
export interface SearchOptions {
    /** Key prefix for prefix search mode. */
    prefix?: string | undefined;
    /** Start key for range search mode (inclusive). */
    startKey?: string | undefined;
    /** End key for range search mode (exclusive). */
    endKey?: string | undefined;
    /** Maximum number of results to return. */
    limit?: number | undefined;
    /** Whether to perform case-sensitive matching. */
    caseSensitive?: boolean | undefined;
    /** MVCC revision to query at (omit for latest). */
    revision?: number | undefined;
    /** Whether to include deleted keys in results. */
    includeDeleted?: boolean | undefined;
}

// ---------------------------------------------------------------------------
// TimeSeries Search Options
// ---------------------------------------------------------------------------

/** Aggregation functions for time series data. */
export type TimeSeriesAggregation = 'none' | 'avg' | 'sum' | 'min' | 'max' | 'count';

/**
 * Options for time series search operations.
 */
export interface TimeSeriesSearchOptions {
    /** Metric name to query. */
    metric: string;
    /** Start of the time range (inclusive). */
    startTime?: Date | undefined;
    /** End of the time range (inclusive). */
    endTime?: Date | undefined;
    /** Tag filters as key-value pairs. */
    tags?: Record<string, string> | undefined;
    /** Aggregation function to apply. */
    aggregation?: TimeSeriesAggregation | undefined;
    /** Aggregation interval (e.g., "1m", "5m", "1h"). */
    interval?: string | undefined;
}

/** Quick time range presets for time series queries. */
export type TimeRangePreset = '5m' | '15m' | '1h' | '6h' | '24h' | '7d' | '30d' | 'custom';

// ---------------------------------------------------------------------------
// Vector Search Options
// ---------------------------------------------------------------------------

/** Distance metrics for vector similarity search. */
export type DistanceMetric = 'cosine' | 'euclidean' | 'dot';

/** Search method for vector queries. */
export type VectorSearchMethod = 'vector' | 'id' | 'metadata';

/**
 * Options for vector similarity search operations.
 */
export interface VectorSearchOptions {
    /** Namespace containing the vectors. */
    namespace: string;
    /** Raw vector for similarity search. */
    vector?: number[] | undefined;
    /** Vector ID to search by. */
    vectorId?: string | undefined;
    /** Number of nearest neighbors to return. */
    topK?: number | undefined;
    /** Metadata filter conditions. */
    metadataFilter?: Record<string, unknown> | undefined;
    /** Distance metric for similarity calculation. */
    distanceMetric?: DistanceMetric | undefined;
}

// ---------------------------------------------------------------------------
// Engine Type
// ---------------------------------------------------------------------------

/** Supported search engines. */
export type SearchEngine = 'kv' | 'timeseries' | 'vector';

// ---------------------------------------------------------------------------
// Event Types
// ---------------------------------------------------------------------------

/**
 * Event payload for KV search.
 */
export interface KVSearchEvent {
    mode: SearchMode;
    options: SearchOptions;
}

/**
 * Event payload for TimeSeries search.
 */
export interface TimeSeriesSearchEvent {
    options: TimeSeriesSearchOptions;
}

/**
 * Event payload for Vector search.
 */
export interface VectorSearchEvent {
    method: VectorSearchMethod;
    options: VectorSearchOptions;
}

/**
 * Union type for all search events.
 */
export type SearchEvent =
    | { engine: 'kv'; data: KVSearchEvent }
    | { engine: 'timeseries'; data: TimeSeriesSearchEvent }
    | { engine: 'vector'; data: VectorSearchEvent };
