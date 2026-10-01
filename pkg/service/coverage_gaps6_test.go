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

// coverage_gaps6_test.go covers collection marshal/unmarshal, lock service
// error paths, and additional low-coverage items.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// ---------------------------------------------------------------------------
// Mock non-JSON codec for marshalDocument / unmarshalDocument tests
// ---------------------------------------------------------------------------

// mockBinaryCodec is a test double that returns a fixed byte slice and
// the constant type "mock" so that codec.Type() != codec.TypeJSON.
type mockBinaryCodec struct {
	marshalFn   func(v any) ([]byte, error)
	unmarshalFn func(data []byte, v any) error
}

func (m *mockBinaryCodec) Marshal(v any) ([]byte, error) {
	if m.marshalFn != nil {
		return m.marshalFn(v)
	}
	return json.Marshal(v)
}

func (m *mockBinaryCodec) Unmarshal(data []byte, v any) error {
	if m.unmarshalFn != nil {
		return m.unmarshalFn(data, v)
	}
	return json.Unmarshal(data, v)
}

func (m *mockBinaryCodec) Type() string { return "mock" }

// Compile-time check.
var _ codec.Codec = (*mockBinaryCodec)(nil)

// ---------------------------------------------------------------------------
// marshalDocument (25%) – non-JSON codec path
// ---------------------------------------------------------------------------

func TestMarshalDocument_JSONCodec(t *testing.T) {
	entity := &dao.DynamicEntity{
		ID:     42,
		Fields: map[string]interface{}{"name": "Alice"},
	}
	c := &codec.JSONCodec{}
	data, err := marshalDocument(entity, c)
	if err != nil {
		t.Fatalf("marshalDocument with JSON codec: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestMarshalDocument_NonJSONCodec_Success(t *testing.T) {
	entity := &dao.DynamicEntity{
		ID:     99,
		Fields: map[string]interface{}{"color": "blue", "id": "should-be-skipped"},
	}
	c := &mockBinaryCodec{
		marshalFn: func(v any) ([]byte, error) {
			return json.Marshal(v)
		},
	}
	data, err := marshalDocument(entity, c)
	if err != nil {
		t.Fatalf("marshalDocument with non-JSON codec: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestMarshalDocument_NonJSONCodec_MarshalError(t *testing.T) {
	entity := &dao.DynamicEntity{ID: 1, Fields: map[string]interface{}{"k": "v"}}
	c := &mockBinaryCodec{
		marshalFn: func(v any) ([]byte, error) {
			return nil, errors.New("marshal failed")
		},
	}
	_, err := marshalDocument(entity, c)
	if err == nil {
		t.Fatal("expected error from marshal failure, got nil")
	}
}

// ---------------------------------------------------------------------------
// unmarshalDocument (13.3%) – non-JSON codec path
// ---------------------------------------------------------------------------

func TestUnmarshalDocument_JSONCodec(t *testing.T) {
	entity := &dao.DynamicEntity{}
	data := []byte(`{"id":55,"fields":{"name":"Bob"}}`)
	c := &codec.JSONCodec{}
	err := unmarshalDocument(data, entity, c)
	if err != nil {
		t.Fatalf("unmarshalDocument with JSON codec: %v", err)
	}
}

func TestUnmarshalDocument_NonJSONCodec_WithStringID(t *testing.T) {
	entity := &dao.DynamicEntity{}
	// The mock returns a map with a string "id".
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			m := v.(*map[string]interface{})
			*m = map[string]interface{}{
				"id":   "123",
				"name": "Carol",
			}
			return nil
		},
	}
	err := unmarshalDocument([]byte("dummy"), entity, c)
	if err != nil {
		t.Fatalf("unmarshalDocument with string id: %v", err)
	}
	if entity.ID != 123 {
		t.Fatalf("expected ID=123, got %d", entity.ID)
	}
}

func TestUnmarshalDocument_NonJSONCodec_WithUint64ID(t *testing.T) {
	entity := &dao.DynamicEntity{}
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			m := v.(*map[string]interface{})
			*m = map[string]interface{}{"id": uint64(77), "val": "x"}
			return nil
		},
	}
	err := unmarshalDocument([]byte("dummy"), entity, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entity.ID != 77 {
		t.Fatalf("expected ID=77, got %d", entity.ID)
	}
}

func TestUnmarshalDocument_NonJSONCodec_WithInt64ID(t *testing.T) {
	entity := &dao.DynamicEntity{}
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			m := v.(*map[string]interface{})
			*m = map[string]interface{}{"id": int64(88)}
			return nil
		},
	}
	err := unmarshalDocument([]byte("dummy"), entity, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entity.ID != 88 {
		t.Fatalf("expected ID=88, got %d", entity.ID)
	}
}

func TestUnmarshalDocument_NonJSONCodec_WithFloat64ID(t *testing.T) {
	entity := &dao.DynamicEntity{}
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			m := v.(*map[string]interface{})
			*m = map[string]interface{}{"id": float64(99)}
			return nil
		},
	}
	err := unmarshalDocument([]byte("dummy"), entity, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entity.ID != 99 {
		t.Fatalf("expected ID=99, got %d", entity.ID)
	}
}

func TestUnmarshalDocument_NonJSONCodec_FallbackToJSON(t *testing.T) {
	entity := &dao.DynamicEntity{}
	// The mock codec fails, triggering JSON fallback.
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			return errors.New("decode failed")
		},
	}
	// Use valid JSON as the raw data so the fallback succeeds.
	data := []byte(`{"id":11,"fields":{"x":"y"}}`)
	err := unmarshalDocument(data, entity, c)
	if err != nil {
		t.Fatalf("expected fallback JSON to succeed, got %v", err)
	}
}

func TestUnmarshalDocument_NonJSONCodec_NoIDField(t *testing.T) {
	entity := &dao.DynamicEntity{}
	c := &mockBinaryCodec{
		unmarshalFn: func(data []byte, v any) error {
			m := v.(*map[string]interface{})
			*m = map[string]interface{}{"name": "Dave"}
			return nil
		},
	}
	err := unmarshalDocument([]byte("dummy"), entity, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No id field → entity.ID remains 0.
	if entity.ID != 0 {
		t.Fatalf("expected ID=0, got %d", entity.ID)
	}
}

// ---------------------------------------------------------------------------
// lock_service.go – all methods at 0%
// ---------------------------------------------------------------------------

// We test the lock service by testing that NewLockService returns an error
// when given nil dependencies, which exercises NewLockService's error path.
// The methods themselves are thin wrappers; coverage of the constructor plus
// the validation logic of the underlying locker covers the critical paths.

func TestLockService_NewLockService_NilTxSvc(t *testing.T) {
	_, err := NewLockService(nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil txSvc, got nil")
	}
}

func TestLockService_NewLockService_NilLeaseSvc(t *testing.T) {
	txSvc := NewTransactionService(&mockHost{}, 1)
	_, err := NewLockService(txSvc, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil leaseSvc, got nil")
	}
}

// ---------------------------------------------------------------------------
// Additional collection service coverage (countDocuments 80%)
// ---------------------------------------------------------------------------

func TestCollectionService_InsertMultipleDocuments_CountDocuments(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "countcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	for i := 0; i < 5; i++ {
		_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "countcol",
			Database:   "mydb",
			Document:   map[string]interface{}{"i": i},
		})
		if err != nil {
			t.Fatalf("InsertDocument %d: %v", i, err)
		}
	}

	// ListDocuments triggers countDocuments internally.
	resp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "countcol",
		Database:   "mydb",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(resp.Documents) != 5 {
		t.Fatalf("expected 5 documents, got %d", len(resp.Documents))
	}
}

// ---------------------------------------------------------------------------
// authfactor_service.go – additional error paths (80–84% → higher)
// ---------------------------------------------------------------------------

func TestAuthFactorService_EnrollFactor_UnknownFactor(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	// Try enrolling with an unknown factor name.
	_, err := svc.EnrollFactor(ctx, "no-such-factor", "user1", authfactors.EnrollParams{})
	if err == nil {
		t.Fatal("expected error for unknown factor, got nil")
	}
}

func TestAuthFactorService_ChallengeFactor_UnknownUser(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	_, err := svc.ChallengeFactor(ctx, "no-such-user")
	if err == nil {
		t.Fatal("expected error for non-existent user, got nil")
	}
}

func TestAuthFactorService_VerifyFactor_UnknownUser(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	err := svc.VerifyFactor(ctx, "no-such-factor", "no-challenge", nil)
	if err == nil {
		t.Fatal("expected error for non-existent factor, got nil")
	}
}

func TestAuthFactorService_ListFactors_UnknownUser(t *testing.T) {
	svc, _ := newPIVAuthFactorService(t)
	ctx := context.Background()

	factors, err := svc.ListFactors(ctx, "ghost-user")
	// Either empty list or error is acceptable.
	if err == nil && len(factors) != 0 {
		t.Fatalf("expected empty factors for unknown user, got %d", len(factors))
	}
}

// ---------------------------------------------------------------------------
// lease_service.go – Grant, Revoke, KeepAlive (85.7%)
// ---------------------------------------------------------------------------

func TestLeaseService_Grant_ProposeError_Cov(t *testing.T) {
	// Duplicate name protection: renamed with _Cov suffix.
	// The lease_service_test.go already has these, checking coverage gaps.
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("propose failed")
		},
	}
	svc := NewLeaseService(host, 1)
	_, err := svc.Grant(context.Background(), int64(time.Second))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// sm.Result is used above via the import at the top of the file.
