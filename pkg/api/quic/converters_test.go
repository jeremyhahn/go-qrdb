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
	"bytes"
	"sort"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// protoSortOrderMap
// ---------------------------------------------------------------------------

func TestProtoSortOrderMap_Length(t *testing.T) {
	if got := len(protoSortOrderMap); got != 3 {
		t.Fatalf("expected 3 entries, got %d", got)
	}
}

func TestProtoSortOrderMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.SortOrder
		want service.SortOrder
	}{
		{"SORT_NONE", pb.SortOrder_SORT_NONE, service.SortNone},
		{"SORT_ASCEND", pb.SortOrder_SORT_ASCEND, service.SortAscend},
		{"SORT_DESCEND", pb.SortOrder_SORT_DESCEND, service.SortDescend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoSortOrderMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoSortOrderMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoSortOrderMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// protoSortTargetMap
// ---------------------------------------------------------------------------

func TestProtoSortTargetMap_Length(t *testing.T) {
	if got := len(protoSortTargetMap); got != 5 {
		t.Fatalf("expected 5 entries, got %d", got)
	}
}

func TestProtoSortTargetMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.SortTarget
		want service.SortTarget
	}{
		{"SORT_BY_KEY", pb.SortTarget_SORT_BY_KEY, service.SortByKey},
		{"SORT_BY_VERSION", pb.SortTarget_SORT_BY_VERSION, service.SortByVersion},
		{"SORT_BY_VALUE", pb.SortTarget_SORT_BY_VALUE, service.SortByValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoSortTargetMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoSortTargetMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoSortTargetMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestProtoSortTargetMap_AllEntries(t *testing.T) {
	expected := map[pb.SortTarget]service.SortTarget{
		pb.SortTarget_SORT_BY_KEY:             service.SortByKey,
		pb.SortTarget_SORT_BY_VERSION:         service.SortByVersion,
		pb.SortTarget_SORT_BY_CREATE_REVISION: service.SortByCreateRevision,
		pb.SortTarget_SORT_BY_MOD_REVISION:    service.SortByModRevision,
		pb.SortTarget_SORT_BY_VALUE:           service.SortByValue,
	}
	for k, want := range expected {
		got, ok := protoSortTargetMap[k]
		if !ok {
			t.Errorf("key %v missing from protoSortTargetMap", k)
			continue
		}
		if got != want {
			t.Errorf("protoSortTargetMap[%v] = %v, want %v", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// protoCompareTargetMap
// ---------------------------------------------------------------------------

func TestProtoCompareTargetMap_Length(t *testing.T) {
	if got := len(protoCompareTargetMap); got != 4 {
		t.Fatalf("expected 4 entries, got %d", got)
	}
}

func TestProtoCompareTargetMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.CompareTarget
		want service.CompareTarget
	}{
		{"COMPARE_VERSION", pb.CompareTarget_COMPARE_VERSION, service.CompareVersion},
		{"COMPARE_VALUE", pb.CompareTarget_COMPARE_VALUE, service.CompareValue},
		{"COMPARE_CREATE_REVISION", pb.CompareTarget_COMPARE_CREATE_REVISION, service.CompareCreateRevision},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoCompareTargetMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoCompareTargetMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoCompareTargetMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// protoCompareResultMap
// ---------------------------------------------------------------------------

func TestProtoCompareResultMap_Length(t *testing.T) {
	if got := len(protoCompareResultMap); got != 4 {
		t.Fatalf("expected 4 entries, got %d", got)
	}
}

func TestProtoCompareResultMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.CompareResult
		want service.CompareResult
	}{
		{"COMPARE_EQUAL", pb.CompareResult_COMPARE_EQUAL, service.CompareEqual},
		{"COMPARE_GREATER", pb.CompareResult_COMPARE_GREATER, service.CompareGreater},
		{"COMPARE_NOT_EQUAL", pb.CompareResult_COMPARE_NOT_EQUAL, service.CompareNotEqual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoCompareResultMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoCompareResultMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoCompareResultMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// protoOpTypeMap
// ---------------------------------------------------------------------------

func TestProtoOpTypeMap_Length(t *testing.T) {
	if got := len(protoOpTypeMap); got != 4 {
		t.Fatalf("expected 4 entries, got %d", got)
	}
}

func TestProtoOpTypeMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.OpType
		want service.OpType
	}{
		{"OP_PUT", pb.OpType_OP_PUT, service.OpPut},
		{"OP_GET", pb.OpType_OP_GET, service.OpGet},
		{"OP_RANGE", pb.OpType_OP_RANGE, service.OpRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoOpTypeMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoOpTypeMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoOpTypeMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestProtoOpTypeMap_AllEntries(t *testing.T) {
	expected := map[pb.OpType]service.OpType{
		pb.OpType_OP_PUT:    service.OpPut,
		pb.OpType_OP_GET:    service.OpGet,
		pb.OpType_OP_DELETE: service.OpDelete,
		pb.OpType_OP_RANGE:  service.OpRange,
	}
	for k, want := range expected {
		got, ok := protoOpTypeMap[k]
		if !ok {
			t.Errorf("key %v missing from protoOpTypeMap", k)
			continue
		}
		if got != want {
			t.Errorf("protoOpTypeMap[%v] = %v, want %v", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// protoEventTypeMap
// ---------------------------------------------------------------------------

func TestProtoEventTypeMap_Length(t *testing.T) {
	if got := len(protoEventTypeMap); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
}

func TestProtoEventTypeMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  watch.EventType
		want pb.EventType
	}{
		{"EventPut", watch.EventPut, pb.EventType_EVENT_PUT},
		{"EventDelete", watch.EventDelete, pb.EventType_EVENT_DELETE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoEventTypeMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoEventTypeMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoEventTypeMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// protoAggFuncMap
// ---------------------------------------------------------------------------

func TestProtoAggFuncMap_Length(t *testing.T) {
	if got := len(protoAggFuncMap); got != 6 {
		t.Fatalf("expected 6 entries, got %d", got)
	}
}

func TestProtoAggFuncMap_SpotCheck(t *testing.T) {
	tests := []struct {
		name string
		key  pb.AggFunc
		want timeseries.AggFunc
	}{
		{"AGG_AVG", pb.AggFunc_AGG_AVG, timeseries.AggAvg},
		{"AGG_COUNT", pb.AggFunc_AGG_COUNT, timeseries.AggCount},
		{"AGG_PERCENTILE", pb.AggFunc_AGG_PERCENTILE, timeseries.AggPercentile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protoAggFuncMap[tt.key]
			if !ok {
				t.Fatalf("key %v not found in protoAggFuncMap", tt.key)
			}
			if got != tt.want {
				t.Fatalf("protoAggFuncMap[%v] = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestProtoAggFuncMap_AllEntries(t *testing.T) {
	expected := map[pb.AggFunc]timeseries.AggFunc{
		pb.AggFunc_AGG_AVG:        timeseries.AggAvg,
		pb.AggFunc_AGG_SUM:        timeseries.AggSum,
		pb.AggFunc_AGG_MIN:        timeseries.AggMin,
		pb.AggFunc_AGG_MAX:        timeseries.AggMax,
		pb.AggFunc_AGG_COUNT:      timeseries.AggCount,
		pb.AggFunc_AGG_PERCENTILE: timeseries.AggPercentile,
	}
	for k, want := range expected {
		got, ok := protoAggFuncMap[k]
		if !ok {
			t.Errorf("key %v missing from protoAggFuncMap", k)
			continue
		}
		if got != want {
			t.Errorf("protoAggFuncMap[%v] = %v, want %v", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// kvToProto
// ---------------------------------------------------------------------------

func TestKVToProto_NilInput(t *testing.T) {
	got := kvToProto(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestKVToProto_ZeroValues(t *testing.T) {
	k := &kv.KeyValue{}
	got := kvToProto(k)
	if got == nil {
		t.Fatal("expected non-nil result for zero-value input")
	}
	if got.Key != nil {
		t.Fatalf("expected nil Key, got %v", got.Key)
	}
	if got.Value != nil {
		t.Fatalf("expected nil Value, got %v", got.Value)
	}
	if got.CreateRevision != 0 {
		t.Fatalf("expected CreateRevision 0, got %d", got.CreateRevision)
	}
	if got.ModRevision != 0 {
		t.Fatalf("expected ModRevision 0, got %d", got.ModRevision)
	}
	if got.Version != 0 {
		t.Fatalf("expected Version 0, got %d", got.Version)
	}
	if got.LeaseId != 0 {
		t.Fatalf("expected LeaseId 0, got %d", got.LeaseId)
	}
}

func TestKVToProto_FullValues(t *testing.T) {
	k := &kv.KeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 10,
		ModRevision:    20,
		Version:        3,
		LeaseID:        42,
	}
	got := kvToProto(k)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if !bytes.Equal(got.Key, []byte("test-key")) {
		t.Fatalf("Key = %q, want %q", got.Key, "test-key")
	}
	if !bytes.Equal(got.Value, []byte("test-value")) {
		t.Fatalf("Value = %q, want %q", got.Value, "test-value")
	}
	if got.CreateRevision != 10 {
		t.Fatalf("CreateRevision = %d, want 10", got.CreateRevision)
	}
	if got.ModRevision != 20 {
		t.Fatalf("ModRevision = %d, want 20", got.ModRevision)
	}
	if got.Version != 3 {
		t.Fatalf("Version = %d, want 3", got.Version)
	}
	if got.LeaseId != 42 {
		t.Fatalf("LeaseId = %d, want 42", got.LeaseId)
	}
}

// ---------------------------------------------------------------------------
// protoToCompare
// ---------------------------------------------------------------------------

func TestProtoToCompare_VersionTarget(t *testing.T) {
	c := &pb.Compare{
		Key:    []byte("cmp-key"),
		Target: pb.CompareTarget_COMPARE_VERSION,
		Result: pb.CompareResult_COMPARE_EQUAL,
		TargetUnion: &pb.Compare_Version{
			Version: 5,
		},
	}
	got := protoToCompare(c)
	if !bytes.Equal(got.Key, []byte("cmp-key")) {
		t.Fatalf("Key = %q, want %q", got.Key, "cmp-key")
	}
	if got.Target != service.CompareVersion {
		t.Fatalf("Target = %v, want CompareVersion", got.Target)
	}
	if got.Result != service.CompareEqual {
		t.Fatalf("Result = %v, want CompareEqual", got.Result)
	}
	if got.Version != 5 {
		t.Fatalf("Version = %d, want 5", got.Version)
	}
}

func TestProtoToCompare_CreateRevisionTarget(t *testing.T) {
	c := &pb.Compare{
		Key:    []byte("rev-key"),
		Target: pb.CompareTarget_COMPARE_CREATE_REVISION,
		Result: pb.CompareResult_COMPARE_GREATER,
		TargetUnion: &pb.Compare_CreateRevision{
			CreateRevision: 100,
		},
	}
	got := protoToCompare(c)
	if got.Target != service.CompareCreateRevision {
		t.Fatalf("Target = %v, want CompareCreateRevision", got.Target)
	}
	if got.Result != service.CompareGreater {
		t.Fatalf("Result = %v, want CompareGreater", got.Result)
	}
	if got.CreateRevision != 100 {
		t.Fatalf("CreateRevision = %d, want 100", got.CreateRevision)
	}
}

func TestProtoToCompare_ModRevisionTarget(t *testing.T) {
	c := &pb.Compare{
		Key:    []byte("mod-key"),
		Target: pb.CompareTarget_COMPARE_MOD_REVISION,
		Result: pb.CompareResult_COMPARE_LESS,
		TargetUnion: &pb.Compare_ModRevision{
			ModRevision: 77,
		},
	}
	got := protoToCompare(c)
	if got.Target != service.CompareModRevision {
		t.Fatalf("Target = %v, want CompareModRevision", got.Target)
	}
	if got.Result != service.CompareLess {
		t.Fatalf("Result = %v, want CompareLess", got.Result)
	}
	if got.ModRevision != 77 {
		t.Fatalf("ModRevision = %d, want 77", got.ModRevision)
	}
}

func TestProtoToCompare_ValueTarget(t *testing.T) {
	c := &pb.Compare{
		Key:    []byte("val-key"),
		Target: pb.CompareTarget_COMPARE_VALUE,
		Result: pb.CompareResult_COMPARE_NOT_EQUAL,
		TargetUnion: &pb.Compare_Value{
			Value: []byte("expected-value"),
		},
	}
	got := protoToCompare(c)
	if got.Target != service.CompareValue {
		t.Fatalf("Target = %v, want CompareValue", got.Target)
	}
	if got.Result != service.CompareNotEqual {
		t.Fatalf("Result = %v, want CompareNotEqual", got.Result)
	}
	if !bytes.Equal(got.Value, []byte("expected-value")) {
		t.Fatalf("Value = %q, want %q", got.Value, "expected-value")
	}
}

// ---------------------------------------------------------------------------
// protoToOp
// ---------------------------------------------------------------------------

func TestProtoToOp_PutOp(t *testing.T) {
	op := &pb.Op{
		Type:    pb.OpType_OP_PUT,
		Key:     []byte("put-key"),
		Value:   []byte("put-value"),
		LeaseId: 99,
	}
	got := protoToOp(op)
	if got.Type != service.OpPut {
		t.Fatalf("Type = %v, want OpPut", got.Type)
	}
	if !bytes.Equal(got.Key, []byte("put-key")) {
		t.Fatalf("Key = %q, want %q", got.Key, "put-key")
	}
	if !bytes.Equal(got.Value, []byte("put-value")) {
		t.Fatalf("Value = %q, want %q", got.Value, "put-value")
	}
	if got.LeaseID != 99 {
		t.Fatalf("LeaseID = %d, want 99", got.LeaseID)
	}
}

func TestProtoToOp_GetOp(t *testing.T) {
	op := &pb.Op{
		Type: pb.OpType_OP_GET,
		Key:  []byte("get-key"),
	}
	got := protoToOp(op)
	if got.Type != service.OpGet {
		t.Fatalf("Type = %v, want OpGet", got.Type)
	}
	if !bytes.Equal(got.Key, []byte("get-key")) {
		t.Fatalf("Key = %q, want %q", got.Key, "get-key")
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil", got.Value)
	}
}

func TestProtoToOp_DeleteOp(t *testing.T) {
	op := &pb.Op{
		Type: pb.OpType_OP_DELETE,
		Key:  []byte("del-key"),
	}
	got := protoToOp(op)
	if got.Type != service.OpDelete {
		t.Fatalf("Type = %v, want OpDelete", got.Type)
	}
	if !bytes.Equal(got.Key, []byte("del-key")) {
		t.Fatalf("Key = %q, want %q", got.Key, "del-key")
	}
}

func TestProtoToOp_RangeOp(t *testing.T) {
	op := &pb.Op{
		Type:     pb.OpType_OP_RANGE,
		Key:      []byte("start"),
		RangeEnd: []byte("end"),
	}
	got := protoToOp(op)
	if got.Type != service.OpRange {
		t.Fatalf("Type = %v, want OpRange", got.Type)
	}
	if !bytes.Equal(got.Key, []byte("start")) {
		t.Fatalf("Key = %q, want %q", got.Key, "start")
	}
	if !bytes.Equal(got.RangeEnd, []byte("end")) {
		t.Fatalf("RangeEnd = %q, want %q", got.RangeEnd, "end")
	}
}

// ---------------------------------------------------------------------------
// opResponseToProto
// ---------------------------------------------------------------------------

func TestOpResponseToProto_WithKVs(t *testing.T) {
	resp := service.OpResponse{
		Kvs: []*kv.KeyValue{
			{Key: []byte("k1"), Value: []byte("v1"), Version: 1},
			{Key: []byte("k2"), Value: []byte("v2"), Version: 2},
		},
		Count:     2,
		Succeeded: true,
	}
	got := opResponseToProto(resp)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got.Kvs) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(got.Kvs))
	}
	if !bytes.Equal(got.Kvs[0].Key, []byte("k1")) {
		t.Fatalf("Kvs[0].Key = %q, want %q", got.Kvs[0].Key, "k1")
	}
	if !bytes.Equal(got.Kvs[1].Key, []byte("k2")) {
		t.Fatalf("Kvs[1].Key = %q, want %q", got.Kvs[1].Key, "k2")
	}
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if !got.Succeeded {
		t.Fatal("expected Succeeded to be true")
	}
}

func TestOpResponseToProto_Empty(t *testing.T) {
	resp := service.OpResponse{
		Kvs:       nil,
		Count:     0,
		Succeeded: false,
	}
	got := opResponseToProto(resp)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got.Kvs) != 0 {
		t.Fatalf("expected 0 kvs, got %d", len(got.Kvs))
	}
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0", got.Count)
	}
	if got.Succeeded {
		t.Fatal("expected Succeeded to be false")
	}
}

func TestOpResponseToProto_NilKVInSlice(t *testing.T) {
	resp := service.OpResponse{
		Kvs:       []*kv.KeyValue{nil},
		Count:     1,
		Succeeded: true,
	}
	got := opResponseToProto(resp)
	if len(got.Kvs) != 1 {
		t.Fatalf("expected 1 kv, got %d", len(got.Kvs))
	}
	if got.Kvs[0] != nil {
		t.Fatal("expected nil kv in proto slice for nil input kv")
	}
}

// ---------------------------------------------------------------------------
// leaseToProto
// ---------------------------------------------------------------------------

func TestLeaseToProto_NilInput(t *testing.T) {
	got := leaseToProto(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestLeaseToProto_WithKeys(t *testing.T) {
	l := &lease.Lease{
		ID:           100,
		TTL:          60,
		GrantedTTL:   120,
		RemainingTTL: 45,
		Keys:         [][]byte{[]byte("key1"), []byte("key2"), []byte("key3")},
	}
	got := leaseToProto(l)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Id != 100 {
		t.Fatalf("Id = %d, want 100", got.Id)
	}
	if got.Ttl != 60 {
		t.Fatalf("Ttl = %d, want 60", got.Ttl)
	}
	if got.GrantedTtl != 120 {
		t.Fatalf("GrantedTtl = %d, want 120", got.GrantedTtl)
	}
	if got.RemainingTtl != 45 {
		t.Fatalf("RemainingTtl = %d, want 45", got.RemainingTtl)
	}
	if len(got.Keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(got.Keys))
	}
	if !bytes.Equal(got.Keys[0], []byte("key1")) {
		t.Fatalf("Keys[0] = %q, want %q", got.Keys[0], "key1")
	}
}

func TestLeaseToProto_WithoutKeys(t *testing.T) {
	l := &lease.Lease{
		ID:  200,
		TTL: 30,
	}
	got := leaseToProto(l)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Id != 200 {
		t.Fatalf("Id = %d, want 200", got.Id)
	}
	if got.Ttl != 30 {
		t.Fatalf("Ttl = %d, want 30", got.Ttl)
	}
	if got.Keys != nil {
		t.Fatalf("expected nil Keys, got %v", got.Keys)
	}
}

// ---------------------------------------------------------------------------
// eventToProto
// ---------------------------------------------------------------------------

func TestEventToProto_NilInput(t *testing.T) {
	got := eventToProto(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestEventToProto_PutEvent(t *testing.T) {
	e := &watch.Event{
		Type: watch.EventPut,
		Kv: &kv.KeyValue{
			Key:     []byte("put-key"),
			Value:   []byte("put-value"),
			Version: 1,
		},
	}
	got := eventToProto(e)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Type != pb.EventType_EVENT_PUT {
		t.Fatalf("Type = %v, want EVENT_PUT", got.Type)
	}
	if got.Kv == nil {
		t.Fatal("expected non-nil Kv")
	}
	if !bytes.Equal(got.Kv.Key, []byte("put-key")) {
		t.Fatalf("Kv.Key = %q, want %q", got.Kv.Key, "put-key")
	}
	if got.PrevKv != nil {
		t.Fatal("expected nil PrevKv for put event without previous")
	}
}

func TestEventToProto_DeleteEventWithPrevKV(t *testing.T) {
	e := &watch.Event{
		Type: watch.EventDelete,
		Kv: &kv.KeyValue{
			Key:     []byte("del-key"),
			Version: 0,
		},
		PrevKv: &kv.KeyValue{
			Key:     []byte("del-key"),
			Value:   []byte("old-value"),
			Version: 5,
		},
	}
	got := eventToProto(e)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Type != pb.EventType_EVENT_DELETE {
		t.Fatalf("Type = %v, want EVENT_DELETE", got.Type)
	}
	if got.PrevKv == nil {
		t.Fatal("expected non-nil PrevKv")
	}
	if !bytes.Equal(got.PrevKv.Value, []byte("old-value")) {
		t.Fatalf("PrevKv.Value = %q, want %q", got.PrevKv.Value, "old-value")
	}
	if got.PrevKv.Version != 5 {
		t.Fatalf("PrevKv.Version = %d, want 5", got.PrevKv.Version)
	}
}

func TestEventToProto_NilKV(t *testing.T) {
	e := &watch.Event{
		Type: watch.EventPut,
		Kv:   nil,
	}
	got := eventToProto(e)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Kv != nil {
		t.Fatal("expected nil Kv when input Kv is nil")
	}
}

// ---------------------------------------------------------------------------
// protoTagsToMap
// ---------------------------------------------------------------------------

func TestProtoTagsToMap_NilInput(t *testing.T) {
	got := protoTagsToMap(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestProtoTagsToMap_EmptyInput(t *testing.T) {
	got := protoTagsToMap([]*pb.TSTag{})
	if got != nil {
		t.Fatalf("expected nil for empty slice, got %v", got)
	}
}

func TestProtoTagsToMap_MultipleTags(t *testing.T) {
	tags := []*pb.TSTag{
		{Key: "host", Value: "server01"},
		{Key: "region", Value: "us-east-1"},
		{Key: "env", Value: "production"},
	}
	got := protoTagsToMap(tags)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got["host"] != "server01" {
		t.Fatalf("host = %q, want %q", got["host"], "server01")
	}
	if got["region"] != "us-east-1" {
		t.Fatalf("region = %q, want %q", got["region"], "us-east-1")
	}
	if got["env"] != "production" {
		t.Fatalf("env = %q, want %q", got["env"], "production")
	}
}

// ---------------------------------------------------------------------------
// mapToProtoTags
// ---------------------------------------------------------------------------

func TestMapToProtoTags_NilInput(t *testing.T) {
	got := mapToProtoTags(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestMapToProtoTags_EmptyInput(t *testing.T) {
	got := mapToProtoTags(map[string]string{})
	if got != nil {
		t.Fatalf("expected nil for empty map, got %v", got)
	}
}

func TestMapToProtoTags_SingleEntry(t *testing.T) {
	m := map[string]string{"color": "blue"}
	got := mapToProtoTags(m)
	if len(got) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(got))
	}
	if got[0].Key != "color" {
		t.Fatalf("Key = %q, want %q", got[0].Key, "color")
	}
	if got[0].Value != "blue" {
		t.Fatalf("Value = %q, want %q", got[0].Value, "blue")
	}
}

func TestMapToProtoTags_MultipleEntries(t *testing.T) {
	m := map[string]string{
		"dc":   "us-west-2",
		"tier": "frontend",
	}
	got := mapToProtoTags(m)
	if len(got) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(got))
	}

	// Sort by key for deterministic comparison since map iteration is unordered.
	sort.Slice(got, func(i, j int) bool {
		return got[i].Key < got[j].Key
	})

	if got[0].Key != "dc" || got[0].Value != "us-west-2" {
		t.Fatalf("tag[0] = (%q, %q), want (%q, %q)", got[0].Key, got[0].Value, "dc", "us-west-2")
	}
	if got[1].Key != "tier" || got[1].Value != "frontend" {
		t.Fatalf("tag[1] = (%q, %q), want (%q, %q)", got[1].Key, got[1].Value, "tier", "frontend")
	}
}

// ---------------------------------------------------------------------------
// dataPointToProto
// ---------------------------------------------------------------------------

func TestDataPointToProto_WithTags(t *testing.T) {
	ts := time.Date(2026, 2, 26, 12, 0, 0, 0, time.UTC)
	dp := timeseries.DataPoint{
		Metric:    "cpu.usage",
		Value:     85.5,
		Timestamp: ts,
		Tags:      map[string]string{"host": "node01"},
	}
	got := dataPointToProto(dp)
	if got.Metric != "cpu.usage" {
		t.Fatalf("Metric = %q, want %q", got.Metric, "cpu.usage")
	}
	if got.Value != 85.5 {
		t.Fatalf("Value = %f, want 85.5", got.Value)
	}
	if got.TimestampNs != ts.UnixNano() {
		t.Fatalf("TimestampNs = %d, want %d", got.TimestampNs, ts.UnixNano())
	}
	if len(got.Tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(got.Tags))
	}
	if got.Tags[0].Key != "host" || got.Tags[0].Value != "node01" {
		t.Fatalf("tag = (%q, %q), want (%q, %q)", got.Tags[0].Key, got.Tags[0].Value, "host", "node01")
	}
}

func TestDataPointToProto_WithoutTags(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dp := timeseries.DataPoint{
		Metric:    "memory.free",
		Value:     1024.0,
		Timestamp: ts,
	}
	got := dataPointToProto(dp)
	if got.Metric != "memory.free" {
		t.Fatalf("Metric = %q, want %q", got.Metric, "memory.free")
	}
	if got.Value != 1024.0 {
		t.Fatalf("Value = %f, want 1024.0", got.Value)
	}
	if got.TimestampNs != ts.UnixNano() {
		t.Fatalf("TimestampNs = %d, want %d", got.TimestampNs, ts.UnixNano())
	}
	if got.Tags != nil {
		t.Fatalf("expected nil Tags, got %v", got.Tags)
	}
}

func TestDataPointToProto_ZeroTimestamp(t *testing.T) {
	dp := timeseries.DataPoint{
		Metric:    "disk.io",
		Value:     0.0,
		Timestamp: time.Time{},
	}
	got := dataPointToProto(dp)
	zeroNano := (time.Time{}).UnixNano()
	if got.TimestampNs != zeroNano {
		t.Fatalf("TimestampNs = %d, want %d", got.TimestampNs, zeroNano)
	}
}

// ---------------------------------------------------------------------------
// protoToDataPoint
// ---------------------------------------------------------------------------

func TestProtoToDataPoint_WithTags(t *testing.T) {
	ts := time.Date(2026, 6, 15, 8, 30, 0, 0, time.UTC)
	pbDP := &pb.TSDataPoint{
		Metric:      "requests.count",
		Value:       42.0,
		TimestampNs: ts.UnixNano(),
		Tags: []*pb.TSTag{
			{Key: "method", Value: "GET"},
			{Key: "status", Value: "200"},
		},
	}
	got := protoToDataPoint(pbDP)
	if got.Metric != "requests.count" {
		t.Fatalf("Metric = %q, want %q", got.Metric, "requests.count")
	}
	if got.Value != 42.0 {
		t.Fatalf("Value = %f, want 42.0", got.Value)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(got.Tags))
	}
	if got.Tags["method"] != "GET" {
		t.Fatalf("Tags[method] = %q, want %q", got.Tags["method"], "GET")
	}
	if got.Tags["status"] != "200" {
		t.Fatalf("Tags[status] = %q, want %q", got.Tags["status"], "200")
	}
}

func TestProtoToDataPoint_WithoutTags(t *testing.T) {
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	pbDP := &pb.TSDataPoint{
		Metric:      "latency.p99",
		Value:       12.34,
		TimestampNs: ts.UnixNano(),
	}
	got := protoToDataPoint(pbDP)
	if got.Metric != "latency.p99" {
		t.Fatalf("Metric = %q, want %q", got.Metric, "latency.p99")
	}
	if got.Value != 12.34 {
		t.Fatalf("Value = %f, want 12.34", got.Value)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
	if got.Tags != nil {
		t.Fatalf("expected nil Tags, got %v", got.Tags)
	}
}

func TestProtoToDataPoint_ZeroValues(t *testing.T) {
	pbDP := &pb.TSDataPoint{}
	got := protoToDataPoint(pbDP)
	if got.Metric != "" {
		t.Fatalf("Metric = %q, want empty", got.Metric)
	}
	if got.Value != 0.0 {
		t.Fatalf("Value = %f, want 0.0", got.Value)
	}
	expected := time.Unix(0, 0)
	if !got.Timestamp.Equal(expected) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, expected)
	}
}

// ---------------------------------------------------------------------------
// aggResultToProto
// ---------------------------------------------------------------------------

func TestAggResultToProto_FullValues(t *testing.T) {
	ts := time.Date(2026, 7, 4, 14, 0, 0, 0, time.UTC)
	r := timeseries.AggResult{
		Timestamp: ts,
		Value:     99.9,
		Count:     1000,
	}
	got := aggResultToProto(r)
	if got.TimestampNs != ts.UnixNano() {
		t.Fatalf("TimestampNs = %d, want %d", got.TimestampNs, ts.UnixNano())
	}
	if got.Value != 99.9 {
		t.Fatalf("Value = %f, want 99.9", got.Value)
	}
	if got.Count != 1000 {
		t.Fatalf("Count = %d, want 1000", got.Count)
	}
}

func TestAggResultToProto_ZeroValues(t *testing.T) {
	r := timeseries.AggResult{}
	got := aggResultToProto(r)
	zeroNano := (time.Time{}).UnixNano()
	if got.TimestampNs != zeroNano {
		t.Fatalf("TimestampNs = %d, want %d", got.TimestampNs, zeroNano)
	}
	if got.Value != 0.0 {
		t.Fatalf("Value = %f, want 0.0", got.Value)
	}
	if got.Count != 0 {
		t.Fatalf("Count = %d, want 0", got.Count)
	}
}

func TestAggResultToProto_NegativeValue(t *testing.T) {
	ts := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	r := timeseries.AggResult{
		Timestamp: ts,
		Value:     -42.5,
		Count:     7,
	}
	got := aggResultToProto(r)
	if got.Value != -42.5 {
		t.Fatalf("Value = %f, want -42.5", got.Value)
	}
	if got.Count != 7 {
		t.Fatalf("Count = %d, want 7", got.Count)
	}
}

// ---------------------------------------------------------------------------
// Round-trip tests (proto -> service -> proto)
// ---------------------------------------------------------------------------

func TestDataPointRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 15, 10, 30, 0, 0, time.UTC)
	original := timeseries.DataPoint{
		Metric:    "roundtrip.metric",
		Value:     77.7,
		Timestamp: ts,
		Tags:      map[string]string{"env": "test"},
	}

	pbDP := dataPointToProto(original)
	converted := protoToDataPoint(pbDP)

	if converted.Metric != original.Metric {
		t.Fatalf("Metric = %q, want %q", converted.Metric, original.Metric)
	}
	if converted.Value != original.Value {
		t.Fatalf("Value = %f, want %f", converted.Value, original.Value)
	}
	if !converted.Timestamp.Equal(original.Timestamp) {
		t.Fatalf("Timestamp = %v, want %v", converted.Timestamp, original.Timestamp)
	}
	if converted.Tags["env"] != "test" {
		t.Fatalf("Tags[env] = %q, want %q", converted.Tags["env"], "test")
	}
}

func TestTagsRoundTrip(t *testing.T) {
	original := map[string]string{
		"alpha": "one",
		"beta":  "two",
	}

	protoTags := mapToProtoTags(original)
	converted := protoTagsToMap(protoTags)

	if len(converted) != len(original) {
		t.Fatalf("length = %d, want %d", len(converted), len(original))
	}
	for k, want := range original {
		if got := converted[k]; got != want {
			t.Errorf("converted[%q] = %q, want %q", k, got, want)
		}
	}
}
