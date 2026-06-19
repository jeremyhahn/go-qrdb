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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// quotaTimeSeriesService wraps a TimeSeriesServicer to enforce a tenant's
// storage-byte quota. Time-series points are independent tenant payload written
// outside the KV/MVCC servicers, so each ingest is admitted through the
// Accountant first: an ingest that would push the tenant over its
// MaxStorageBytes is rejected with *quota.ExceededError before it reaches
// consensus, and the reserved bytes are released if the write fails downstream.
// All query, aggregation, retention, and metric-listing operations embed the
// inner servicer and pass straight through with no accounting.
//
// Point size measure: each data point is accounted as
//
//	len(metric) + tsPointFixedBytes + sum(len(tagKey)+len(tagValue))
//
// where tsPointFixedBytes covers the 8-byte float64 value and 8-byte int64
// timestamp the engine persists per point. This mirrors the per-point keyspace
// the time-series engine writes (metric + tags compose the key, value+timestamp
// the record) without an extra read on the hot path. Consistent with the KV
// quota path, overwrites are counted by full point size rather than a delta and
// DeleteSeries is not credited; the background reconciler corrects the persisted
// counter for volume tenants.
type quotaTimeSeriesService struct {
	service.TimeSeriesServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface check.
var _ service.TimeSeriesServicer = (*quotaTimeSeriesService)(nil)

// newQuotaTimeSeriesService wraps inner with storage-byte quota enforcement for
// the given tenant and limit (MaxStorageBytes).
func newQuotaTimeSeriesService(inner service.TimeSeriesServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaTimeSeriesService {
	return &quotaTimeSeriesService{
		TimeSeriesServicer: inner,
		acct:               acct,
		tenantID:           tenantID,
		limit:              limit,
	}
}

// tsPointFixedBytes is the per-point fixed payload the engine persists: an
// 8-byte float64 value plus an 8-byte int64 (nanosecond) timestamp.
const tsPointFixedBytes = 16

// tsPointBytes returns the accounted byte size of a single data point with the
// given metric name and tags.
func tsPointBytes(metric string, tags map[string]string) int {
	n := tsPointFixedBytes + len(metric)
	for k, v := range tags {
		n += len(k) + len(v)
	}
	return n
}

// reserve admits n bytes against the tenant's quota.
func (s *quotaTimeSeriesService) reserve(ctx context.Context, n int) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, int64(n))
}

// Write admits the point's bytes against the quota, then forwards.
func (s *quotaTimeSeriesService) Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	n := tsPointBytes(metric, tags)
	if err := s.reserve(ctx, n); err != nil {
		return err
	}
	if err := s.TimeSeriesServicer.Write(ctx, metric, value, timestamp, tags); err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return err
	}
	return nil
}

// WriteBatch admits the sum of the batch's point bytes against the quota, then
// forwards.
func (s *quotaTimeSeriesService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	var n int
	for i := range points {
		n += tsPointBytes(points[i].Metric, points[i].Tags)
	}
	if err := s.reserve(ctx, n); err != nil {
		return err
	}
	if err := s.TimeSeriesServicer.WriteBatch(ctx, points); err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return err
	}
	return nil
}

// WritePoint admits the point's bytes against the quota, then forwards.
func (s *quotaTimeSeriesService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if point == nil {
		return s.TimeSeriesServicer.WritePoint(ctx, point)
	}
	n := tsPointBytes(point.Metric, point.Tags)
	if err := s.reserve(ctx, n); err != nil {
		return err
	}
	if err := s.TimeSeriesServicer.WritePoint(ctx, point); err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return err
	}
	return nil
}
