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

import (
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Time series storage types
// ---------------------------------------------------------------------------

// TimeSeriesStorage is a PebbleDB-backed time series storage engine.
type TimeSeriesStorage = timeseries.Storage

// TimeSeriesStorageOption configures a PebbleDB-backed time series storage instance.
type TimeSeriesStorageOption = timeseries.StorageOption

// NewTimeSeriesStorage creates a new time series storage from an existing PebbleDB.
var NewTimeSeriesStorage = timeseries.New

// NewTimeSeriesStorageWithDir creates a new time series storage at the given directory.
// Optional TimeSeriesStorageOption values configure the underlying PebbleDB instance.
var NewTimeSeriesStorageWithDir = timeseries.NewWithDir

// NewTimeSeriesStorageWithBarrier wraps the time series Pebble FS with the
// given StorageBarrier so all on-disk files are encrypted at rest. The barrier
// must be unsealed before calling this constructor.
//
// Deprecated: prefer NewTimeSeriesStorageWithEncryptor, which routes through
// the single atrest.Encryptor authority. Retained for backward compatibility.
func NewTimeSeriesStorageWithBarrier(dataDir string, barrier *seal.StorageBarrier) (*timeseries.Storage, error) {
	//nolint:staticcheck // SA1019: the barrier form is this constructor's whole contract.
	return timeseries.NewWithDir(dataDir, timeseries.WithBarrier(barrier))
}

// NewTimeSeriesStorageWithEncryptor opens a time series store at dataDir
// with at-rest encryption driven by enc. A nil enc or a no-op enc opens
// the store in plaintext mode, preserving backward compatibility.
func NewTimeSeriesStorageWithEncryptor(dataDir string, enc *atrest.Encryptor) (*timeseries.Storage, error) {
	return timeseries.NewWithDir(dataDir, timeseries.WithEncryptor(enc))
}

// TimeSeriesWithBarrier returns a TimeSeriesStorageOption that wraps the
// Pebble vfs.FS with seal/encfs. A nil barrier is a no-op.
//
// Deprecated: prefer TimeSeriesWithEncryptor(atrest.New(barrier)), which routes
// through the single atrest.Encryptor authority. Retained for backward
// compatibility.
//
//nolint:staticcheck // SA1019: re-exporting the deprecated option is this var's purpose.
var TimeSeriesWithBarrier = timeseries.WithBarrier

// TimeSeriesWithEncryptor returns a TimeSeriesStorageOption that wraps the
// Pebble vfs.FS using the shared atrest.Encryptor. A nil or no-op Encryptor
// leaves the FS untouched.
var TimeSeriesWithEncryptor = timeseries.WithEncryptor

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
