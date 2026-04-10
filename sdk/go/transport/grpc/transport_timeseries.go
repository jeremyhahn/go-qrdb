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

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TimeSeriesClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// TimeSeriesClient implementation
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point through Raft consensus.
func (t *Transport) TSWrite(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.TSWrite(ctx, &pb.TSWriteRequest{
		Point: sdkPointToProto(point),
	})
	if err != nil {
		return mapGRPCError("TSWrite", err)
	}
	return nil
}

// TSWriteBatch stores multiple time-series data points atomically through
// Raft consensus.
func (t *Transport) TSWriteBatch(ctx context.Context, points []transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	pbPoints := make([]*pb.TSDataPoint, len(points))
	for i := range points {
		pbPoints[i] = sdkPointToProto(&points[i])
	}

	_, err := t.client.TSWriteBatch(ctx, &pb.TSWriteBatchRequest{
		Points: pbPoints,
	})
	if err != nil {
		return mapGRPCError("TSWriteBatch", err)
	}
	return nil
}

// TSQuery retrieves data points for a metric within a time range.
func (t *Transport) TSQuery(ctx context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSQuery(ctx, &pb.TSQueryRequest{
		Metric:     req.Metric,
		StartNs:    req.StartNs,
		EndNs:      req.EndNs,
		Tags:       sdkTagsToProto(req.Tags),
		Limit:      req.Limit,
		Descending: req.Descending,
	})
	if err != nil {
		return nil, mapGRPCError("TSQuery", err)
	}

	points := make([]transport.TSDataPoint, len(resp.GetPoints()))
	for i, p := range resp.GetPoints() {
		points[i] = protoPointToSDK(p)
	}
	return &transport.TSQueryResponse{Points: points}, nil
}

// TSAggregate computes an aggregation over data points in a time range.
func (t *Transport) TSAggregate(ctx context.Context, req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSAggregate(ctx, &pb.TSAggregateRequest{
		Metric:     req.Metric,
		StartNs:    req.StartNs,
		EndNs:      req.EndNs,
		Function:   sdkAggFuncToProto(req.Function),
		IntervalNs: req.IntervalNs,
	})
	if err != nil {
		return nil, mapGRPCError("TSAggregate", err)
	}

	results := make([]transport.TSAggResult, len(resp.GetResults()))
	for i, r := range resp.GetResults() {
		results[i] = transport.TSAggResult{
			TimestampNs: r.GetTimestampNs(),
			Value:       r.GetValue(),
			Count:       r.GetCount(),
		}
	}
	return &transport.TSAggregateResponse{Results: results}, nil
}

// TSWritePoint stores a single validated data point through Raft consensus.
func (t *Transport) TSWritePoint(ctx context.Context, point *transport.TSDataPoint) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.TSWritePoint(ctx, &pb.TSWriteRequest{
		Point: sdkPointToProto(point),
	})
	if err != nil {
		return mapGRPCError("TSWritePoint", err)
	}
	return nil
}

// TSRichQuery performs a rich query with aggregation, downsampling, and
// grouping over time-series data.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSRichQuery(ctx, &pb.TSRichQueryRequest{
		Metric:      req.Metric,
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Tags:        sdkTagsToProto(req.Tags),
		Aggregation: sdkAggFuncToProto(req.Aggregation),
		IntervalNs:  req.IntervalNs,
		GroupBy:     req.GroupBy,
		Limit:       int32(req.Limit),
		Percentile:  req.Percentile,
	})
	if err != nil {
		return nil, mapGRPCError("TSRichQuery", err)
	}

	return protoRichQueryResponseToSDK(resp), nil
}

// TSDeleteSeries deletes time-series data matching the given criteria.
func (t *Transport) TSDeleteSeries(ctx context.Context, req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	_, err := t.client.TSDeleteSeries(ctx, &pb.TSDeleteSeriesRequest{
		Metric:  req.Metric,
		Tags:    sdkTagsToProto(req.Tags),
		StartNs: req.StartNs,
		EndNs:   req.EndNs,
	})
	if err != nil {
		return nil, mapGRPCError("TSDeleteSeries", err)
	}
	return &transport.TSDeleteResponse{}, nil
}

// TSGetRetention retrieves the retention policy for a metric.
func (t *Transport) TSGetRetention(ctx context.Context, req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSGetRetention(ctx, &pb.TSGetRetentionRequest{
		Metric: req.Metric,
	})
	if err != nil {
		return nil, mapGRPCError("TSGetRetention", err)
	}

	return &transport.TSGetRetentionResponse{
		Policy: &transport.TSRetentionPolicy{
			DurationNs:   resp.GetDurationNs(),
			ResolutionNs: resp.GetResolutionNs(),
			Aggregation:  protoAggFuncToSDK(resp.GetAggregation()),
		},
	}, nil
}

// TSSetRetention sets the retention policy for a metric.
func (t *Transport) TSSetRetention(ctx context.Context, req *transport.TSSetRetentionRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	pbReq := &pb.TSSetRetentionRequest{
		Metric: req.Metric,
	}
	if req.Policy != nil {
		pbReq.DurationNs = req.Policy.DurationNs
		pbReq.ResolutionNs = req.Policy.ResolutionNs
		pbReq.Aggregation = sdkAggFuncToProto(req.Policy.Aggregation)
	}

	_, err := t.client.TSSetRetention(ctx, pbReq)
	if err != nil {
		return mapGRPCError("TSSetRetention", err)
	}
	return nil
}

// TSListMetrics lists metric names matching a prefix.
func (t *Transport) TSListMetrics(ctx context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSListMetrics(ctx, &pb.TSListMetricsRequest{
		Prefix: req.Prefix,
		Limit:  req.Limit,
	})
	if err != nil {
		return nil, mapGRPCError("TSListMetrics", err)
	}

	return &transport.TSListMetricsResponse{
		Metrics: resp.GetMetrics(),
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// sdkPointToProto converts an SDK TSDataPoint to its protobuf representation.
func sdkPointToProto(p *transport.TSDataPoint) *pb.TSDataPoint {
	if p == nil {
		return nil
	}
	return &pb.TSDataPoint{
		Metric:      p.Metric,
		Value:       p.Value,
		TimestampNs: p.TimestampNs,
		Tags:        sdkTagsToProto(p.Tags),
	}
}

// sdkTagsToProto converts SDK TSTag slices to protobuf TSTag slices.
func sdkTagsToProto(tags []transport.TSTag) []*pb.TSTag {
	if len(tags) == 0 {
		return nil
	}
	result := make([]*pb.TSTag, len(tags))
	for i, tag := range tags {
		result[i] = &pb.TSTag{Key: tag.Key, Value: tag.Value}
	}
	return result
}

// protoPointToSDK converts a protobuf TSDataPoint to the SDK representation.
func protoPointToSDK(p *pb.TSDataPoint) transport.TSDataPoint {
	return transport.TSDataPoint{
		Metric:      p.GetMetric(),
		Value:       p.GetValue(),
		TimestampNs: p.GetTimestampNs(),
		Tags:        protoTagsToSDK(p.GetTags()),
	}
}

// protoTagsToSDK converts protobuf TSTag slices to SDK TSTag slices.
func protoTagsToSDK(tags []*pb.TSTag) []transport.TSTag {
	if len(tags) == 0 {
		return nil
	}
	result := make([]transport.TSTag, len(tags))
	for i, tag := range tags {
		result[i] = transport.TSTag{Key: tag.GetKey(), Value: tag.GetValue()}
	}
	return result
}

// protoPointsToSDK converts a slice of protobuf TSDataPoint to SDK types.
func protoPointsToSDK(points []*pb.TSDataPoint) []transport.TSDataPoint {
	if len(points) == 0 {
		return nil
	}
	result := make([]transport.TSDataPoint, len(points))
	for i, p := range points {
		result[i] = protoPointToSDK(p)
	}
	return result
}

// sdkAggFuncToProto maps SDK AggFunc values to protobuf AggFunc values.
// SDK values are offset by +1 from protobuf values (SDK AggAvg=1 maps to
// proto AGG_AVG=0). SDK AggNone(0) maps to AGG_AVG(0) as the proto has
// no explicit "none" value.
var sdkToProtoAggFunc = map[transport.AggFunc]pb.AggFunc{
	transport.AggNone:       pb.AggFunc_AGG_AVG,
	transport.AggAvg:        pb.AggFunc_AGG_AVG,
	transport.AggSum:        pb.AggFunc_AGG_SUM,
	transport.AggMin:        pb.AggFunc_AGG_MIN,
	transport.AggMax:        pb.AggFunc_AGG_MAX,
	transport.AggCount:      pb.AggFunc_AGG_COUNT,
	transport.AggPercentile: pb.AggFunc_AGG_PERCENTILE,
}

// sdkAggFuncToProto converts an SDK AggFunc to its protobuf equivalent.
func sdkAggFuncToProto(f transport.AggFunc) pb.AggFunc {
	if pf, ok := sdkToProtoAggFunc[f]; ok {
		return pf
	}
	return pb.AggFunc_AGG_AVG
}

// protoToSDKAggFunc maps protobuf AggFunc values to SDK AggFunc values.
// Protobuf values are offset by -1 from SDK values (proto AGG_AVG=0 maps
// to SDK AggAvg=1).
var protoToSDKAggFunc = map[pb.AggFunc]transport.AggFunc{
	pb.AggFunc_AGG_AVG:        transport.AggAvg,
	pb.AggFunc_AGG_SUM:        transport.AggSum,
	pb.AggFunc_AGG_MIN:        transport.AggMin,
	pb.AggFunc_AGG_MAX:        transport.AggMax,
	pb.AggFunc_AGG_COUNT:      transport.AggCount,
	pb.AggFunc_AGG_PERCENTILE: transport.AggPercentile,
}

// protoAggFuncToSDK converts a protobuf AggFunc to its SDK equivalent.
func protoAggFuncToSDK(f pb.AggFunc) transport.AggFunc {
	if sf, ok := protoToSDKAggFunc[f]; ok {
		return sf
	}
	return transport.AggNone
}

// protoRichQueryResponseToSDK converts a protobuf TSRichQueryResponse to its
// SDK representation, handling the HasAggregatedValue sentinel for optional
// float64 values.
func protoRichQueryResponseToSDK(resp *pb.TSRichQueryResponse) *transport.TSRichQueryResponse {
	result := &transport.TSRichQueryResponse{
		Metric: resp.GetMetric(),
		Tags:   protoTagsToSDK(resp.GetTags()),
		Points: protoPointsToSDK(resp.GetPoints()),
	}

	if resp.GetHasAggregatedValue() {
		v := resp.GetAggregatedValue()
		result.AggregatedValue = &v
	}

	if grouped := resp.GetGroupedResults(); len(grouped) > 0 {
		result.GroupedResults = make([]transport.TSGroupedResult, len(grouped))
		for i, g := range grouped {
			gr := transport.TSGroupedResult{
				Tags:   protoTagsToSDK(g.GetTags()),
				Points: protoPointsToSDK(g.GetPoints()),
			}
			if g.GetHasAggregatedValue() {
				v := g.GetAggregatedValue()
				gr.AggregatedValue = &v
			}
			result.GroupedResults[i] = gr
		}
	}

	return result
}
