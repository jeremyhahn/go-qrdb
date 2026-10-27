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

package timeseries

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Time series storage types
// ---------------------------------------------------------------------------

// TimeSeriesStorage is a storage-backed time series storage engine.
type TimeSeriesStorage = timeseries.Storage

// NewTimeSeriesStorage creates a new time series storage over an existing
// storage.Storage.
var NewTimeSeriesStorage = timeseries.New

// TimeSeriesDirStorage is a time series store over a dedicated PebbleDB it
// owns. Every write method of the embedded TimeSeriesStorage stages into a
// TimeSeriesBatch; Batch returns one over the owned database, which the
// caller commits. Close closes the time series store and then the database.
type TimeSeriesDirStorage struct {
	*timeseries.Storage
	stg storage.Storage
}

// Batch returns a write batch over the owned database.
func (d *TimeSeriesDirStorage) Batch() TimeSeriesBatch {
	return d.stg.Batch()
}

// Close closes the time series store and the database it owns, reporting
// both failures when both fail.
func (d *TimeSeriesDirStorage) Close() error {
	return errors.Join(d.Storage.Close(), d.stg.Close())
}

// NewTimeSeriesStorageWithDir opens a dedicated PebbleDB-backed time series
// store rooted at dataDir, for a standalone embedder that wants time-series
// storage without the rest of the qrdb node/engine stack. The qrdb server
// itself never calls this: its time-series storage rides the same
// storage.Storage the database's own engine is built over (see
// pkg/server/lifecycle.NewLocalEngine), so a time-series write lands in the
// same apply batch as everything else the entry does. This is equivalent to
// NewTimeSeriesStorageWithEncryptor(dataDir, nil).
func NewTimeSeriesStorageWithDir(dataDir string) (*TimeSeriesDirStorage, error) {
	return NewTimeSeriesStorageWithEncryptor(dataDir, nil)
}

// NewTimeSeriesStorageWithEncryptor opens a dedicated PebbleDB-backed time
// series store at dataDir with at-rest encryption driven by enc. A nil enc
// or a no-op enc opens the store in plaintext mode.
func NewTimeSeriesStorageWithEncryptor(dataDir string, enc *atrest.Encryptor) (*TimeSeriesDirStorage, error) {
	if dataDir == "" {
		return nil, &timeseries.EmptyDirError{}
	}
	cfg := pebblestore.DefaultConfig(dataDir)
	if enc != nil {
		cfg.FS = enc.FS()
	}
	pstg, err := pebblestore.New(cfg)
	if err != nil {
		return nil, &timeseries.OpenDirError{Dir: dataDir, Err: err}
	}
	ts, err := timeseries.New(pstg)
	if err != nil {
		return nil, errors.Join(err, pstg.Close())
	}
	return &TimeSeriesDirStorage{Storage: ts, stg: pstg}, nil
}

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

// TimeSeriesBatch is the atomic write handle every time-series write method
// stages into; the caller commits it.
type TimeSeriesBatch = storage.Batch
