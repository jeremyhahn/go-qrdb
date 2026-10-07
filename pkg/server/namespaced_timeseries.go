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

package server

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// namespacedTimeSeriesService wraps a TimeSeriesServicer so that the metric
// name of every operation is transparently prefixed with the composite
// (tenant, db) namespace and stripped from returned data points and metric
// listings. Metric names are the keyspace for the time-series engine, so
// prefixing the metric isolates each database's series.
type namespacedTimeSeriesService struct {
	dbGuard
	inner  service.TimeSeriesServicer
	prefix string
}

// Compile-time interface check.
var _ service.TimeSeriesServicer = (*namespacedTimeSeriesService)(nil)

// newNamespacedTimeSeriesService creates a TimeSeriesServicer that prefixes all
// metric names with the given composite namespace prefix.
func newNamespacedTimeSeriesService(inner service.TimeSeriesServicer, prefix string) *namespacedTimeSeriesService {
	return &namespacedTimeSeriesService{inner: inner, prefix: prefix}
}

// prefixPoints returns a copy of points with each metric name prefixed.
func (s *namespacedTimeSeriesService) prefixPoints(points []timeseries.DataPoint) []timeseries.DataPoint {
	out := make([]timeseries.DataPoint, len(points))
	for i, p := range points {
		p.Metric = s.prefix + p.Metric
		out[i] = p
	}
	return out
}

// stripPoints returns a copy of points with each metric name stripped.
func (s *namespacedTimeSeriesService) stripPoints(points []timeseries.DataPoint) []timeseries.DataPoint {
	out := make([]timeseries.DataPoint, len(points))
	for i, p := range points {
		p.Metric = stripStringPrefix(p.Metric, s.prefix)
		out[i] = p
	}
	return out
}

// stripPointPtrs strips the metric name from each non-nil data point pointer.
func (s *namespacedTimeSeriesService) stripPointPtrs(points []*timeseries.DataPoint) []*timeseries.DataPoint {
	if points == nil {
		return nil
	}
	out := make([]*timeseries.DataPoint, len(points))
	for i, p := range points {
		if p == nil {
			continue
		}
		cp := *p
		cp.Metric = stripStringPrefix(p.Metric, s.prefix)
		out[i] = &cp
	}
	return out
}

// Write stores a single data point under the prefixed metric.
func (s *namespacedTimeSeriesService) Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	ctx = s.scope(ctx)
	return s.inner.Write(ctx, s.prefix+metric, value, timestamp, tags)
}

// WriteBatch stores multiple data points under prefixed metrics.
func (s *namespacedTimeSeriesService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	ctx = s.scope(ctx)
	return s.inner.WriteBatch(ctx, s.prefixPoints(points))
}

// Query retrieves data points for a prefixed metric, stripping the prefix from
// the returned points.
func (s *namespacedTimeSeriesService) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	points, err := s.inner.Query(ctx, s.prefix+metric, start, end, opts...)
	if err != nil {
		return nil, err
	}
	return s.stripPoints(points), nil
}

// QueryWithConsistency is Query with an explicit read-consistency level.
func (s *namespacedTimeSeriesService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	points, err := s.inner.QueryWithConsistency(ctx, s.prefix+metric, start, end, ropts, opts...)
	if err != nil {
		return nil, err
	}
	return s.stripPoints(points), nil
}

// Aggregate computes an aggregation over a prefixed metric. Aggregation results
// carry no metric name, so they are returned unchanged.
func (s *namespacedTimeSeriesService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	return s.inner.Aggregate(ctx, s.prefix+metric, start, end, fn, interval)
}

// AggregateWithConsistency is Aggregate with an explicit read-consistency level.
func (s *namespacedTimeSeriesService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return s.inner.AggregateWithConsistency(ctx, s.prefix+metric, start, end, fn, interval, ropts)
}

// WritePoint stores a single validated data point under the prefixed metric.
func (s *namespacedTimeSeriesService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	ctx = s.scope(ctx)
	if point == nil {
		return s.inner.WritePoint(ctx, point)
	}
	cp := *point
	cp.Metric = s.prefix + point.Metric
	return s.inner.WritePoint(ctx, &cp)
}

// prefixQueryRequest returns a copy of req with the metric name prefixed.
func (s *namespacedTimeSeriesService) prefixQueryRequest(req *timeseries.QueryRequest) *timeseries.QueryRequest {
	if req == nil {
		return nil
	}
	cp := *req
	cp.Metric = s.prefix + req.Metric
	return &cp
}

// stripQueryResult returns a copy of result with the metric name and point
// metrics stripped.
func (s *namespacedTimeSeriesService) stripQueryResult(result *timeseries.QueryResult) *timeseries.QueryResult {
	if result == nil {
		return nil
	}
	cp := *result
	cp.Metric = stripStringPrefix(result.Metric, s.prefix)
	cp.Points = s.stripPointPtrs(result.Points)
	if result.GroupedResults != nil {
		groups := make([]*timeseries.GroupedResult, len(result.GroupedResults))
		for i, g := range result.GroupedResults {
			if g == nil {
				continue
			}
			gc := *g
			gc.Points = s.stripPointPtrs(g.Points)
			groups[i] = &gc
		}
		cp.GroupedResults = groups
	}
	return &cp
}

// QueryRich performs a rich query against a prefixed metric.
func (s *namespacedTimeSeriesService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	result, err := s.inner.QueryRich(ctx, s.prefixQueryRequest(req))
	if err != nil {
		return nil, err
	}
	return s.stripQueryResult(result), nil
}

// QueryRichWithConsistency is QueryRich with an explicit read-consistency level.
func (s *namespacedTimeSeriesService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	result, err := s.inner.QueryRichWithConsistency(ctx, s.prefixQueryRequest(req), ropts)
	if err != nil {
		return nil, err
	}
	return s.stripQueryResult(result), nil
}

// DeleteSeries deletes data points for a prefixed metric.
func (s *namespacedTimeSeriesService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	ctx = s.scope(ctx)
	if req == nil {
		return s.inner.DeleteSeries(ctx, req)
	}
	cp := *req
	cp.Metric = s.prefix + req.Metric
	return s.inner.DeleteSeries(ctx, &cp)
}

// GetRetentionPolicy retrieves the retention policy for a prefixed metric.
func (s *namespacedTimeSeriesService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	return s.inner.GetRetentionPolicy(s.prefix + metric)
}

// SetRetentionPolicy sets the retention policy for a prefixed metric.
func (s *namespacedTimeSeriesService) SetRetentionPolicy(ctx context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	return s.inner.SetRetentionPolicy(s.scope(ctx), s.prefix+metric, policy)
}

func (s *namespacedTimeSeriesService) ApplyRetention(ctx context.Context, metric string, cutoff time.Time) error {
	return s.inner.ApplyRetention(s.scope(ctx), s.prefix+metric, cutoff)
}

// ListMetrics returns metric names matching the prefixed search prefix, with
// the namespace prefix stripped from the results.
func (s *namespacedTimeSeriesService) ListMetrics(prefix string, limit int) ([]string, error) {
	names, err := s.inner.ListMetrics(s.prefix+prefix, limit)
	if err != nil {
		return nil, err
	}
	return stripStringSlicePrefix(names, s.prefix), nil
}

// ListMetricsWithConsistency is ListMetrics with an explicit read-consistency
// level.
func (s *namespacedTimeSeriesService) ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	names, err := s.inner.ListMetricsWithConsistency(ctx, s.prefix+prefix, limit, ropts)
	if err != nil {
		return nil, err
	}
	return stripStringSlicePrefix(names, s.prefix), nil
}
