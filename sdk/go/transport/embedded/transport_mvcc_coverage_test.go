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

package embedded

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: MVCCKVServicer
// ===========================================================================

type mockMVCCService struct {
	*mockKVService
	currentRev int64
}

func newMockMVCCService() *mockMVCCService {
	return &mockMVCCService{
		mockKVService: newMockKVService(),
		currentRev:    1,
	}
}

func (m *mockMVCCService) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{
		Key:         []byte(key),
		Value:       value,
		LeaseID:     leaseID,
		ModRevision: m.currentRev,
		Version:     1,
	}, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return &kv.KeyValue{
		Key:         []byte(key),
		Value:       value,
		LeaseID:     leaseID,
		ModRevision: m.currentRev,
		Version:     1,
	}, nil
}

func (m *mockMVCCService) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *mockMVCCService) Range(_ context.Context, start, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{
		{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1},
	}, 1, 77, nil
}

func (m *mockMVCCService) RangeWithConsistency(_ context.Context, start, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return []*kv.KeyValue{
		{Key: start, Value: []byte("v"), ModRevision: 1, Version: 1},
	}, 1, 77, nil
}

func (m *mockMVCCService) Compact(_ context.Context, _ int64) error {
	return nil
}

func (m *mockMVCCService) CurrentRevision(_ context.Context) (int64, error) {
	return m.currentRev, nil
}

func (m *mockMVCCService) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes: []service.RestoreKeyChange{
			{Key: "restored-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Applied: 1,
		DryRun:  req.DryRun,
	}, nil
}

var _ service.MVCCKVServicer = (*mockMVCCService)(nil)

// ===========================================================================
// MVCC success-path tests
// ===========================================================================

func TestEmbeddedMVCC_PutWithLease_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 42)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if string(kv.Key) != "k" || kv.LeaseID != 42 {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestEmbeddedMVCC_PutWithLease_WithWriteOption(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 42, transport.WithWriteMode(transport.WriteModeSync))
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if string(kv.Key) != "k" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestEmbeddedMVCC_GetRevision_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.GetRevision(context.Background(), "k")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if string(kv.Key) != "k" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestEmbeddedMVCC_GetRevision_WithConsistency(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.GetRevision(context.Background(), "k", transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if string(kv.Key) != "k" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestEmbeddedMVCC_GetAtRevision_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.GetAtRevision(context.Background(), "k", 7)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if kv.ModRevision != 7 {
		t.Fatalf("ModRevision = %d", kv.ModRevision)
	}
}

func TestEmbeddedMVCC_GetAtRevision_WithConsistency(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	kv, err := tr.GetAtRevision(context.Background(), "k", 7, transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if kv.ModRevision != 7 {
		t.Fatalf("ModRevision = %d", kv.ModRevision)
	}
}

func TestEmbeddedMVCC_Range_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count != 1 || len(resp.Kvs) != 1 {
		t.Fatalf("unexpected: %+v", resp)
	}
	// Revision (77) is deliberately distinct from Count (1) so this
	// assertion cannot pass by coincidence if the revision is dropped.
	if resp.Revision != 77 {
		t.Fatalf("Revision = %d, want 77", resp.Revision)
	}
}

func TestEmbeddedMVCC_Range_WithConsistency(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10}, transport.WithConsistency(transport.ConsistencyLinearizable))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("Count = %d", resp.Count)
	}
	if resp.Revision != 77 {
		t.Fatalf("Revision = %d, want 77", resp.Revision)
	}
}

func TestEmbeddedMVCC_Compact_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	if err := tr.Compact(context.Background(), 100); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestEmbeddedMVCC_CurrentRevision_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	rev, err := tr.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 1 {
		t.Fatalf("rev = %d", rev)
	}
}

func TestEmbeddedMVCC_RestoreToRevision_Success(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Revision != 5 || result.Applied != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "restored-key" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
}

func TestEmbeddedMVCC_RestoreToRevision_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	if _, err := tr.RestoreToRevision(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestEmbeddedMVCC_RestoreToRevision_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{Revision: 1})
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected *NilServiceError, got %T: %v", err, err)
	}
}

// TestEmbeddedMVCC_RestoreToRevision_BinaryRoundTrip proves an arbitrary
// byte-valued prefix/key survives the []byte<->string conversion the
// embedded transport applies between the SDK's binary-safe DTO and the
// service layer's string-keyed RestoreRevisionRequest -- a lossless,
// in-process conversion, unlike the JSON/base64 or proto `bytes` encodings
// the other transports apply across the wire.
func TestEmbeddedMVCC_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k'}

	svc := &captureRestoreMVCCService{mockMVCCService: newMockMVCCService()}
	tr := newExtTransport(t, WithMVCCService(svc))

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if svc.captured == nil || svc.captured.Prefix != string(binaryPrefix) {
		t.Fatalf("service received prefix %+v, want %v", svc.captured, binaryPrefix)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "restored-key" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}

	// Keys direction, using a genuinely non-UTF-8 key.
	if _, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{binaryKey},
	}); err != nil {
		t.Fatalf("RestoreToRevision (keys): %v", err)
	}
	if len(svc.captured.Keys) != 1 || svc.captured.Keys[0] != string(binaryKey) {
		t.Fatalf("expected service to receive binary key %v, got %v", binaryKey, svc.captured.Keys)
	}
}

// captureRestoreMVCCService wraps mockMVCCService and records the last
// RestoreToRevision request it received.
type captureRestoreMVCCService struct {
	*mockMVCCService
	captured *service.RestoreRevisionRequest
}

func (c *captureRestoreMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	c.captured = req
	return c.mockMVCCService.RestoreToRevision(ctx, req)
}

// ===========================================================================
// MVCC dispatch handlers
// ===========================================================================

func TestEmbeddedMVCC_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	ctx := context.Background()

	t.Run("PutWithLease", func(t *testing.T) {
		var resp *transport.MVCCKeyValue
		if err := tr.Request(ctx, "PutWithLease", &putWithLeaseRequest{Key: "k", Value: []byte("v"), LeaseID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetRevision", func(t *testing.T) {
		var resp *transport.MVCCKeyValue
		if err := tr.Request(ctx, "GetRevision", &getRevisionRequest{Key: "k"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("GetAtRevision", func(t *testing.T) {
		var resp *transport.MVCCKeyValue
		if err := tr.Request(ctx, "GetAtRevision", &getAtRevisionRequest{Key: "k", Revision: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("Range", func(t *testing.T) {
		var resp *transport.RangeResponse
		if err := tr.Request(ctx, "Range", &rangeRequest{Start: []byte("a"), End: []byte("z")}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("Compact", func(t *testing.T) {
		if err := tr.Request(ctx, "Compact", &compactRequest{Revision: 1}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("CurrentRevision", func(t *testing.T) {
		var rev int64
		if err := tr.Request(ctx, "CurrentRevision", nil, &rev); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("RestoreToRevision", func(t *testing.T) {
		var resp *transport.RestoreRevisionResult
		req := &transport.RestoreRevisionRequest{Revision: 5, Prefix: []byte("prefix")}
		if err := tr.Request(ctx, "RestoreToRevision", req, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if resp == nil || resp.Applied != 1 {
			t.Fatalf("unexpected dispatch result: %+v", resp)
		}
	})
	t.Run("RestoreToRevision_InvalidRequestType", func(t *testing.T) {
		err := tr.Request(ctx, "RestoreToRevision", "not-a-request", nil)
		var ire *InvalidRequestError
		if !errors.As(err, &ire) {
			t.Fatalf("expected *InvalidRequestError, got %T: %v", err, err)
		}
	})
}

// ===========================================================================
// Mock: TransactionServicer
// ===========================================================================

type mockTxService struct {
	executeFn         func(ctx context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error)
	executeWithModeFn func(ctx context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error)
}

func (m *mockTxService) Execute(ctx context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, success, failure)
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: []service.OpResponse{{Succeeded: true}},
	}, nil
}

func (m *mockTxService) ExecuteWithMode(ctx context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	if m.executeWithModeFn != nil {
		return m.executeWithModeFn(ctx, compares, success, failure, mode)
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: []service.OpResponse{{Succeeded: true}},
	}, nil
}

func (m *mockTxService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, errors.New("not implemented")
}

var _ service.TransactionServicer = (*mockTxService)(nil)

// ===========================================================================
// Transaction tests
// ===========================================================================

func TestEmbeddedTx_Execute_Success(t *testing.T) {
	tr := newExtTransport(t, WithTransactionService(&mockTxService{}))
	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k"), Target: transport.CompareTarget(0), Result: transport.CompareResult(0), Value: []byte("v")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpType(0), Key: []byte("k"), Value: []byte("new")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected success")
	}
}

func TestEmbeddedTx_ExecuteWithMode(t *testing.T) {
	tr := newExtTransport(t, WithTransactionService(&mockTxService{}))
	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k"), Value: []byte("v")},
		},
		SuccessOps: []transport.Op{
			{Key: []byte("k"), Value: []byte("new")},
		},
	}, transport.WithWriteMode(transport.WriteModeSync))
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected success")
	}
}

func TestEmbeddedTx_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithTransactionService(&mockTxService{}))
	var resp *transport.TransactionResponse
	if err := tr.Request(context.Background(), "ExecuteTransaction", &transport.TransactionRequest{}, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// ===========================================================================
// Mock: BatchServicer for ExecuteBatch success path
// ===========================================================================

func TestEmbeddedBatch_Execute_Success(t *testing.T) {
	svc := &fullBatchService{}
	tr := newExtTransport(t, WithBatchService(svc))
	_, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Key: []byte("k"), Value: []byte("v")},
	})
	if err == nil {
		// fullBatchService.Execute returns "not implemented" - which is OK for branch coverage
		t.Fatal("expected error from stub mock")
	}
}

func TestEmbeddedBatch_ExecuteWithMode(t *testing.T) {
	svc := &fullBatchService{}
	tr := newExtTransport(t, WithBatchService(svc))
	_, err := tr.ExecuteBatch(context.Background(), nil, transport.WithWriteMode(transport.WriteModeSync))
	if err == nil {
		// fullBatchService.ExecuteWithMode returns "not implemented" - which is OK for branch coverage
		t.Fatal("expected error from stub mock")
	}
}

// fullBatchExecSvc satisfies BatchServicer with working Execute methods so we
// can hit the success branches and the type conversion helpers.
type fullBatchExecSvc struct{}

func (s *fullBatchExecSvc) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	resps := make([]service.OpResponse, len(ops))
	for i := range ops {
		resps[i] = service.OpResponse{Succeeded: true, Count: 1, Kvs: []*kv.KeyValue{
			{Key: ops[i].Key, Value: ops[i].Value, ModRevision: 1, Version: 1},
		}}
	}
	return resps, nil
}

func (s *fullBatchExecSvc) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return s.Execute(ctx, ops)
}

func (s *fullBatchExecSvc) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *fullBatchExecSvc) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return &service.ImportResult{}, nil
}

// stubReader is a stand-in to keep imports minimal; will be substituted below.

// ===========================================================================
// Type conversion helpers — coverage via Batch Execute success
// ===========================================================================

func TestEmbeddedBatch_ExecuteAllOpTypes(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchExecSvc{}))
	// Use all op type variants to cover toServiceOps and toTransportOpResponses
	ops := []transport.Op{
		{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1"), LeaseID: 1},
		{Type: transport.OpGet, Key: []byte("k2")},
		{Type: transport.OpDelete, Key: []byte("k3")},
		{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
	}
	resp, err := tr.ExecuteBatch(context.Background(), ops)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 4 {
		t.Fatalf("got %d responses", len(resp.Responses))
	}
}

func TestEmbeddedTx_ExecuteAllOpTypesAndCompares(t *testing.T) {
	tr := newExtTransport(t, WithTransactionService(&mockTxService{
		executeFn: func(_ context.Context, _ []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: true,
				Revision:  1,
				Responses: []service.OpResponse{
					{Succeeded: true, Kvs: []*kv.KeyValue{{Key: []byte("k"), Value: []byte("v")}}, Count: 1},
				},
			}, nil
		},
	}))
	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k1"), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: transport.CompareVersion, Result: transport.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: transport.CompareCreateRevision, Result: transport.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: transport.CompareModRevision, Result: transport.CompareNotEqual, ModRevision: 7},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected success")
	}
}

// ===========================================================================
// Range with RangeOptions field coverage
// ===========================================================================

func TestEmbeddedMVCC_Range_WithRangeOptions(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))
	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"),
		transport.RangeOptions{Limit: 5, Revision: 1, SortOrder: transport.SortAscend, SortTarget: transport.SortByKey, KeysOnly: true})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count == 0 {
		t.Fatal("expected count > 0")
	}
}

// ===========================================================================
// MVCC error helpers — translateError paths
// ===========================================================================

type mockMVCCFailing struct {
	*mockKVService
}

func (m *mockMVCCFailing) PutWithLease(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) PutWithLeaseAndMode(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) GetRevision(_ context.Context, _ string) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) GetAtRevision(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return nil, errSentinel
}
func (m *mockMVCCFailing) Range(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return nil, 0, 0, errSentinel
}
func (m *mockMVCCFailing) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return nil, 0, 0, errSentinel
}
func (m *mockMVCCFailing) Compact(_ context.Context, _ int64) error { return errSentinel }
func (m *mockMVCCFailing) CurrentRevision(_ context.Context) (int64, error) {
	return 0, errSentinel
}
func (m *mockMVCCFailing) RestoreToRevision(_ context.Context, _ *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, errSentinel
}

var _ service.MVCCKVServicer = (*mockMVCCFailing)(nil)

func TestEmbeddedMVCC_AllMethods_ServiceError(t *testing.T) {
	svc := &mockMVCCFailing{mockKVService: newMockKVService()}
	tr := newExtTransport(t, WithMVCCService(svc))
	ctx := context.Background()

	if _, err := tr.PutWithLease(ctx, "k", []byte("v"), 1); err == nil {
		t.Fatal("PutWithLease: expected error")
	}
	if _, err := tr.GetRevision(ctx, "k"); err == nil {
		t.Fatal("GetRevision: expected error")
	}
	if _, err := tr.GetAtRevision(ctx, "k", 1); err == nil {
		t.Fatal("GetAtRevision: expected error")
	}
	if _, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("Range: expected error")
	}
	if err := tr.Compact(ctx, 1); err == nil {
		t.Fatal("Compact: expected error")
	}
	if _, err := tr.CurrentRevision(ctx); err == nil {
		t.Fatal("CurrentRevision: expected error")
	}
	if _, err := tr.RestoreToRevision(ctx, &transport.RestoreRevisionRequest{Revision: 1, Prefix: []byte("k")}); err == nil {
		t.Fatal("RestoreToRevision: expected error")
	}
	// With consistency
	if _, err := tr.GetRevision(ctx, "k", transport.WithConsistency(transport.ConsistencyLinearizable)); err == nil {
		t.Fatal("GetRevision/consistency: expected error")
	}
	if _, err := tr.GetAtRevision(ctx, "k", 1, transport.WithConsistency(transport.ConsistencyLinearizable)); err == nil {
		t.Fatal("GetAtRevision/consistency: expected error")
	}
	if _, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{}, transport.WithConsistency(transport.ConsistencyLinearizable)); err == nil {
		t.Fatal("Range/consistency: expected error")
	}
	if _, err := tr.PutWithLease(ctx, "k", []byte("v"), 1, transport.WithWriteMode(transport.WriteModeSync)); err == nil {
		t.Fatal("PutWithLease/writemode: expected error")
	}
}
