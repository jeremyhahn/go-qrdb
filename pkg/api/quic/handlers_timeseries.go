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

package quic

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// handleTSWrite unmarshals a TSWriteRequest, extracts the data point,
// and stores it via the time-series service. Returns an empty response
// payload on success.
func (s *Server) handleTSWrite(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req pb.TSWriteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	p := req.GetPoint()
	if p == nil {
		return nil, &InvalidArgumentError{Reason: "point is required"}
	}

	ts := time.Unix(0, p.GetTimestampNs())
	tags := protoTagsToMap(p.GetTags())

	if err := s.tsSvc.Write(ctx, p.GetMetric(), p.GetValue(), ts, tags); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleTSWriteBatch unmarshals a TSWriteBatchRequest, converts the
// protobuf data points to service-layer DataPoint values, and stores
// them atomically via the time-series service. Returns an empty
// response payload on success.
func (s *Server) handleTSWriteBatch(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req pb.TSWriteBatchRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	points := make([]timeseries.DataPoint, len(req.GetPoints()))
	for i, p := range req.GetPoints() {
		points[i] = protoToDataPoint(p)
	}

	if err := s.tsSvc.WriteBatch(ctx, points); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleTSQuery unmarshals a TSQueryRequest, queries the time-series
// service for data points matching the metric within the specified time
// range, and returns a serialized TSQueryResponse.
func (s *Server) handleTSQuery(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req pb.TSQueryRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	start := time.Unix(0, req.GetStartNs())
	end := time.Unix(0, req.GetEndNs())

	var opts []timeseries.TSQueryOption

	if tags := protoTagsToMap(req.GetTags()); len(tags) > 0 {
		opts = append(opts, timeseries.WithTags(tags))
	}
	if req.GetLimit() > 0 {
		opts = append(opts, timeseries.WithTSLimit(int(req.GetLimit())))
	}
	if req.GetDescending() {
		opts = append(opts, timeseries.WithDescending())
	}

	var (
		points []timeseries.DataPoint
		err    error
	)
	if ropts, ok := readOptsFromWire(req.GetConsistency()); ok {
		points, err = s.tsSvc.QueryWithConsistency(ctx, req.GetMetric(), start, end, ropts, opts...)
	} else {
		points, err = s.tsSvc.Query(ctx, req.GetMetric(), start, end, opts...)
	}
	if err != nil {
		return nil, err
	}

	pbPoints := make([]*pb.TSDataPoint, len(points))
	for i, p := range points {
		pbPoints[i] = dataPointToProto(p)
	}

	resp, err := proto.Marshal(&pb.TSQueryResponse{Points: pbPoints})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTSAggregate unmarshals a TSAggregateRequest, computes the
// specified aggregation function over data points in the time range
// grouped by the requested interval, and returns a serialized
// TSAggregateResponse.
func (s *Server) handleTSAggregate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req pb.TSAggregateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	start := time.Unix(0, req.GetStartNs())
	end := time.Unix(0, req.GetEndNs())
	fn := protoAggFuncMap[req.GetFunction()]
	interval := time.Duration(req.GetIntervalNs())

	var (
		results []timeseries.AggResult
		err     error
	)
	if ropts, ok := readOptsFromWire(req.GetConsistency()); ok {
		results, err = s.tsSvc.AggregateWithConsistency(ctx, req.GetMetric(), start, end, fn, interval, ropts)
	} else {
		results, err = s.tsSvc.Aggregate(ctx, req.GetMetric(), start, end, fn, interval)
	}
	if err != nil {
		return nil, err
	}

	pbResults := make([]*pb.TSAggResult, len(results))
	for i, r := range results {
		pbResults[i] = aggResultToProto(r)
	}

	resp, err := proto.Marshal(&pb.TSAggregateResponse{Results: pbResults})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// JSON wire types for rich TS handlers
// ---------------------------------------------------------------------------

// tsWritePointWireRequest is the JSON wire payload for ts.WritePoint.
type tsWritePointWireRequest struct {
	Metric      string            `json:"metric"`
	Value       float64           `json:"value"`
	TimestampNs int64             `json:"timestamp_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// tsRichQueryWireRequest is the JSON wire payload for ts.RichQuery.
type tsRichQueryWireRequest struct {
	Metric      string            `json:"metric"`
	StartNs     int64             `json:"start_ns"`
	EndNs       int64             `json:"end_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
	Aggregation int               `json:"aggregation,omitempty"`
	IntervalNs  int64             `json:"interval_ns,omitempty"`
	GroupBy     []string          `json:"group_by,omitempty"`
	Limit       int               `json:"limit,omitempty"`
	Percentile  float64           `json:"percentile,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// tsRichQueryWireResponse is the JSON wire representation of a rich query result.
type tsRichQueryWireResponse struct {
	Metric          string                `json:"metric"`
	Tags            map[string]string     `json:"tags,omitempty"`
	Points          []tsDataPointWire     `json:"points"`
	AggregatedValue *float64              `json:"aggregated_value,omitempty"`
	GroupedResults  []tsGroupedResultWire `json:"grouped_results,omitempty"`
}

// tsDataPointWire is the JSON wire representation of a single data point.
type tsDataPointWire struct {
	Metric      string            `json:"metric"`
	Value       float64           `json:"value"`
	TimestampNs int64             `json:"timestamp_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// tsGroupedResultWire is the JSON wire representation of a grouped query result.
type tsGroupedResultWire struct {
	Tags            map[string]string `json:"tags"`
	Points          []tsDataPointWire `json:"points,omitempty"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
}

// tsDeleteSeriesWireRequest is the JSON wire payload for ts.DeleteSeries.
type tsDeleteSeriesWireRequest struct {
	Metric  string            `json:"metric"`
	Tags    map[string]string `json:"tags,omitempty"`
	StartNs int64             `json:"start_ns,omitempty"`
	EndNs   int64             `json:"end_ns,omitempty"`
}

// tsGetRetentionWireRequest is the JSON wire payload for ts.GetRetention.
type tsGetRetentionWireRequest struct {
	Metric string `json:"metric"`
}

// tsRetentionPolicyWireResponse is the JSON wire representation of a retention policy.
type tsRetentionPolicyWireResponse struct {
	DurationNs   int64 `json:"duration_ns"`
	ResolutionNs int64 `json:"resolution_ns,omitempty"`
	Aggregation  int   `json:"aggregation,omitempty"`
}

// tsSetRetentionWireRequest is the JSON wire payload for ts.SetRetention.
type tsSetRetentionWireRequest struct {
	Metric       string `json:"metric"`
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  int    `json:"aggregation,omitempty"`
}

// tsListMetricsWireRequest is the JSON wire payload for ts.ListMetrics.
type tsListMetricsWireRequest struct {
	Prefix string `json:"prefix,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	// Consistency selects the read-consistency level
	// ("strong"/"linearizable"/"eventual"). Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

// tsListMetricsWireResponse is the JSON wire payload returned by ts.ListMetrics.
type tsListMetricsWireResponse struct {
	Metrics []string `json:"metrics"`
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// dataPointToWire converts a service-layer DataPoint to its JSON wire
// representation with nanosecond timestamps.
func dataPointToWire(dp *timeseries.DataPoint) tsDataPointWire {
	return tsDataPointWire{
		Metric:      dp.Metric,
		Value:       dp.Value,
		TimestampNs: dp.Timestamp.UnixNano(),
		Tags:        dp.Tags,
	}
}

// dataPointsToWire converts a slice of service-layer DataPoint pointers
// to their JSON wire representations.
func dataPointsToWire(dps []*timeseries.DataPoint) []tsDataPointWire {
	out := make([]tsDataPointWire, len(dps))
	for i, dp := range dps {
		out[i] = dataPointToWire(dp)
	}
	return out
}

// ---------------------------------------------------------------------------
// JSON-based TS handlers (no proto messages for these operations)
// ---------------------------------------------------------------------------

// handleTSWritePoint unmarshals a JSON write-point request, converts the
// wire timestamps to time.Time, and stores the data point via the
// time-series service. Returns an empty response payload on success.
func (s *Server) handleTSWritePoint(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsWritePointWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.tsSvc.WritePoint(ctx, &timeseries.DataPoint{
		Metric:    req.Metric,
		Value:     req.Value,
		Timestamp: time.Unix(0, req.TimestampNs),
		Tags:      req.Tags,
	}); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleTSRichQuery unmarshals a JSON rich query request, converts wire
// timestamps and aggregation fields, executes the query, and returns a
// serialized JSON response containing the result with data points
// converted to nanosecond wire format.
func (s *Server) handleTSRichQuery(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsRichQueryWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	qr := &timeseries.QueryRequest{
		Metric:      req.Metric,
		Tags:        req.Tags,
		StartTime:   time.Unix(0, req.StartNs),
		EndTime:     time.Unix(0, req.EndNs),
		Aggregation: timeseries.AggFunc(req.Aggregation),
		Interval:    time.Duration(req.IntervalNs),
		GroupBy:     req.GroupBy,
		Limit:       req.Limit,
		Percentile:  req.Percentile,
	}

	var (
		result *timeseries.QueryResult
		err    error
	)
	if req.Consistency != "" {
		level, parseErr := consistency.ParseLevel(req.Consistency)
		if parseErr != nil {
			return nil, &UnmarshalError{Err: parseErr}
		}
		ropts := consistency.ReadOptions{
			Consistency: level,
			AllowStale:  level == consistency.Eventual,
		}
		result, err = s.tsSvc.QueryRichWithConsistency(ctx, qr, ropts)
	} else {
		result, err = s.tsSvc.QueryRich(ctx, qr)
	}
	if err != nil {
		return nil, err
	}

	wireResp := tsRichQueryWireResponse{
		Metric:          result.Metric,
		Tags:            result.Tags,
		Points:          dataPointsToWire(result.Points),
		AggregatedValue: result.AggregatedValue,
	}

	if len(result.GroupedResults) > 0 {
		wireResp.GroupedResults = make([]tsGroupedResultWire, len(result.GroupedResults))
		for i, gr := range result.GroupedResults {
			wireResp.GroupedResults[i] = tsGroupedResultWire{
				Tags:            gr.Tags,
				Points:          dataPointsToWire(gr.Points),
				AggregatedValue: gr.AggregatedValue,
			}
		}
	}

	out, err := json.Marshal(wireResp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTSDeleteSeries unmarshals a JSON delete-series request, converts
// wire timestamps, and deletes matching data points via the time-series
// service. Returns an empty response payload on success.
func (s *Server) handleTSDeleteSeries(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsDeleteSeriesWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	delReq := &timeseries.DeleteRequest{
		Metric: req.Metric,
		Tags:   req.Tags,
	}
	if req.StartNs != 0 {
		delReq.StartTime = time.Unix(0, req.StartNs)
	}
	if req.EndNs != 0 {
		delReq.EndTime = time.Unix(0, req.EndNs)
	}

	if err := s.tsSvc.DeleteSeries(ctx, delReq); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleTSGetRetention unmarshals a JSON get-retention request, retrieves
// the retention policy for the metric, and returns a serialized JSON
// response with durations expressed as nanoseconds.
func (s *Server) handleTSGetRetention(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsGetRetentionWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	policy, err := s.tsSvc.GetRetentionPolicy(req.Metric)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(tsRetentionPolicyWireResponse{
		DurationNs:   int64(policy.Duration),
		ResolutionNs: int64(policy.Resolution),
		Aggregation:  int(policy.Aggregation),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleTSSetRetention unmarshals a JSON set-retention request, converts
// nanosecond wire durations to time.Duration, and applies the retention
// policy via the time-series service. Returns an empty response payload
// on success.
func (s *Server) handleTSSetRetention(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsSetRetentionWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.tsSvc.SetRetentionPolicy(req.Metric, &timeseries.RetentionPolicy{
		Duration:    time.Duration(req.DurationNs),
		Resolution:  time.Duration(req.ResolutionNs),
		Aggregation: timeseries.AggFunc(req.Aggregation),
	}); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleTSListMetrics unmarshals a JSON list-metrics request, queries the
// time-series service for matching metric names, and returns a serialized
// JSON response.
func (s *Server) handleTSListMetrics(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tsSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	var req tsListMetricsWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var (
		metrics []string
		err     error
	)
	if req.Consistency != "" {
		level, parseErr := consistency.ParseLevel(req.Consistency)
		if parseErr != nil {
			return nil, &UnmarshalError{Err: parseErr}
		}
		ropts := consistency.ReadOptions{
			Consistency: level,
			AllowStale:  level == consistency.Eventual,
		}
		metrics, err = s.tsSvc.ListMetricsWithConsistency(ctx, req.Prefix, req.Limit, ropts)
	} else {
		metrics, err = s.tsSvc.ListMetrics(req.Prefix, req.Limit)
	}
	if err != nil {
		return nil, err
	}

	if metrics == nil {
		metrics = []string{}
	}

	out, err := json.Marshal(tsListMetricsWireResponse{Metrics: metrics})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
