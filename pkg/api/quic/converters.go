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
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// protoSortOrderMap converts proto SortOrder values to service SortOrder.
var protoSortOrderMap = map[pb.SortOrder]service.SortOrder{
	pb.SortOrder_SORT_NONE:    service.SortNone,
	pb.SortOrder_SORT_ASCEND:  service.SortAscend,
	pb.SortOrder_SORT_DESCEND: service.SortDescend,
}

// protoSortTargetMap converts proto SortTarget values to service SortTarget.
var protoSortTargetMap = map[pb.SortTarget]service.SortTarget{
	pb.SortTarget_SORT_BY_KEY:             service.SortByKey,
	pb.SortTarget_SORT_BY_VERSION:         service.SortByVersion,
	pb.SortTarget_SORT_BY_CREATE_REVISION: service.SortByCreateRevision,
	pb.SortTarget_SORT_BY_MOD_REVISION:    service.SortByModRevision,
	pb.SortTarget_SORT_BY_VALUE:           service.SortByValue,
}

// protoCompareTargetMap converts proto CompareTarget values to service CompareTarget.
var protoCompareTargetMap = map[pb.CompareTarget]service.CompareTarget{
	pb.CompareTarget_COMPARE_VERSION:         service.CompareVersion,
	pb.CompareTarget_COMPARE_CREATE_REVISION: service.CompareCreateRevision,
	pb.CompareTarget_COMPARE_MOD_REVISION:    service.CompareModRevision,
	pb.CompareTarget_COMPARE_VALUE:           service.CompareValue,
}

// protoCompareResultMap converts proto CompareResult values to service CompareResult.
var protoCompareResultMap = map[pb.CompareResult]service.CompareResult{
	pb.CompareResult_COMPARE_EQUAL:     service.CompareEqual,
	pb.CompareResult_COMPARE_GREATER:   service.CompareGreater,
	pb.CompareResult_COMPARE_LESS:      service.CompareLess,
	pb.CompareResult_COMPARE_NOT_EQUAL: service.CompareNotEqual,
}

// protoOpTypeMap converts proto OpType values to service OpType.
var protoOpTypeMap = map[pb.OpType]service.OpType{
	pb.OpType_OP_PUT:    service.OpPut,
	pb.OpType_OP_GET:    service.OpGet,
	pb.OpType_OP_DELETE: service.OpDelete,
	pb.OpType_OP_RANGE:  service.OpRange,
}

// protoEventTypeMap converts watch EventType values to proto EventType.
var protoEventTypeMap = map[watch.EventType]pb.EventType{
	watch.EventPut:    pb.EventType_EVENT_PUT,
	watch.EventDelete: pb.EventType_EVENT_DELETE,
}

// protoAggFuncMap converts proto AggFunc values to timeseries AggFunc.
var protoAggFuncMap = map[pb.AggFunc]timeseries.AggFunc{
	pb.AggFunc_AGG_AVG:        timeseries.AggAvg,
	pb.AggFunc_AGG_SUM:        timeseries.AggSum,
	pb.AggFunc_AGG_MIN:        timeseries.AggMin,
	pb.AggFunc_AGG_MAX:        timeseries.AggMax,
	pb.AggFunc_AGG_COUNT:      timeseries.AggCount,
	pb.AggFunc_AGG_PERCENTILE: timeseries.AggPercentile,
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// kvToProto converts a service-layer KeyValue to its protobuf representation.
func kvToProto(k *kv.KeyValue) *pb.KeyValue {
	if k == nil {
		return nil
	}
	return &pb.KeyValue{
		Key:            k.Key,
		Value:          k.Value,
		CreateRevision: k.CreateRevision,
		ModRevision:    k.ModRevision,
		Version:        k.Version,
		LeaseId:        k.LeaseID,
	}
}

// protoToCompare converts a proto Compare to a service Compare.
func protoToCompare(c *pb.Compare) service.Compare {
	sc := service.Compare{
		Key:    c.GetKey(),
		Target: protoCompareTargetMap[c.GetTarget()],
		Result: protoCompareResultMap[c.GetResult()],
	}

	switch c.GetTarget() {
	case pb.CompareTarget_COMPARE_VERSION:
		sc.Version = c.GetVersion()
	case pb.CompareTarget_COMPARE_CREATE_REVISION:
		sc.CreateRevision = c.GetCreateRevision()
	case pb.CompareTarget_COMPARE_MOD_REVISION:
		sc.ModRevision = c.GetModRevision()
	case pb.CompareTarget_COMPARE_VALUE:
		sc.Value = c.GetValue()
	}

	return sc
}

// protoToOp converts a proto Op to a service Op. An enum value this server
// does not know becomes service.OpUnknown, which the service refuses, never
// the zero OpPut.
func protoToOp(op *pb.Op) service.Op {
	opType, ok := protoOpTypeMap[op.GetType()]
	if !ok {
		opType = service.OpUnknown
	}
	return service.Op{
		Type:     opType,
		Key:      op.GetKey(),
		Value:    op.GetValue(),
		RangeEnd: op.GetRangeEnd(),
		LeaseID:  op.GetLeaseId(),
	}
}

// opResponseToProto converts a service OpResponse to its protobuf representation.
func opResponseToProto(r service.OpResponse) *pb.OpResponse {
	pbKvs := make([]*pb.KeyValue, len(r.Kvs))
	for i, k := range r.Kvs {
		pbKvs[i] = kvToProto(k)
	}
	return &pb.OpResponse{
		Kvs:       pbKvs,
		Count:     r.Count,
		Succeeded: r.Succeeded,
	}
}

// leaseToProto converts a service-layer Lease to its protobuf representation.
func leaseToProto(l *lease.Lease) *pb.Lease {
	if l == nil {
		return nil
	}
	return &pb.Lease{
		Id:           l.ID,
		Ttl:          l.TTL,
		GrantedTtl:   l.GrantedTTL,
		RemainingTtl: l.RemainingTTL,
		Keys:         l.Keys,
	}
}

// eventToProto converts a watch Event to its protobuf representation.
func eventToProto(e *watch.Event) *pb.Event {
	if e == nil {
		return nil
	}
	return &pb.Event{
		Type:   protoEventTypeMap[e.Type],
		Kv:     kvToProto(e.Kv),
		PrevKv: kvToProto(e.PrevKv),
	}
}

// protoTagsToMap converts repeated TSTag proto messages to a Go map.
func protoTagsToMap(tags []*pb.TSTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.GetKey()] = t.GetValue()
	}
	return m
}

// mapToProtoTags converts a Go map to repeated TSTag proto messages.
func mapToProtoTags(m map[string]string) []*pb.TSTag {
	if len(m) == 0 {
		return nil
	}
	tags := make([]*pb.TSTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, &pb.TSTag{Key: k, Value: v})
	}
	return tags
}

// dataPointToProto converts a service-layer DataPoint to its protobuf representation.
func dataPointToProto(dp timeseries.DataPoint) *pb.TSDataPoint {
	return &pb.TSDataPoint{
		Metric:      dp.Metric,
		Value:       dp.Value,
		TimestampNs: dp.Timestamp.UnixNano(),
		Tags:        mapToProtoTags(dp.Tags),
	}
}

// protoToDataPoint converts a proto TSDataPoint to a service-layer DataPoint.
func protoToDataPoint(dp *pb.TSDataPoint) timeseries.DataPoint {
	return timeseries.DataPoint{
		Metric:    dp.GetMetric(),
		Value:     dp.GetValue(),
		Timestamp: time.Unix(0, dp.GetTimestampNs()),
		Tags:      protoTagsToMap(dp.GetTags()),
	}
}

// aggResultToProto converts a service-layer AggResult to its protobuf representation.
func aggResultToProto(r timeseries.AggResult) *pb.TSAggResult {
	return &pb.TSAggResult{
		TimestampNs: r.Timestamp.UnixNano(),
		Value:       r.Value,
		Count:       r.Count,
	}
}
