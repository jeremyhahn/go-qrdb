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

package service

import (
	"context"
	"errors"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// TimeSeriesServicer is an alias for the canonical timeseries.TimeSeriesServicer interface.
type TimeSeriesServicer = timeseries.TimeSeriesServicer

// TimeSeriesService implements TimeSeriesServicer backed by a QuicRaft
// host running an MVCCStateMachine with an attached time-series storage
// engine.
type TimeSeriesService struct {
	host    Host
	shardID uint64
}

// NewTimeSeriesService creates a new TimeSeriesServicer that operates on
// the specified shard. The shard must be running an MVCCStateMachine
// with attached time-series storage for operations to succeed.
func NewTimeSeriesService(host Host, shardID uint64) TimeSeriesServicer {
	return &TimeSeriesService{
		host:    host,
		shardID: shardID,
	}
}

// Write stores a single time-series data point through Raft consensus.
func (s *TimeSeriesService) Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	cmd := protocol.EncodeTSWrite(metric, timestamp.UnixNano(), value, tags)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// WriteBatch stores multiple time-series data points atomically through
// Raft consensus.
func (s *TimeSeriesService) WriteBatch(ctx context.Context, points []DataPoint) error {
	params := make([]protocol.TSWriteParams, len(points))
	for i, p := range points {
		params[i] = protocol.TSWriteParams{
			Metric:    p.Metric,
			Timestamp: p.Timestamp.UnixNano(),
			Value:     p.Value,
			Tags:      p.Tags,
		}
	}
	cmd := protocol.EncodeTSWriteBatch(params)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// Query retrieves data points for a metric within the given time range.
func (s *TimeSeriesService) Query(ctx context.Context, metric string, start, end time.Time, opts ...TSQueryOption) ([]DataPoint, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, TSQueryRequest{
		Metric: metric,
		Start:  start,
		End:    end,
		Opts:   opts,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	points, ok := result.([]DataPoint)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return points, nil
}

// Aggregate computes an aggregation function over data points for a
// metric within the given time range, grouped by the specified interval.
func (s *TimeSeriesService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn AggFunc, interval time.Duration) ([]AggResult, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, TSAggregateRequest{
		Metric:   metric,
		Start:    start,
		End:      end,
		Func:     fn,
		Interval: interval,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	results, ok := result.([]AggResult)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return results, nil
}

// WritePoint stores a single validated data point through Raft consensus.
func (s *TimeSeriesService) WritePoint(ctx context.Context, point *DataPoint) error {
	cmd := protocol.EncodeTSWritePoint(point.Metric, point.Timestamp.UnixNano(), point.Value, point.Tags)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// QueryRich performs a rich query with filtering, aggregation,
// downsampling, and grouping through linearizable read.
func (s *TimeSeriesService) QueryRich(ctx context.Context, req *QueryRequest) (*QueryResult, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, TSRichQueryRequest{
		Request: req,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	qr, ok := result.(*QueryResult)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return qr, nil
}

// DeleteSeries deletes data points matching the request criteria through
// Raft consensus.
func (s *TimeSeriesService) DeleteSeries(ctx context.Context, req *DeleteRequest) error {
	var startNs, endNs int64
	if !req.StartTime.IsZero() {
		startNs = req.StartTime.UnixNano()
	}
	if !req.EndTime.IsZero() {
		endNs = req.EndTime.UnixNano()
	}
	cmd := protocol.EncodeTSDeleteSeries(req.Metric, startNs, endNs, req.Tags)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// GetRetentionPolicy retrieves the retention policy for a metric through
// linearizable read.
func (s *TimeSeriesService) GetRetentionPolicy(metric string) (*RetentionPolicy, error) {
	result, err := s.host.SyncRead(context.Background(), s.shardID, TSGetRetentionRequest{
		Metric: metric,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	policy, ok := result.(*RetentionPolicy)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return policy, nil
}

// SetRetentionPolicy sets the retention policy for a metric through
// Raft consensus.
func (s *TimeSeriesService) SetRetentionPolicy(metric string, policy *RetentionPolicy) error {
	cmd := protocol.EncodeTSSetRetention(
		metric,
		int64(policy.Duration),
		int64(policy.Resolution),
		byte(policy.Aggregation),
	)
	_, err := s.host.SyncPropose(context.Background(), s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// ListMetrics returns metric names matching the optional prefix through
// linearizable read.
func (s *TimeSeriesService) ListMetrics(prefix string, limit int) ([]string, error) {
	result, err := s.host.SyncRead(context.Background(), s.shardID, TSListMetricsRequest{
		Prefix: prefix,
		Limit:  limit,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	metrics, ok := result.([]string)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return metrics, nil
}

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *TimeSeriesService) translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// translateReadError converts QuicRaft read errors to service-level
// typed errors.
func (s *TimeSeriesService) translateReadError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ReadError{Err: err}
}
