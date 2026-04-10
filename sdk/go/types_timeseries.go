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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/timeseries"

// ---------------------------------------------------------------------------
// Time series storage types
// ---------------------------------------------------------------------------

// TimeSeriesStorage is a PebbleDB-backed time series storage engine.
type TimeSeriesStorage = timeseries.Storage

// NewTimeSeriesStorage creates a new time series storage from an existing PebbleDB.
var NewTimeSeriesStorage = timeseries.New

// NewTimeSeriesStorageWithDir creates a new time series storage at the given directory.
var NewTimeSeriesStorageWithDir = timeseries.NewWithDir

// TimeSeriesDataPoint holds a single time series data point.
type TimeSeriesDataPoint = timeseries.DataPoint

// TimeSeriesQueryRequest describes a time series query.
type TimeSeriesQueryRequest = timeseries.QueryRequest

// TimeSeriesQueryResult holds the result of a time series query.
type TimeSeriesQueryResult = timeseries.QueryResult

// TimeSeriesGroupedResult holds a grouped time series result.
type TimeSeriesGroupedResult = timeseries.GroupedResult

// TimeSeriesDeleteRequest describes a time series deletion request.
type TimeSeriesDeleteRequest = timeseries.DeleteRequest

// TimeSeriesRetentionPolicy configures data retention for a metric.
type TimeSeriesRetentionPolicy = timeseries.RetentionPolicy

// TimeSeriesAggFunc identifies an aggregation function.
type TimeSeriesAggFunc = timeseries.AggFunc
