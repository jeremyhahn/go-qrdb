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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TimeSeriesClient = (*Transport)(nil)

// protoConsistencyFromString maps an SDK consistency string to its proto
// ConsistencyLevel. An empty or unrecognised string maps to UNSPECIFIED so
// the server uses its default path.
func protoConsistencyFromString(s string) pb.ConsistencyLevel {
	switch s {
	case "strong":
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG
	case "linearizable":
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE
	case "eventual", "stale":
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	default:
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED
	}
}

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// aggFuncToProto converts transport AggFunc to proto AggFunc.
var aggFuncToProto = map[transport.AggFunc]pb.AggFunc{
	transport.AggAvg:        pb.AggFunc_AGG_AVG,
	transport.AggSum:        pb.AggFunc_AGG_SUM,
	transport.AggMin:        pb.AggFunc_AGG_MIN,
	transport.AggMax:        pb.AggFunc_AGG_MAX,
	transport.AggCount:      pb.AggFunc_AGG_COUNT,
	transport.AggPercentile: pb.AggFunc_AGG_PERCENTILE,
}

// ---------------------------------------------------------------------------
// Implemented methods (server handlers exist)
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point through the QUIC
// transport.
func (t *Transport) TSWrite(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TSWriteRequest{
		Point: dataPointToProto(point),
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "ts.Write", payload)
	return err
}

// TSWriteBatch stores multiple time-series data points atomically
// through the QUIC transport.
func (t *Transport) TSWriteBatch(ctx context.Context, points []transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	pbPoints := make([]*pb.TSDataPoint, len(points))
	for i := range points {
		pbPoints[i] = dataPointToProto(&points[i])
	}

	payload, err := proto.Marshal(&pb.TSWriteBatchRequest{
		Points: pbPoints,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "ts.WriteBatch", payload)
	return err
}

// TSQuery retrieves data points for a metric within a time range
// through the QUIC transport.
func (t *Transport) TSQuery(ctx context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req.Consistency != "" {
		if _, perr := transport.ParseConsistencyLevel(req.Consistency); perr != nil {
			return nil, &transport.RequestError{Method: "TSQuery", Err: perr}
		}
	}

	payload, err := proto.Marshal(&pb.TSQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Tags:        tagsToProto(req.Tags),
		Limit:       req.Limit,
		Descending:  req.Descending,
		Consistency: protoConsistencyFromString(req.Consistency),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "ts.Query", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.TSQueryResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	points := make([]transport.TSDataPoint, len(pbResp.GetPoints()))
	for i, p := range pbResp.GetPoints() {
		points[i] = protoToDataPoint(p)
	}

	return &transport.TSQueryResponse{Points: points}, nil
}

// TSAggregate computes an aggregation over data points in a time
// range through the QUIC transport.
func (t *Transport) TSAggregate(ctx context.Context, req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req.Consistency != "" {
		if _, perr := transport.ParseConsistencyLevel(req.Consistency); perr != nil {
			return nil, &transport.RequestError{Method: "TSAggregate", Err: perr}
		}
	}

	payload, err := proto.Marshal(&pb.TSAggregateRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Function:    aggFuncToProto[req.Function],
		IntervalNs:  req.IntervalNs,
		Consistency: protoConsistencyFromString(req.Consistency),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "ts.Aggregate", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.TSAggregateResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	results := make([]transport.TSAggResult, len(pbResp.GetResults()))
	for i, r := range pbResp.GetResults() {
		results[i] = transport.TSAggResult{
			TimestampNs: r.GetTimestampNs(),
			Value:       r.GetValue(),
			Count:       r.GetCount(),
		}
	}

	return &transport.TSAggregateResponse{Results: results}, nil
}

// ---------------------------------------------------------------------------
// JSON wire types for TS methods
// ---------------------------------------------------------------------------

type tsWritePointRequest struct {
	Metric      string            `json:"metric"`
	Value       float64           `json:"value"`
	TimestampNs int64             `json:"timestamp_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type tsRichQueryRequest struct {
	Metric      string            `json:"metric"`
	StartNs     int64             `json:"start_ns"`
	EndNs       int64             `json:"end_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
	Aggregation int               `json:"aggregation,omitempty"`
	IntervalNs  int64             `json:"interval_ns,omitempty"`
	GroupBy     []string          `json:"group_by,omitempty"`
	Limit       int               `json:"limit,omitempty"`
	Percentile  float64           `json:"percentile,omitempty"`
	// Consistency selects the read-consistency level. Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

type tsRichQueryResponse struct {
	Metric          string                `json:"metric"`
	Tags            map[string]string     `json:"tags,omitempty"`
	Points          []tsDataPointWire     `json:"points"`
	AggregatedValue *float64              `json:"aggregated_value,omitempty"`
	GroupedResults  []tsGroupedResultWire `json:"grouped_results,omitempty"`
}

type tsDataPointWire struct {
	Metric      string            `json:"metric"`
	Value       float64           `json:"value"`
	TimestampNs int64             `json:"timestamp_ns"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type tsGroupedResultWire struct {
	Tags            map[string]string `json:"tags"`
	Points          []tsDataPointWire `json:"points,omitempty"`
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
}

type tsDeleteSeriesRequest struct {
	Metric  string            `json:"metric"`
	Tags    map[string]string `json:"tags,omitempty"`
	StartNs int64             `json:"start_ns,omitempty"`
	EndNs   int64             `json:"end_ns,omitempty"`
}

type tsGetRetentionRequest struct {
	Metric string `json:"metric"`
}

type tsRetentionPolicyWire struct {
	DurationNs   int64 `json:"duration_ns"`
	ResolutionNs int64 `json:"resolution_ns,omitempty"`
	Aggregation  int   `json:"aggregation,omitempty"`
}

type tsGetRetentionResponse struct {
	Policy *tsRetentionPolicyWire `json:"policy"`
}

type tsSetRetentionRequest struct {
	Metric       string `json:"metric"`
	DurationNs   int64  `json:"duration_ns"`
	ResolutionNs int64  `json:"resolution_ns,omitempty"`
	Aggregation  int    `json:"aggregation,omitempty"`
}

type tsListMetricsRequest struct {
	Prefix string `json:"prefix,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
	// Consistency selects the read-consistency level. Empty uses the server default.
	Consistency string `json:"consistency,omitempty"`
}

type tsListMetricsResponse struct {
	Metrics []string `json:"metrics"`
}

// ---------------------------------------------------------------------------
// JSON-based TS methods
// ---------------------------------------------------------------------------

// TSWritePoint stores a single validated data point through the QUIC
// transport using the JSON wire format.
func (t *Transport) TSWritePoint(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	req := tsWritePointRequest{
		Metric:      point.Metric,
		Value:       point.Value,
		TimestampNs: point.TimestampNs,
		Tags:        tagsToMap(point.Tags),
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "ts.WritePoint", payload)
	return err
}

// TSRichQuery performs a rich query with aggregation, downsampling,
// and grouping through the QUIC transport using the JSON wire format.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req.Consistency != "" {
		if _, perr := transport.ParseConsistencyLevel(req.Consistency); perr != nil {
			return nil, &transport.RequestError{Method: "TSRichQuery", Err: perr}
		}
	}

	wireReq := tsRichQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Tags:        tagsToMap(req.Tags),
		Aggregation: int(req.Aggregation),
		IntervalNs:  req.IntervalNs,
		GroupBy:     req.GroupBy,
		Limit:       req.Limit,
		Percentile:  req.Percentile,
		Consistency: req.Consistency,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "ts.RichQuery", payload)
	if err != nil {
		return nil, err
	}

	var wireResp tsRichQueryResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	points := make([]transport.TSDataPoint, len(wireResp.Points))
	for i, p := range wireResp.Points {
		points[i] = transport.TSDataPoint{
			Metric:      p.Metric,
			Value:       p.Value,
			TimestampNs: p.TimestampNs,
			Tags:        mapToTags(p.Tags),
		}
	}

	grouped := make([]transport.TSGroupedResult, len(wireResp.GroupedResults))
	for i, g := range wireResp.GroupedResults {
		gPoints := make([]transport.TSDataPoint, len(g.Points))
		for j, p := range g.Points {
			gPoints[j] = transport.TSDataPoint{
				Metric:      p.Metric,
				Value:       p.Value,
				TimestampNs: p.TimestampNs,
				Tags:        mapToTags(p.Tags),
			}
		}
		grouped[i] = transport.TSGroupedResult{
			Tags:            mapToTags(g.Tags),
			Points:          gPoints,
			AggregatedValue: g.AggregatedValue,
		}
	}

	return &transport.TSRichQueryResponse{
		Metric:          wireResp.Metric,
		Tags:            mapToTags(wireResp.Tags),
		Points:          points,
		AggregatedValue: wireResp.AggregatedValue,
		GroupedResults:  grouped,
	}, nil
}

// TSDeleteSeries deletes time-series data matching the criteria
// through the QUIC transport using the JSON wire format.
func (t *Transport) TSDeleteSeries(ctx context.Context, req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := tsDeleteSeriesRequest{
		Metric:  req.Metric,
		Tags:    tagsToMap(req.Tags),
		StartNs: req.StartNs,
		EndNs:   req.EndNs,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	_, err = t.call(ctx, "ts.DeleteSeries", payload)
	if err != nil {
		return nil, err
	}

	return &transport.TSDeleteResponse{}, nil
}

// TSGetRetention retrieves the retention policy for a metric through
// the QUIC transport using the JSON wire format.
func (t *Transport) TSGetRetention(ctx context.Context, req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := tsGetRetentionRequest{
		Metric: req.Metric,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "ts.GetRetention", payload)
	if err != nil {
		return nil, err
	}

	var wireResp tsGetRetentionResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	var policy *transport.TSRetentionPolicy
	if wireResp.Policy != nil {
		policy = &transport.TSRetentionPolicy{
			DurationNs:   wireResp.Policy.DurationNs,
			ResolutionNs: wireResp.Policy.ResolutionNs,
			Aggregation:  transport.AggFunc(wireResp.Policy.Aggregation),
		}
	}

	return &transport.TSGetRetentionResponse{
		Policy: policy,
	}, nil
}

// TSSetRetention sets the retention policy for a metric through the
// QUIC transport using the JSON wire format.
func (t *Transport) TSSetRetention(ctx context.Context, req *transport.TSSetRetentionRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	wireReq := tsSetRetentionRequest{
		Metric: req.Metric,
	}
	if req.Policy != nil {
		wireReq.DurationNs = req.Policy.DurationNs
		wireReq.ResolutionNs = req.Policy.ResolutionNs
		wireReq.Aggregation = int(req.Policy.Aggregation)
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "ts.SetRetention", payload)
	return err
}

// TSListMetrics lists metric names matching a prefix through the
// QUIC transport using the JSON wire format.
func (t *Transport) TSListMetrics(ctx context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req.Consistency != "" {
		if _, perr := transport.ParseConsistencyLevel(req.Consistency); perr != nil {
			return nil, &transport.RequestError{Method: "TSListMetrics", Err: perr}
		}
	}

	wireReq := tsListMetricsRequest{
		Prefix:      req.Prefix,
		Limit:       req.Limit,
		Consistency: req.Consistency,
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "ts.ListMetrics", payload)
	if err != nil {
		return nil, err
	}

	var wireResp tsListMetricsResponse
	if err := json.Unmarshal(resp, &wireResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.TSListMetricsResponse{
		Metrics: wireResp.Metrics,
	}, nil
}

// ---------------------------------------------------------------------------
// Tag conversion helpers (JSON wire format uses map, transport uses slice)
// ---------------------------------------------------------------------------

// tagsToMap converts a slice of transport TSTag to a map for JSON
// wire encoding.
func tagsToMap(tags []transport.TSTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// mapToTags converts a map from JSON wire decoding to a slice of
// transport TSTag.
func mapToTags(m map[string]string) []transport.TSTag {
	if len(m) == 0 {
		return nil
	}
	tags := make([]transport.TSTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, transport.TSTag{Key: k, Value: v})
	}
	return tags
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// dataPointToProto converts a transport TSDataPoint to its proto
// representation.
func dataPointToProto(p *transport.TSDataPoint) *pb.TSDataPoint {
	if p == nil {
		return nil
	}
	return &pb.TSDataPoint{
		Metric:      p.Metric,
		Value:       p.Value,
		TimestampNs: p.TimestampNs,
		Tags:        tagsToProto(p.Tags),
	}
}

// protoToDataPoint converts a proto TSDataPoint to a transport
// TSDataPoint.
func protoToDataPoint(p *pb.TSDataPoint) transport.TSDataPoint {
	return transport.TSDataPoint{
		Metric:      p.GetMetric(),
		Value:       p.GetValue(),
		TimestampNs: p.GetTimestampNs(),
		Tags:        protoToTags(p.GetTags()),
	}
}

// tagsToProto converts a slice of transport TSTag to proto TSTag.
func tagsToProto(tags []transport.TSTag) []*pb.TSTag {
	if len(tags) == 0 {
		return nil
	}
	pbTags := make([]*pb.TSTag, len(tags))
	for i, t := range tags {
		pbTags[i] = &pb.TSTag{Key: t.Key, Value: t.Value}
	}
	return pbTags
}

// protoToTags converts proto TSTag messages to a slice of transport
// TSTag.
func protoToTags(tags []*pb.TSTag) []transport.TSTag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]transport.TSTag, len(tags))
	for i, t := range tags {
		out[i] = transport.TSTag{Key: t.GetKey(), Value: t.GetValue()}
	}
	return out
}
