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
