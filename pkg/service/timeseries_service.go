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
	"math"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
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

	// maxMetrics caps the metric names the shard holds
	// (timeseries.max_metrics); zero is unlimited. It travels in every write
	// command and the apply path enforces it, so it holds across every node
	// serving the shard and orders against deletes in the log.
	maxMetrics int
}

// TimeSeriesServiceOption configures a TimeSeriesService.
type TimeSeriesServiceOption func(*TimeSeriesService)

// WithMaxMetrics caps the metric names the shard holds at n
// (timeseries.max_metrics): a write that would create a metric past the cap
// is refused with *MetricLimitError, and a write to a metric that exists is
// never refused. Zero or less leaves the shard unlimited.
func WithMaxMetrics(n int) TimeSeriesServiceOption {
	return func(s *TimeSeriesService) { s.maxMetrics = n }
}

// NewTimeSeriesService creates a new TimeSeriesServicer that operates on
// the specified shard. The shard must be running an MVCCStateMachine
// with attached time-series storage for operations to succeed.
func NewTimeSeriesService(host Host, shardID uint64, opts ...TimeSeriesServiceOption) TimeSeriesServicer {
	s := &TimeSeriesService{
		host:    host,
		shardID: shardID,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Write stores a single time-series data point through Raft consensus.
func (s *TimeSeriesService) Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	cmd, encErr := protocol.EncodeTSWrite(metric, timestamp.UnixNano(), value, tags)
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	// The shard's metric cap travels in the command and the apply path
	// decides it against the replicated metric set (go-qrdb#1088).
	cmd = protocol.AppendTSMetricCap(cmd, gatedCommandCap(s.host, s.shardID, s.maxMetrics))
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. time-series
	// subsystem not configured, validation failure) that the SM placed in the
	// result payload instead of stalling consensus.
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
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
	cmd, encErr := protocol.EncodeTSWriteBatch(params)
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	// The shard's metric cap travels in the command and the apply path
	// decides it against the replicated metric set (go-qrdb#1088).
	cmd = protocol.AppendTSMetricCap(cmd, gatedCommandCap(s.host, s.shardID, s.maxMetrics))
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. time-series
	// subsystem not configured, validation failure) that the SM placed in the
	// result payload instead of stalling consensus.
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// commandCap is a configured shard cap as the command carries it: zero, no
// cap, for a non-positive n, and n clamped to the wire's uint32 range.
func commandCap(n int) uint32 {
	if n <= 0 {
		return 0
	}
	return uint32(min(uint64(n), math.MaxUint32))
}

// gatedCommandCap is the cap a command proposed to shardID carries: n as
// commandCap encodes it once featuregate.CommandCaps is active for the
// group, and zero, the encoding a binary from before the cap proposes,
// until then. Every replica enforces the cap its command carries, so a cap
// proposed while a replica that ignores it is still in the group would
// admit there what its peers refuse. A host that gates nothing (standalone,
// embedded) runs one binary and carries the cap at once.
func gatedCommandCap(host Host, shardID uint64, n int) uint32 {
	if n <= 0 {
		return 0
	}
	if gated, ok := host.(FeatureGatedHost); ok && !gated.FeatureEnabled(shardID, featuregate.CommandCaps) {
		return 0
	}
	return commandCap(n)
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

// QueryWithConsistency retrieves data points for a metric within the given
// time range using the specified read-consistency level.
func (s *TimeSeriesService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...TSQueryOption) ([]DataPoint, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, TSQueryRequest{
		Metric: metric,
		Start:  start,
		End:    end,
		Opts:   opts,
	}, ropts)
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

// AggregateWithConsistency computes an aggregation using the specified
// read-consistency level.
func (s *TimeSeriesService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]AggResult, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, TSAggregateRequest{
		Metric:   metric,
		Start:    start,
		End:      end,
		Func:     fn,
		Interval: interval,
	}, ropts)
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
	cmd, encErr := protocol.EncodeTSWritePoint(point.Metric, point.Timestamp.UnixNano(), point.Value, point.Tags)
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	// The shard's metric cap travels in the command and the apply path
	// decides it against the replicated metric set (go-qrdb#1088).
	cmd = protocol.AppendTSMetricCap(cmd, gatedCommandCap(s.host, s.shardID, s.maxMetrics))
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. time-series
	// subsystem not configured, validation failure) that the SM placed in the
	// result payload instead of stalling consensus.
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
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

// QueryRichWithConsistency performs a rich query using the specified
// read-consistency level.
func (s *TimeSeriesService) QueryRichWithConsistency(ctx context.Context, req *QueryRequest, ropts consistency.ReadOptions) (*QueryResult, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, TSRichQueryRequest{
		Request: req,
	}, ropts)
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
	cmd, encErr := protocol.EncodeTSDeleteSeries(req.Metric, startNs, endNs, req.Tags)
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. time-series
	// subsystem not configured, validation failure) that the SM placed in the
	// result payload instead of stalling consensus.
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
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
func (s *TimeSeriesService) SetRetentionPolicy(ctx context.Context, metric string, policy *RetentionPolicy) error {
	cmd, encErr := protocol.EncodeTSSetRetention(
		metric,
		int64(policy.Duration),
		int64(policy.Resolution),
		byte(policy.Aggregation),
	)
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// checkTimeSeriesRetentionEnabled refuses with *feature.NotActiveError
// until every replica of this shard's group can apply
// protocol.OpTSApplyRetention: go-quicraft replays a group's committed log
// to catch up a new member, so an opcode applied under a narrower
// membership that already used it would be in that log for a replica
// admitted later that cannot apply it.
func (s *TimeSeriesService) checkTimeSeriesRetentionEnabled() error {
	gated, ok := s.host.(FeatureGatedHost)
	if ok && !gated.FeatureEnabled(s.shardID, featuregate.TimeSeriesRetention) {
		return &feature.NotActiveError{Feature: featuregate.TimeSeriesRetention, ShardID: s.shardID}
	}
	return nil
}

// ApplyRetention deletes points for metric older than cutoff through Raft
// consensus, so every replica deletes the identical set of points. Refused
// with *feature.NotActiveError until every replica of the group can
// apply protocol.OpTSApplyRetention (see checkTimeSeriesRetentionEnabled).
func (s *TimeSeriesService) ApplyRetention(ctx context.Context, metric string, cutoff time.Time) error {
	if err := s.checkTimeSeriesRetentionEnabled(); err != nil {
		return err
	}
	cmd, encErr := protocol.EncodeTSApplyRetention(metric, cutoff.UnixNano())
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. time-series
	// subsystem not configured) that the SM placed in the result payload
	// instead of stalling consensus.
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
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

// ListMetricsWithConsistency returns metric names using the specified
// read-consistency level.
func (s *TimeSeriesService) ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, TSListMetricsRequest{
		Prefix: prefix,
		Limit:  limit,
	}, ropts)
	if err != nil {
		return nil, s.translateReadError(err)
	}

	metrics, ok := result.([]string)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return metrics, nil
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
