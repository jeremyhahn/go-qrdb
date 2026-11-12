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
 * SearchBar component exports.
 *
 * A comprehensive search interface component for QRDB with engine-specific
 * variants for KV, TimeSeries, and Vector queries.
 *
 * @example
 * ```svelte
 * <script lang="ts">
 *   import { SearchBar, type SearchEvent } from '$components/core/SearchBar';
 *
 *   function handleSearch(event: SearchEvent): void {
 *     switch (event.engine) {
 *       case 'kv':
 *         console.log('KV search:', event.data);
 *         break;
 *       case 'timeseries':
 *         console.log('TimeSeries search:', event.data);
 *         break;
 *       case 'vector':
 *         console.log('Vector search:', event.data);
 *         break;
 *     }
 *   }
 * </script>
 *
 * <SearchBar engine="kv" onSearch={handleSearch} />
 * ```
 */

// Main wrapper component
export { default as SearchBar } from "./SearchBar.svelte";

// Engine-specific components (for direct use if needed)
export { default as KVSearch } from "./KVSearch.svelte";
export { default as TimeSeriesSearch } from "./TimeSeriesSearch.svelte";
export { default as VectorSearch } from "./VectorSearch.svelte";

// Types
export type {
  // Search modes and options
  SearchMode,
  SearchOptions,

  // TimeSeries types
  TimeSeriesAggregation,
  TimeSeriesSearchOptions,
  TimeRangePreset,

  // Vector types
  DistanceMetric,
  VectorSearchMethod,
  VectorSearchOptions,

  // Engine type
  SearchEngine,

  // Event types
  KVSearchEvent,
  TimeSeriesSearchEvent,
  VectorSearchEvent,
  SearchEvent,
} from "./types.ts";
