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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// TimeSeriesClient implementation
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point.
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

// TSWriteBatch stores multiple time-series data points atomically.
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

// TSWritePoint stores a single validated data point.
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

// TSRichQuery performs a rich query with aggregation, downsampling, and grouping.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TSRichQuery(ctx, &pb.TSRichQueryRequest{
		Metric:      req.Metric,
		Tags:        sdkTagsToProto(req.Tags),
		StartNs:     req.StartNs,
		EndNs:       req.EndNs,
		Aggregation: sdkAggFuncToProto(req.Aggregation),
		IntervalNs:  req.IntervalNs,
		GroupBy:     req.GroupBy,
		Limit:       int32(req.Limit),
		Percentile:  req.Percentile,
	})
	if err != nil {
		return nil, mapGRPCError("TSRichQuery", err)
	}

	points := make([]transport.TSDataPoint, len(resp.GetPoints()))
	for i, p := range resp.GetPoints() {
		points[i] = protoPointToSDK(p)
	}

	result := &transport.TSRichQueryResponse{
		Metric: resp.GetMetric(),
		Tags:   protoTagsToSDK(resp.GetTags()),
		Points: points,
	}

	if resp.GetHasAggregatedValue() {
		v := resp.GetAggregatedValue()
		result.AggregatedValue = &v
	}

	grouped := resp.GetGroupedResults()
	if len(grouped) > 0 {
		result.GroupedResults = make([]transport.TSGroupedResult, len(grouped))
		for i, g := range grouped {
			gPoints := make([]transport.TSDataPoint, len(g.GetPoints()))
			for j, p := range g.GetPoints() {
				gPoints[j] = protoPointToSDK(p)
			}
			gr := transport.TSGroupedResult{
				Tags:   protoTagsToSDK(g.GetTags()),
				Points: gPoints,
			}
			if g.GetHasAggregatedValue() {
				v := g.GetAggregatedValue()
				gr.AggregatedValue = &v
			}
			result.GroupedResults[i] = gr
		}
	}

	return result, nil
}

// TSDeleteSeries deletes time-series data matching the criteria.
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

// sdkPointToProto converts an SDK TSDataPoint to a proto TSDataPoint.
func sdkPointToProto(p *transport.TSDataPoint) *pb.TSDataPoint {
	return &pb.TSDataPoint{
		Metric:      p.Metric,
		Value:       p.Value,
		TimestampNs: p.TimestampNs,
		Tags:        sdkTagsToProto(p.Tags),
	}
}

// sdkTagsToProto converts SDK tags to proto tags.
func sdkTagsToProto(tags []transport.TSTag) []*pb.TSTag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]*pb.TSTag, len(tags))
	for i, tag := range tags {
		out[i] = &pb.TSTag{
			Key:   tag.Key,
			Value: tag.Value,
		}
	}
	return out
}

// protoPointToSDK converts a proto TSDataPoint to an SDK TSDataPoint.
func protoPointToSDK(p *pb.TSDataPoint) transport.TSDataPoint {
	return transport.TSDataPoint{
		Metric:      p.GetMetric(),
		Value:       p.GetValue(),
		TimestampNs: p.GetTimestampNs(),
		Tags:        protoTagsToSDK(p.GetTags()),
	}
}

// protoTagsToSDK converts proto tags to SDK tags.
func protoTagsToSDK(tags []*pb.TSTag) []transport.TSTag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]transport.TSTag, len(tags))
	for i, tag := range tags {
		out[i] = transport.TSTag{
			Key:   tag.GetKey(),
			Value: tag.GetValue(),
		}
	}
	return out
}

// sdkAggFuncToProto converts an SDK AggFunc to a proto AggFunc. The SDK
// enum is offset by 1 from the proto enum (SDK AggNone=0 has no proto
// equivalent; SDK AggAvg=1 maps to proto AGG_AVG=0, etc.).
func sdkAggFuncToProto(f transport.AggFunc) pb.AggFunc {
	switch f {
	case transport.AggAvg:
		return pb.AggFunc_AGG_AVG
	case transport.AggSum:
		return pb.AggFunc_AGG_SUM
	case transport.AggMin:
		return pb.AggFunc_AGG_MIN
	case transport.AggMax:
		return pb.AggFunc_AGG_MAX
	case transport.AggCount:
		return pb.AggFunc_AGG_COUNT
	case transport.AggPercentile:
		return pb.AggFunc_AGG_PERCENTILE
	default:
		return pb.AggFunc_AGG_AVG
	}
}

// protoAggFuncToSDK converts a proto AggFunc to an SDK AggFunc. The
// proto enum is offset by -1 from the SDK enum.
func protoAggFuncToSDK(f pb.AggFunc) transport.AggFunc {
	switch f {
	case pb.AggFunc_AGG_AVG:
		return transport.AggAvg
	case pb.AggFunc_AGG_SUM:
		return transport.AggSum
	case pb.AggFunc_AGG_MIN:
		return transport.AggMin
	case pb.AggFunc_AGG_MAX:
		return transport.AggMax
	case pb.AggFunc_AGG_COUNT:
		return transport.AggCount
	case pb.AggFunc_AGG_PERCENTILE:
		return transport.AggPercentile
	default:
		return transport.AggNone
	}
}
