//go:build !qrdb_ts

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

// Package timeseries provides time-series data storage and aggregation built
// on PebbleDB. This stub version is compiled when time-series support is
// disabled: the pure data and wire types remain real definitions so untagged
// consumers keep compiling and marshaling identically, while every storage
// operation returns ErrNotCompiled.
package timeseries

import (
	"context"
	"errors"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ErrNotCompiled is returned by every storage operation when time-series
// support is not compiled in.
var ErrNotCompiled = errors.New("timeseries support not compiled: build with -tags qrdb_ts")

// ---------------------------------------------------------------------------
// Data types (real definitions — identical to the qrdb_ts build)
// ---------------------------------------------------------------------------

// DataPoint represents a single time-series measurement.
type DataPoint struct {
	// Metric is the metric name (e.g., "cpu.usage", "request.latency").
	Metric string `json:"metric" msgpack:"metric"`

	// Value is the measured value.
	Value float64 `json:"value" msgpack:"value"`

	// Timestamp is the measurement time.
	Timestamp time.Time `json:"timestamp" msgpack:"timestamp"`

	// Tags are optional key-value labels for filtering and grouping.
	Tags map[string]string `json:"tags,omitempty" msgpack:"tags,omitempty"`
}

// Validate checks that a data point has a non-empty metric and non-zero
// timestamp.
func (dp *DataPoint) Validate() error {
	if dp.Metric == "" {
		return &InvalidMetricError{}
	}
	if dp.Timestamp.IsZero() {
		return &InvalidTimestampError{}
	}
	return nil
}

// AggFunc identifies an aggregation function for time-series queries.
type AggFunc uint8

const (
	// AggNone represents no aggregation (zero value sentinel).
	AggNone AggFunc = 0

	// AggAvg computes the arithmetic mean.
	AggAvg AggFunc = 1

	// AggSum computes the sum.
	AggSum AggFunc = 2

	// AggMin finds the minimum value.
	AggMin AggFunc = 3

	// AggMax finds the maximum value.
	AggMax AggFunc = 4

	// AggCount counts the number of data points.
	AggCount AggFunc = 5

	// AggPercentile computes a percentile value.
	AggPercentile AggFunc = 6

	// AggFirst returns the first value in the interval.
	AggFirst AggFunc = 7

	// AggLast returns the last value in the interval.
	AggLast AggFunc = 8
)

// IsValid checks if the aggregation function is a known value.
func (f AggFunc) IsValid() bool {
	return f >= AggAvg && f <= AggLast
}

// String returns the human-readable name of the aggregation function.
func (f AggFunc) String() string {
	names := map[AggFunc]string{
		AggNone:       "none",
		AggAvg:        "avg",
		AggSum:        "sum",
		AggMin:        "min",
		AggMax:        "max",
		AggCount:      "count",
		AggPercentile: "percentile",
		AggFirst:      "first",
		AggLast:       "last",
	}
	if name, ok := names[f]; ok {
		return name
	}
	return "unknown"
}

// AggResult is the result of an aggregation over a time interval.
type AggResult struct {
	// Timestamp marks the start of the aggregation interval.
	Timestamp time.Time

	// Value is the computed aggregation value.
	Value float64

	// Count is the number of data points in the interval.
	Count int64
}

// RetentionPolicy defines how long data is retained and downsampling rules.
type RetentionPolicy struct {
	Duration    time.Duration `json:"duration" msgpack:"duration"`
	Resolution  time.Duration `json:"resolution,omitempty" msgpack:"resolution,omitempty"`
	Aggregation AggFunc       `json:"aggregation,omitempty" msgpack:"aggregation,omitempty"`
}

// QueryRequest represents a rich time series query with filtering,
// aggregation, downsampling, and grouping.
type QueryRequest struct {
	Metric      string            `json:"metric" msgpack:"metric"`
	Tags        map[string]string `json:"tags,omitempty" msgpack:"tags,omitempty"`
	StartTime   time.Time         `json:"start_time" msgpack:"start_time"`
	EndTime     time.Time         `json:"end_time" msgpack:"end_time"`
	Aggregation AggFunc           `json:"aggregation,omitempty" msgpack:"aggregation,omitempty"`
	Interval    time.Duration     `json:"interval,omitempty" msgpack:"interval,omitempty"`
	GroupBy     []string          `json:"group_by,omitempty" msgpack:"group_by,omitempty"`
	Limit       int               `json:"limit,omitempty" msgpack:"limit,omitempty"`
	Percentile  float64           `json:"percentile,omitempty" msgpack:"percentile,omitempty"`
}

// Validate checks if the query request is valid.
func (qr *QueryRequest) Validate() error {
	if qr.Metric == "" {
		return &InvalidMetricError{}
	}
	if qr.StartTime.IsZero() {
		return &InvalidTimestampError{}
	}
	if qr.EndTime.IsZero() {
		return &InvalidTimestampError{}
	}
	if qr.EndTime.Before(qr.StartTime) {
		return &InvalidTimeRangeError{Start: qr.StartTime, End: qr.EndTime}
	}
	if qr.Aggregation != AggNone && !qr.Aggregation.IsValid() {
		return &InvalidAggFuncError{Func: qr.Aggregation}
	}
	if qr.Aggregation == AggPercentile {
		if qr.Percentile < 0 || qr.Percentile > 100 {
			return &ValidationError{Op: "QueryRequest.Validate", Message: "percentile must be between 0 and 100"}
		}
	}
	return nil
}

// QueryResult represents aggregated query results.
type QueryResult struct {
	Metric          string            `json:"metric" msgpack:"metric"`
	Tags            map[string]string `json:"tags,omitempty" msgpack:"tags,omitempty"`
	Points          []*DataPoint      `json:"points" msgpack:"points"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty" msgpack:"aggregated_value,omitempty"`
	GroupedResults  []*GroupedResult  `json:"grouped_results,omitempty" msgpack:"grouped_results,omitempty"`
}

// GroupedResult represents query results grouped by tags.
type GroupedResult struct {
	Tags            map[string]string `json:"tags" msgpack:"tags"`
	Points          []*DataPoint      `json:"points,omitempty" msgpack:"points,omitempty"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty" msgpack:"aggregated_value,omitempty"`
}

// WriteRequest represents a request to write data points.
type WriteRequest struct {
	Points []*DataPoint `json:"points" msgpack:"points"`
}

// Validate checks if the write request is valid.
func (wr *WriteRequest) Validate() error {
	if len(wr.Points) == 0 {
		return &ValidationError{Op: "WriteRequest.Validate", Message: "no data points to write"}
	}
	for i, point := range wr.Points {
		if err := point.Validate(); err != nil {
			return &InvalidBatchPointError{Index: i, Err: err}
		}
	}
	return nil
}

// DeleteRequest represents a request to delete a time series.
type DeleteRequest struct {
	Metric    string            `json:"metric" msgpack:"metric"`
	Tags      map[string]string `json:"tags,omitempty" msgpack:"tags,omitempty"`
	StartTime time.Time         `json:"start_time,omitempty" msgpack:"start_time,omitempty"`
	EndTime   time.Time         `json:"end_time,omitempty" msgpack:"end_time,omitempty"`
}

// Validate checks if the delete request is valid.
func (dr *DeleteRequest) Validate() error {
	if dr.Metric == "" {
		return &InvalidMetricError{}
	}
	if !dr.StartTime.IsZero() && !dr.EndTime.IsZero() && dr.EndTime.Before(dr.StartTime) {
		return &InvalidTimeRangeError{Start: dr.StartTime, End: dr.EndTime}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Query options
// ---------------------------------------------------------------------------

// TSQueryOptions holds the resolved configuration for a time-series query.
type TSQueryOptions struct {
	Tags       map[string]string
	Limit      int
	Descending bool
}

// TSQueryOption configures a time-series query.
type TSQueryOption func(*TSQueryOptions)

// WithTags filters time-series results to data points matching all
// specified tags.
func WithTags(tags map[string]string) TSQueryOption {
	return func(o *TSQueryOptions) {
		o.Tags = tags
	}
}

// WithTSLimit sets the maximum number of data points to return.
func WithTSLimit(limit int) TSQueryOption {
	return func(o *TSQueryOptions) {
		o.Limit = limit
	}
}

// WithDescending returns data points in reverse chronological order.
func WithDescending() TSQueryOption {
	return func(o *TSQueryOptions) {
		o.Descending = true
	}
}

// ApplyTSQueryOptions resolves variadic TSQueryOption values into a
// TSQueryOptions struct.
func ApplyTSQueryOptions(opts []TSQueryOption) TSQueryOptions {
	var tso TSQueryOptions
	for _, opt := range opts {
		opt(&tso)
	}
	return tso
}

// ---------------------------------------------------------------------------
// Query request types for SyncRead/Lookup dispatch
// ---------------------------------------------------------------------------

// TSQueryRequest queries time-series data points for a metric.
type TSQueryRequest struct {
	Metric string
	Start  time.Time
	End    time.Time
	Opts   []TSQueryOption
}

// TSAggregateRequest computes an aggregation over time-series data points.
type TSAggregateRequest struct {
	Metric   string
	Start    time.Time
	End      time.Time
	Func     AggFunc
	Interval time.Duration
}

// TSRichQueryRequest performs a rich query via SyncRead/Lookup dispatch.
type TSRichQueryRequest struct {
	Request *QueryRequest
}

// TSGetRetentionRequest retrieves the retention policy for a metric
// via SyncRead/Lookup dispatch.
type TSGetRetentionRequest struct {
	Metric string
}

// TSListMetricsRequest lists metric names via SyncRead/Lookup dispatch.
type TSListMetricsRequest struct {
	Prefix string
	Limit  int
}

// ---------------------------------------------------------------------------
// Servicer interface (identical to the qrdb_ts build)
// ---------------------------------------------------------------------------

// TimeSeriesServicer provides time-series data storage and aggregation
// over the key-value store. Data points are stored using composite keys
// that enable efficient time-range queries and aggregation.
type TimeSeriesServicer interface {
	// Write stores a single time-series data point.
	Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error

	// WriteBatch stores multiple time-series data points atomically.
	WriteBatch(ctx context.Context, points []DataPoint) error

	// Query retrieves data points for a metric within the given time
	// range [start, end).
	Query(ctx context.Context, metric string, start, end time.Time, opts ...TSQueryOption) ([]DataPoint, error)

	// QueryWithConsistency is Query with an explicit read-consistency level.
	QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...TSQueryOption) ([]DataPoint, error)

	// Aggregate computes an aggregation function over data points for
	// a metric within the given time range, grouped by the specified
	// interval.
	Aggregate(ctx context.Context, metric string, start, end time.Time, fn AggFunc, interval time.Duration) ([]AggResult, error)

	// AggregateWithConsistency is Aggregate with an explicit read-consistency
	// level.
	AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]AggResult, error)

	// WritePoint stores a single validated DataPoint.
	WritePoint(ctx context.Context, point *DataPoint) error

	// QueryRich performs a rich query with filtering, aggregation, downsampling, and grouping.
	QueryRich(ctx context.Context, req *QueryRequest) (*QueryResult, error)

	// QueryRichWithConsistency is QueryRich with an explicit read-consistency
	// level.
	QueryRichWithConsistency(ctx context.Context, req *QueryRequest, ropts consistency.ReadOptions) (*QueryResult, error)

	// DeleteSeries deletes data points matching the request criteria.
	DeleteSeries(ctx context.Context, req *DeleteRequest) error

	// GetRetentionPolicy retrieves the retention policy for a metric.
	GetRetentionPolicy(metric string) (*RetentionPolicy, error)

	// SetRetentionPolicy sets the retention policy for a metric.
	SetRetentionPolicy(metric string, policy *RetentionPolicy) error

	// ListMetrics returns metric names matching the optional prefix.
	ListMetrics(prefix string, limit int) ([]string, error)

	// ListMetricsWithConsistency is ListMetrics with an explicit
	// read-consistency level.
	ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error)
}

// ---------------------------------------------------------------------------
// Storage stub
// ---------------------------------------------------------------------------

// Storage provides time-series data storage and aggregation over PebbleDB.
// In this stub build the constructor succeeds so that shared-PebbleDB store
// wiring keeps working, but every storage operation returns ErrNotCompiled.
type Storage struct{}

// New creates a new time-series Storage backed by the given PebbleDB instance.
// Returns a NilDBError if db is nil.
func New(db *pebble.DB) (*Storage, error) {
	if db == nil {
		return nil, &NilDBError{}
	}
	return &Storage{}, nil
}

// StorageOption configures a new Storage instance created by NewWithDir or
// NewWithBarrier. Options are applied to the pebble.Options before the
// database is opened.
type StorageOption func(*pebble.Options)

// WithBarrier wraps the Pebble vfs.FS with seal/encfs so that every file
// the time-series store writes is transparently encrypted under the supplied
// StorageBarrier. In this stub build the option is a no-op.
//
// Deprecated: prefer WithEncryptor(atrest.New(barrier)) which routes
// through the single atrest.Encryptor authority. WithBarrier is kept for
// backward compatibility.
func WithBarrier(barrier *seal.StorageBarrier) StorageOption {
	return WithEncryptor(atrest.New(barrier))
}

// WithEncryptor applies enc to the Pebble options, wrapping the vfs.FS
// with barrier-driven AES-256-GCM encryption. In this stub build the option
// is a no-op.
func WithEncryptor(enc *atrest.Encryptor) StorageOption {
	return func(o *pebble.Options) {}
}

// NewWithDir creates a new time-series Storage that owns its own PebbleDB
// instance rooted at dataDir.
func NewWithDir(dataDir string, opts ...StorageOption) (*Storage, error) {
	return nil, ErrNotCompiled
}

// DB returns the underlying PebbleDB instance.
func (s *Storage) DB() *pebble.DB {
	return nil
}

// Write stores a single data point in PebbleDB.
func (s *Storage) Write(metric string, value float64, timestamp time.Time, tags map[string]string) error {
	return ErrNotCompiled
}

// WritePoint writes a single DataPoint to storage after validation.
func (s *Storage) WritePoint(_ context.Context, point *DataPoint) error {
	return ErrNotCompiled
}

// WriteBatch atomically stores multiple data points using a PebbleDB batch.
func (s *Storage) WriteBatch(points []DataPoint) error {
	return ErrNotCompiled
}

// Query retrieves data points for a metric within the given time range.
func (s *Storage) Query(metric string, start, end time.Time, opts ...TSQueryOption) ([]DataPoint, error) {
	return nil, ErrNotCompiled
}

// QueryRich performs a rich query with aggregation, downsampling, and groupBy
// support.
func (s *Storage) QueryRich(_ context.Context, req *QueryRequest) (*QueryResult, error) {
	return nil, ErrNotCompiled
}

// DeleteSeries deletes data points matching the given DeleteRequest criteria.
func (s *Storage) DeleteSeries(_ context.Context, req *DeleteRequest) error {
	return ErrNotCompiled
}

// GetRetentionPolicy retrieves the retention policy for the given metric.
func (s *Storage) GetRetentionPolicy(metric string) (*RetentionPolicy, error) {
	return nil, ErrNotCompiled
}

// SetRetentionPolicy stores the retention policy for the given metric.
func (s *Storage) SetRetentionPolicy(metric string, policy *RetentionPolicy) error {
	return ErrNotCompiled
}

// ListMetrics returns the names of all indexed metrics whose names start with
// the given prefix.
func (s *Storage) ListMetrics(prefix string, limit int) ([]string, error) {
	return nil, ErrNotCompiled
}

// Aggregate computes an aggregation function over data points for a metric
// within the given time range, bucketed by the specified interval.
func (s *Storage) Aggregate(metric string, start, end time.Time, fn AggFunc, interval time.Duration) ([]AggResult, error) {
	return nil, ErrNotCompiled
}

// Close releases stub storage resources. It never fails.
func (s *Storage) Close() error {
	return nil
}
