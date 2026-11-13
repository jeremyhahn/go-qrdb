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
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoToMVCCKeyValue tests
// ---------------------------------------------------------------------------

func TestProtoToMVCCKeyValue_Nil(t *testing.T) {
	result := protoToMVCCKeyValue(nil)
	if result != nil {
		t.Errorf("protoToMVCCKeyValue(nil) = %v, want nil", result)
	}
}

func TestProtoToMVCCKeyValue_Valid(t *testing.T) {
	kv := &pb.KeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 10,
		ModRevision:    15,
		Version:        3,
		LeaseId:        42,
	}
	result := protoToMVCCKeyValue(kv)
	if result == nil {
		t.Fatal("protoToMVCCKeyValue() returned nil for valid input")
	}
	if string(result.Key) != "test-key" {
		t.Errorf("Key = %q, want %q", result.Key, "test-key")
	}
	if string(result.Value) != "test-value" {
		t.Errorf("Value = %q, want %q", result.Value, "test-value")
	}
	if result.CreateRevision != 10 {
		t.Errorf("CreateRevision = %d, want 10", result.CreateRevision)
	}
	if result.ModRevision != 15 {
		t.Errorf("ModRevision = %d, want 15", result.ModRevision)
	}
	if result.Version != 3 {
		t.Errorf("Version = %d, want 3", result.Version)
	}
	if result.LeaseID != 42 {
		t.Errorf("LeaseID = %d, want 42", result.LeaseID)
	}
}

func TestProtoToMVCCKeyValue_EmptyFields(t *testing.T) {
	kv := &pb.KeyValue{}
	result := protoToMVCCKeyValue(kv)
	if result == nil {
		t.Fatal("protoToMVCCKeyValue() returned nil for empty proto")
	}
	if result.Key != nil {
		t.Errorf("Key = %v, want nil", result.Key)
	}
	if result.Value != nil {
		t.Errorf("Value = %v, want nil", result.Value)
	}
	if result.CreateRevision != 0 {
		t.Errorf("CreateRevision = %d, want 0", result.CreateRevision)
	}
}

// ---------------------------------------------------------------------------
// compareToProto tests
// ---------------------------------------------------------------------------

func TestCompareToProto_VersionTarget(t *testing.T) {
	cmp := transport.Compare{
		Key:     []byte("my-key"),
		Target:  transport.CompareVersion,
		Result:  transport.CompareEqual,
		Version: 5,
	}
	pbCmp := compareToProto(cmp)
	if pbCmp.Target != pb.CompareTarget_COMPARE_VERSION {
		t.Errorf("Target = %v, want COMPARE_VERSION", pbCmp.Target)
	}
	if pbCmp.Result != pb.CompareResult_COMPARE_EQUAL {
		t.Errorf("Result = %v, want COMPARE_EQUAL", pbCmp.Result)
	}
	if u, ok := pbCmp.TargetUnion.(*pb.Compare_Version); !ok || u.Version != 5 {
		t.Errorf("TargetUnion version = %v, want 5", pbCmp.TargetUnion)
	}
}

func TestCompareToProto_CreateRevisionTarget(t *testing.T) {
	cmp := transport.Compare{
		Key:            []byte("key"),
		Target:         transport.CompareCreateRevision,
		Result:         transport.CompareGreater,
		CreateRevision: 100,
	}
	pbCmp := compareToProto(cmp)
	if pbCmp.Target != pb.CompareTarget_COMPARE_CREATE_REVISION {
		t.Errorf("Target = %v, want COMPARE_CREATE_REVISION", pbCmp.Target)
	}
	if u, ok := pbCmp.TargetUnion.(*pb.Compare_CreateRevision); !ok || u.CreateRevision != 100 {
		t.Errorf("TargetUnion CreateRevision = %v, want 100", pbCmp.TargetUnion)
	}
}

func TestCompareToProto_ModRevisionTarget(t *testing.T) {
	cmp := transport.Compare{
		Key:         []byte("key"),
		Target:      transport.CompareModRevision,
		Result:      transport.CompareLess,
		ModRevision: 50,
	}
	pbCmp := compareToProto(cmp)
	if pbCmp.Target != pb.CompareTarget_COMPARE_MOD_REVISION {
		t.Errorf("Target = %v, want COMPARE_MOD_REVISION", pbCmp.Target)
	}
	if u, ok := pbCmp.TargetUnion.(*pb.Compare_ModRevision); !ok || u.ModRevision != 50 {
		t.Errorf("TargetUnion ModRevision = %v, want 50", pbCmp.TargetUnion)
	}
}

func TestCompareToProto_ValueTarget(t *testing.T) {
	cmp := transport.Compare{
		Key:    []byte("key"),
		Target: transport.CompareValue,
		Result: transport.CompareNotEqual,
		Value:  []byte("expected-value"),
	}
	pbCmp := compareToProto(cmp)
	if pbCmp.Target != pb.CompareTarget_COMPARE_VALUE {
		t.Errorf("Target = %v, want COMPARE_VALUE", pbCmp.Target)
	}
	if u, ok := pbCmp.TargetUnion.(*pb.Compare_Value); !ok || string(u.Value) != "expected-value" {
		t.Errorf("TargetUnion Value = %v, want expected-value", pbCmp.TargetUnion)
	}
}

// ---------------------------------------------------------------------------
// opToProto tests
// ---------------------------------------------------------------------------

func TestOpToProto_Put(t *testing.T) {
	op := transport.Op{
		Type:  transport.OpPut,
		Key:   []byte("key"),
		Value: []byte("value"),
	}
	pbOp := opToProto(op)
	if pbOp.Type != pb.OpType_OP_PUT {
		t.Errorf("Type = %v, want OP_PUT", pbOp.Type)
	}
	if string(pbOp.Key) != "key" {
		t.Errorf("Key = %q, want %q", pbOp.Key, "key")
	}
	if string(pbOp.Value) != "value" {
		t.Errorf("Value = %q, want %q", pbOp.Value, "value")
	}
}

func TestOpToProto_Get(t *testing.T) {
	op := transport.Op{
		Type: transport.OpGet,
		Key:  []byte("key"),
	}
	pbOp := opToProto(op)
	if pbOp.Type != pb.OpType_OP_GET {
		t.Errorf("Type = %v, want OP_GET", pbOp.Type)
	}
}

func TestOpToProto_Delete(t *testing.T) {
	op := transport.Op{
		Type: transport.OpDelete,
		Key:  []byte("key"),
	}
	pbOp := opToProto(op)
	if pbOp.Type != pb.OpType_OP_DELETE {
		t.Errorf("Type = %v, want OP_DELETE", pbOp.Type)
	}
}

func TestOpToProto_Range(t *testing.T) {
	op := transport.Op{
		Type:     transport.OpRange,
		Key:      []byte("start"),
		RangeEnd: []byte("end"),
	}
	pbOp := opToProto(op)
	if pbOp.Type != pb.OpType_OP_RANGE {
		t.Errorf("Type = %v, want OP_RANGE", pbOp.Type)
	}
	if string(pbOp.RangeEnd) != "end" {
		t.Errorf("RangeEnd = %q, want %q", pbOp.RangeEnd, "end")
	}
}

func TestOpToProto_WithLeaseID(t *testing.T) {
	op := transport.Op{
		Type:    transport.OpPut,
		Key:     []byte("key"),
		Value:   []byte("value"),
		LeaseID: 123,
	}
	pbOp := opToProto(op)
	if pbOp.LeaseId != 123 {
		t.Errorf("LeaseId = %d, want 123", pbOp.LeaseId)
	}
}

// ---------------------------------------------------------------------------
// protoToOpResponse tests
// ---------------------------------------------------------------------------

func TestProtoToOpResponse_Empty(t *testing.T) {
	pbResp := &pb.OpResponse{}
	result := protoToOpResponse(pbResp)
	if len(result.Kvs) != 0 {
		t.Errorf("Kvs length = %d, want 0", len(result.Kvs))
	}
	if result.Count != 0 {
		t.Errorf("Count = %d, want 0", result.Count)
	}
	if result.Succeeded {
		t.Error("Succeeded = true, want false")
	}
}

func TestProtoToOpResponse_WithKvs(t *testing.T) {
	pbResp := &pb.OpResponse{
		Kvs: []*pb.KeyValue{
			{
				Key:            []byte("key1"),
				Value:          []byte("value1"),
				CreateRevision: 1,
				ModRevision:    2,
				Version:        1,
			},
			{
				Key:            []byte("key2"),
				Value:          []byte("value2"),
				CreateRevision: 3,
				ModRevision:    4,
				Version:        1,
			},
		},
		Count:     2,
		Succeeded: true,
	}
	result := protoToOpResponse(pbResp)
	if len(result.Kvs) != 2 {
		t.Fatalf("Kvs length = %d, want 2", len(result.Kvs))
	}
	if string(result.Kvs[0].Key) != "key1" {
		t.Errorf("Kvs[0].Key = %q, want %q", result.Kvs[0].Key, "key1")
	}
	if string(result.Kvs[1].Key) != "key2" {
		t.Errorf("Kvs[1].Key = %q, want %q", result.Kvs[1].Key, "key2")
	}
	if result.Count != 2 {
		t.Errorf("Count = %d, want 2", result.Count)
	}
	if !result.Succeeded {
		t.Error("Succeeded = false, want true")
	}
}

// ---------------------------------------------------------------------------
// protoToLeaseInfo tests
// ---------------------------------------------------------------------------

func TestProtoToLeaseInfo_Nil(t *testing.T) {
	result := protoToLeaseInfo(nil)
	if result != nil {
		t.Errorf("protoToLeaseInfo(nil) = %v, want nil", result)
	}
}

func TestProtoToLeaseInfo_Valid(t *testing.T) {
	l := &pb.Lease{
		Id:           42,
		Ttl:          60,
		GrantedTtl:   60,
		RemainingTtl: 45,
		Keys:         [][]byte{[]byte("key1"), []byte("key2")},
	}
	result := protoToLeaseInfo(l)
	if result == nil {
		t.Fatal("protoToLeaseInfo() returned nil for valid input")
	}
	if result.ID != 42 {
		t.Errorf("ID = %d, want 42", result.ID)
	}
	if result.TTL != 60 {
		t.Errorf("TTL = %d, want 60", result.TTL)
	}
	if result.GrantedTTL != 60 {
		t.Errorf("GrantedTTL = %d, want 60", result.GrantedTTL)
	}
	if result.RemainingTTL != 45 {
		t.Errorf("RemainingTTL = %d, want 45", result.RemainingTTL)
	}
	if len(result.Keys) != 2 {
		t.Fatalf("Keys length = %d, want 2", len(result.Keys))
	}
	if string(result.Keys[0]) != "key1" {
		t.Errorf("Keys[0] = %q, want %q", result.Keys[0], "key1")
	}
}

func TestProtoToLeaseInfo_EmptyFields(t *testing.T) {
	l := &pb.Lease{}
	result := protoToLeaseInfo(l)
	if result == nil {
		t.Fatal("protoToLeaseInfo() returned nil for empty proto")
	}
	if result.ID != 0 {
		t.Errorf("ID = %d, want 0", result.ID)
	}
	if result.Keys != nil {
		t.Errorf("Keys = %v, want nil", result.Keys)
	}
}

// ---------------------------------------------------------------------------
// Conversion map coverage tests
// ---------------------------------------------------------------------------

func TestSortOrderToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.SortOrder
		expected pb.SortOrder
	}{
		{"SortNone", transport.SortNone, pb.SortOrder_SORT_NONE},
		{"SortAscend", transport.SortAscend, pb.SortOrder_SORT_ASCEND},
		{"SortDescend", transport.SortDescend, pb.SortOrder_SORT_DESCEND},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortOrderToProto[tt.input]
			if got != tt.expected {
				t.Errorf("sortOrderToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSortTargetToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.SortTarget
		expected pb.SortTarget
	}{
		{"SortByKey", transport.SortByKey, pb.SortTarget_SORT_BY_KEY},
		{"SortByVersion", transport.SortByVersion, pb.SortTarget_SORT_BY_VERSION},
		{"SortByCreateRevision", transport.SortByCreateRevision, pb.SortTarget_SORT_BY_CREATE_REVISION},
		{"SortByModRevision", transport.SortByModRevision, pb.SortTarget_SORT_BY_MOD_REVISION},
		{"SortByValue", transport.SortByValue, pb.SortTarget_SORT_BY_VALUE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortTargetToProto[tt.input]
			if got != tt.expected {
				t.Errorf("sortTargetToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCompareTargetToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.CompareTarget
		expected pb.CompareTarget
	}{
		{"CompareVersion", transport.CompareVersion, pb.CompareTarget_COMPARE_VERSION},
		{"CompareCreateRevision", transport.CompareCreateRevision, pb.CompareTarget_COMPARE_CREATE_REVISION},
		{"CompareModRevision", transport.CompareModRevision, pb.CompareTarget_COMPARE_MOD_REVISION},
		{"CompareValue", transport.CompareValue, pb.CompareTarget_COMPARE_VALUE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareTargetToProto[tt.input]
			if got != tt.expected {
				t.Errorf("compareTargetToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCompareResultToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.CompareResult
		expected pb.CompareResult
	}{
		{"CompareEqual", transport.CompareEqual, pb.CompareResult_COMPARE_EQUAL},
		{"CompareGreater", transport.CompareGreater, pb.CompareResult_COMPARE_GREATER},
		{"CompareLess", transport.CompareLess, pb.CompareResult_COMPARE_LESS},
		{"CompareNotEqual", transport.CompareNotEqual, pb.CompareResult_COMPARE_NOT_EQUAL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareResultToProto[tt.input]
			if got != tt.expected {
				t.Errorf("compareResultToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestOpTypeToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    transport.OpType
		expected pb.OpType
	}{
		{"OpPut", transport.OpPut, pb.OpType_OP_PUT},
		{"OpGet", transport.OpGet, pb.OpType_OP_GET},
		{"OpDelete", transport.OpDelete, pb.OpType_OP_DELETE},
		{"OpRange", transport.OpRange, pb.OpType_OP_RANGE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := opTypeToProto[tt.input]
			if got != tt.expected {
				t.Errorf("opTypeToProto[%v] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for MVCC methods
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Closed state tests for Lease methods
// ---------------------------------------------------------------------------
