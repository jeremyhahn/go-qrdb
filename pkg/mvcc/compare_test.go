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

package mvcc

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Evaluate - CompareVersion
// ---------------------------------------------------------------------------

func TestEvaluate_CompareVersion(t *testing.T) {
	tests := []struct {
		name   string
		kvVer  int64
		cmpVer int64
		op     service.CompareResult
		want   bool
	}{
		{"equal_match", 5, 5, service.CompareEqual, true},
		{"equal_no_match", 5, 6, service.CompareEqual, false},
		{"not_equal_match", 5, 6, service.CompareNotEqual, true},
		{"not_equal_no_match", 5, 5, service.CompareNotEqual, false},
		{"greater_match", 10, 5, service.CompareGreater, true},
		{"greater_no_match", 5, 10, service.CompareGreater, false},
		{"greater_equal", 5, 5, service.CompareGreater, false},
		{"less_match", 3, 10, service.CompareLess, true},
		{"less_no_match", 10, 3, service.CompareLess, false},
		{"less_equal", 5, 5, service.CompareLess, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv := &service.KeyValue{Version: tc.kvVer}
			cmp := &service.Compare{
				Target:  service.CompareVersion,
				Result:  tc.op,
				Version: tc.cmpVer,
			}
			got := Evaluate(cmp, kv)
			if got != tc.want {
				t.Errorf("Evaluate: got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Evaluate - CompareCreateRevision
// ---------------------------------------------------------------------------

func TestEvaluate_CompareCreateRevision(t *testing.T) {
	tests := []struct {
		name   string
		kvRev  int64
		cmpRev int64
		op     service.CompareResult
		want   bool
	}{
		{"equal_match", 10, 10, service.CompareEqual, true},
		{"equal_no_match", 10, 20, service.CompareEqual, false},
		{"not_equal_match", 10, 20, service.CompareNotEqual, true},
		{"not_equal_no_match", 10, 10, service.CompareNotEqual, false},
		{"greater_match", 20, 10, service.CompareGreater, true},
		{"greater_no_match", 10, 20, service.CompareGreater, false},
		{"less_match", 5, 10, service.CompareLess, true},
		{"less_no_match", 10, 5, service.CompareLess, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv := &service.KeyValue{CreateRevision: tc.kvRev}
			cmp := &service.Compare{
				Target:         service.CompareCreateRevision,
				Result:         tc.op,
				CreateRevision: tc.cmpRev,
			}
			got := Evaluate(cmp, kv)
			if got != tc.want {
				t.Errorf("Evaluate: got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Evaluate - CompareModRevision
// ---------------------------------------------------------------------------

func TestEvaluate_CompareModRevision(t *testing.T) {
	tests := []struct {
		name   string
		kvRev  int64
		cmpRev int64
		op     service.CompareResult
		want   bool
	}{
		{"equal_match", 15, 15, service.CompareEqual, true},
		{"equal_no_match", 15, 25, service.CompareEqual, false},
		{"not_equal_match", 15, 25, service.CompareNotEqual, true},
		{"not_equal_no_match", 15, 15, service.CompareNotEqual, false},
		{"greater_match", 30, 15, service.CompareGreater, true},
		{"greater_no_match", 15, 30, service.CompareGreater, false},
		{"less_match", 10, 30, service.CompareLess, true},
		{"less_no_match", 30, 10, service.CompareLess, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv := &service.KeyValue{ModRevision: tc.kvRev}
			cmp := &service.Compare{
				Target:      service.CompareModRevision,
				Result:      tc.op,
				ModRevision: tc.cmpRev,
			}
			got := Evaluate(cmp, kv)
			if got != tc.want {
				t.Errorf("Evaluate: got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Evaluate - CompareValue (bytes)
// ---------------------------------------------------------------------------

func TestEvaluate_CompareValue(t *testing.T) {
	tests := []struct {
		name   string
		kvVal  []byte
		cmpVal []byte
		op     service.CompareResult
		want   bool
	}{
		{"equal_match", []byte("abc"), []byte("abc"), service.CompareEqual, true},
		{"equal_no_match", []byte("abc"), []byte("xyz"), service.CompareEqual, false},
		{"not_equal_match", []byte("abc"), []byte("xyz"), service.CompareNotEqual, true},
		{"not_equal_no_match", []byte("abc"), []byte("abc"), service.CompareNotEqual, false},
		{"greater_match", []byte("xyz"), []byte("abc"), service.CompareGreater, true},
		{"greater_no_match", []byte("abc"), []byte("xyz"), service.CompareGreater, false},
		{"less_match", []byte("abc"), []byte("xyz"), service.CompareLess, true},
		{"less_no_match", []byte("xyz"), []byte("abc"), service.CompareLess, false},
		{"nil_vs_nil_equal", nil, nil, service.CompareEqual, true},
		{"nil_vs_empty_equal", nil, []byte{}, service.CompareEqual, true},
		{"empty_vs_value_less", []byte{}, []byte("a"), service.CompareLess, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kv := &service.KeyValue{Value: tc.kvVal}
			cmp := &service.Compare{
				Target: service.CompareValue,
				Result: tc.op,
				Value:  tc.cmpVal,
			}
			got := Evaluate(cmp, kv)
			if got != tc.want {
				t.Errorf("Evaluate: got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Evaluate - nil KeyValue (non-existent key)
// ---------------------------------------------------------------------------

func TestEvaluate_NilKeyValue_VersionZero(t *testing.T) {
	cmp := &service.Compare{
		Target:  service.CompareVersion,
		Result:  service.CompareEqual,
		Version: 0,
	}
	if !Evaluate(cmp, nil) {
		t.Error("Evaluate: nil kv should have version 0")
	}
}

func TestEvaluate_NilKeyValue_CreateRevisionZero(t *testing.T) {
	cmp := &service.Compare{
		Target:         service.CompareCreateRevision,
		Result:         service.CompareEqual,
		CreateRevision: 0,
	}
	if !Evaluate(cmp, nil) {
		t.Error("Evaluate: nil kv should have create revision 0")
	}
}

func TestEvaluate_NilKeyValue_ModRevisionZero(t *testing.T) {
	cmp := &service.Compare{
		Target:      service.CompareModRevision,
		Result:      service.CompareEqual,
		ModRevision: 0,
	}
	if !Evaluate(cmp, nil) {
		t.Error("Evaluate: nil kv should have mod revision 0")
	}
}

func TestEvaluate_NilKeyValue_ValueNil(t *testing.T) {
	cmp := &service.Compare{
		Target: service.CompareValue,
		Result: service.CompareEqual,
		Value:  nil,
	}
	if !Evaluate(cmp, nil) {
		t.Error("Evaluate: nil kv should have nil value")
	}
}

// ---------------------------------------------------------------------------
// Evaluate - unknown target
// ---------------------------------------------------------------------------

func TestEvaluate_UnknownTarget_ReturnsFalse(t *testing.T) {
	kv := &service.KeyValue{Version: 1}
	cmp := &service.Compare{
		Target: service.CompareTarget(255),
		Result: service.CompareEqual,
	}
	if Evaluate(cmp, kv) {
		t.Error("Evaluate: unknown target should return false")
	}
}

func TestEvaluate_UnknownTarget_NilKV_ReturnsFalse(t *testing.T) {
	cmp := &service.Compare{
		Target: service.CompareTarget(200),
		Result: service.CompareEqual,
	}
	if Evaluate(cmp, nil) {
		t.Error("Evaluate: unknown target with nil kv should return false")
	}
}

// ---------------------------------------------------------------------------
// compareInt64 - unknown operator
// ---------------------------------------------------------------------------

func TestCompareInt64_UnknownOperator_ReturnsFalse(t *testing.T) {
	got := compareInt64(5, 5, service.CompareResult(255))
	if got {
		t.Error("compareInt64: unknown operator should return false")
	}
}

func TestCompareInt64_UnknownOperator_DifferentValues(t *testing.T) {
	got := compareInt64(10, 5, service.CompareResult(100))
	if got {
		t.Error("compareInt64: unknown operator with different values should return false")
	}
}

// ---------------------------------------------------------------------------
// compareBytes - unknown operator
// ---------------------------------------------------------------------------

func TestCompareBytes_UnknownOperator_ReturnsFalse(t *testing.T) {
	got := compareBytes([]byte("a"), []byte("a"), service.CompareResult(255))
	if got {
		t.Error("compareBytes: unknown operator should return false")
	}
}

func TestCompareBytes_UnknownOperator_DifferentValues(t *testing.T) {
	got := compareBytes([]byte("a"), []byte("b"), service.CompareResult(100))
	if got {
		t.Error("compareBytes: unknown operator with different values should return false")
	}
}
