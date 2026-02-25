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

import "time"

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

// DataPoint represents a single time-series measurement.
type DataPoint struct {
	// Metric is the metric name (e.g., "cpu.usage", "request.latency").
	Metric string

	// Value is the measured value.
	Value float64

	// Timestamp is the measurement time.
	Timestamp time.Time

	// Tags are optional key-value labels for filtering and grouping.
	Tags map[string]string
}

// AggFunc identifies an aggregation function for time-series queries.
type AggFunc uint8

const (
	// AggAvg computes the arithmetic mean.
	AggAvg AggFunc = iota

	// AggSum computes the sum.
	AggSum

	// AggMin finds the minimum value.
	AggMin

	// AggMax finds the maximum value.
	AggMax

	// AggCount counts the number of data points.
	AggCount

	// AggPercentile computes a percentile value.
	AggPercentile
)

// AggResult is the result of an aggregation over a time interval.
type AggResult struct {
	// Timestamp marks the start of the aggregation interval.
	Timestamp time.Time

	// Value is the computed aggregation value.
	Value float64

	// Count is the number of data points in the interval.
	Count int64
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
// Query request types
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
