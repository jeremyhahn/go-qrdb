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

package transport

// TSTag is a key-value label for time-series data.
type TSTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TSDataPoint is a single time-series measurement.
type TSDataPoint struct {
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	TimestampNs int64   `json:"timestamp_ns"`
	Tags        []TSTag `json:"tags,omitempty"`
}

// TSQueryRequest retrieves data points for a metric within a time range.
type TSQueryRequest struct {
	Metric     string  `json:"metric"`
	StartNs    int64   `json:"start_ns"`
	EndNs      int64   `json:"end_ns"`
	Tags       []TSTag `json:"tags,omitempty"`
	Limit      int32   `json:"limit,omitempty"`
	Descending bool    `json:"descending,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// TSQueryResponse returns matching data points.
type TSQueryResponse struct {
	Points []TSDataPoint `json:"points"`
}

// AggFunc identifies an aggregation function.
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

// TSAggregateRequest computes an aggregation over data points.
type TSAggregateRequest struct {
	Metric     string  `json:"metric"`
	StartNs    int64   `json:"start_ns"`
	EndNs      int64   `json:"end_ns"`
	Function   AggFunc `json:"function"`
	IntervalNs int64   `json:"interval_ns,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// TSAggResult is an aggregation result for a single interval.
type TSAggResult struct {
	TimestampNs int64   `json:"timestamp_ns"`
	Value       float64 `json:"value"`
	Count       int64   `json:"count"`
}

// TSAggregateResponse returns the aggregation results.
type TSAggregateResponse struct {
	Results []TSAggResult `json:"results"`
}

// TSRichQueryRequest performs a rich query with aggregation, downsampling, and grouping.
type TSRichQueryRequest struct {
	Metric      string   `json:"metric"`
	StartNs     int64    `json:"start_ns"`
	EndNs       int64    `json:"end_ns"`
	Tags        []TSTag  `json:"tags,omitempty"`
	Aggregation AggFunc  `json:"aggregation,omitempty"`
	IntervalNs  int64    `json:"interval_ns,omitempty"`
	GroupBy     []string `json:"group_by,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Percentile  float64  `json:"percentile,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// TSRichQueryResponse returns the results of a rich query.
type TSRichQueryResponse struct {
	Metric          string            `json:"metric"`
	Tags            []TSTag           `json:"tags,omitempty"`
	Points          []TSDataPoint     `json:"points"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
	GroupedResults  []TSGroupedResult `json:"grouped_results,omitempty"`
}

// TSGroupedResult represents query results grouped by tags.
type TSGroupedResult struct {
	Tags            []TSTag       `json:"tags"`
	Points          []TSDataPoint `json:"points,omitempty"`
	AggregatedValue *float64      `json:"aggregated_value,omitempty"`
}

// TSDeleteRequest deletes time-series data matching the criteria.
type TSDeleteRequest struct {
	Metric  string  `json:"metric"`
	Tags    []TSTag `json:"tags,omitempty"`
	StartNs int64   `json:"start_ns,omitempty"`
	EndNs   int64   `json:"end_ns,omitempty"`
}

// TSDeleteResponse is the response for a delete operation.
type TSDeleteResponse struct{}

// TSRetentionPolicy defines data retention and downsampling rules.
type TSRetentionPolicy struct {
	DurationNs   int64   `json:"duration_ns"`
	ResolutionNs int64   `json:"resolution_ns,omitempty"`
	Aggregation  AggFunc `json:"aggregation,omitempty"`
}

// TSGetRetentionRequest retrieves the retention policy for a metric.
type TSGetRetentionRequest struct {
	Metric string `json:"metric"`
}

// TSGetRetentionResponse returns the retention policy.
type TSGetRetentionResponse struct {
	Policy *TSRetentionPolicy `json:"policy"`
}

// TSSetRetentionRequest sets the retention policy for a metric.
type TSSetRetentionRequest struct {
	Metric string             `json:"metric"`
	Policy *TSRetentionPolicy `json:"policy"`
}

// TSListMetricsRequest lists metric names.
type TSListMetricsRequest struct {
	Prefix string `json:"prefix,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// TSListMetricsResponse returns metric names.
type TSListMetricsResponse struct {
	Metrics []string `json:"metrics"`
}
