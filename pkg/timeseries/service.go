//go:build qrdb_ts

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
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

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
