// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for the MVCC, Transaction, and Batch services over the
// Unix domain socket transport. Each test wires a real gRPC-over-UDS server
// with a stub service implementation and asserts the SDK transport correctly
// marshals + unmarshals across the wire.

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockMVCCSvc — service.MVCCKVServicer stub
// ---------------------------------------------------------------------------
//
// The MVCC service embeds a KV service. We satisfy the KVServicer surface by
// delegating to the existing newMockKVService used by other round-trip tests.

type mockMVCCSvc struct {
	*mockKVService
	putWithLeaseFn func(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error)
	getRevisionFn  func(ctx context.Context, key string) (*kv.KeyValue, error)
	getAtRevFn     func(ctx context.Context, key string, revision int64) (*kv.KeyValue, error)
	rangeFn        func(ctx context.Context, start, end []byte, opts service.RangeOptions) ([]*kv.KeyValue, int64, error)
	compactFn      func(ctx context.Context, revision int64) error
	currentRevFn   func(ctx context.Context) (int64, error)
}

func newMockMVCCSvc() *mockMVCCSvc {
	return &mockMVCCSvc{mockKVService: newMockKVService()}
}

func (m *mockMVCCSvc) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if m.putWithLeaseFn != nil {
		return m.putWithLeaseFn(ctx, key, value, leaseID)
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          value,
		LeaseID:        leaseID,
		CreateRevision: 1,
		ModRevision:    1,
		Version:        1,
	}, nil
}

func (m *mockMVCCSvc) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return m.PutWithLease(ctx, key, value, leaseID)
}

func (m *mockMVCCSvc) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          []byte("rev-value"),
		CreateRevision: 1,
		ModRevision:    5,
		Version:        2,
	}, nil
}

func (m *mockMVCCSvc) GetRevisionWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetRevision(ctx, key)
}

func (m *mockMVCCSvc) GetAtRevision(ctx context.Context, key string, revision int64) (*kv.KeyValue, error) {
	if m.getAtRevFn != nil {
		return m.getAtRevFn(ctx, key, revision)
	}
	return &kv.KeyValue{
		Key:         []byte(key),
		Value:       []byte("history"),
		ModRevision: revision,
		Version:     1,
	}, nil
}

func (m *mockMVCCSvc) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetAtRevision(ctx, key, revision)
}

func (m *mockMVCCSvc) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	var ro service.RangeOptions
	for _, o := range opts {
		o(&ro)
	}
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, ro)
	}
	return []*kv.KeyValue{
		{Key: []byte("k1"), Value: []byte("v1"), ModRevision: 10},
		{Key: []byte("k2"), Value: []byte("v2"), ModRevision: 11},
	}, 2, nil
}

func (m *mockMVCCSvc) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	return m.Range(ctx, start, end, opts...)
}

func (m *mockMVCCSvc) Compact(ctx context.Context, revision int64) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, revision)
	}
	return nil
}

func (m *mockMVCCSvc) CurrentRevision(ctx context.Context) (int64, error) {
	if m.currentRevFn != nil {
		return m.currentRevFn(ctx)
	}
	return 42, nil
}

var _ service.MVCCKVServicer = (*mockMVCCSvc)(nil)

// ---------------------------------------------------------------------------
// MVCC round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_PutWithLease(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	got, err := tr.PutWithLease(context.Background(), "k1", []byte("hello"), 7)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if got == nil || string(got.Key) != "k1" || string(got.Value) != "hello" {
		t.Errorf("got %+v, want key=k1 value=hello", got)
	}
	if got.LeaseID != 7 {
		t.Errorf("got LeaseID=%d, want 7", got.LeaseID)
	}
}

func TestUnixRT_GetRevision(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	got, err := tr.GetRevision(context.Background(), "k1")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if got == nil || string(got.Key) != "k1" {
		t.Errorf("got %+v", got)
	}
	if got.ModRevision != 5 {
		t.Errorf("got ModRevision=%d, want 5", got.ModRevision)
	}
}

func TestUnixRT_GetAtRevision(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	got, err := tr.GetAtRevision(context.Background(), "k1", 99)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if got == nil || got.ModRevision != 99 {
		t.Errorf("got %+v, want ModRevision=99", got)
	}
}

func TestUnixRT_Range(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	got, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if got == nil || len(got.Kvs) != 2 || got.Count != 2 {
		t.Errorf("got %+v, want 2 kvs", got)
	}
}

func TestUnixRT_Range_WithSortOptions(t *testing.T) {
	svc := &mockMVCCSvc{
		mockKVService: newMockKVService(),
		rangeFn: func(_ context.Context, _, _ []byte, opts service.RangeOptions) ([]*kv.KeyValue, int64, error) {
			if opts.Limit != 5 {
				t.Errorf("expected Limit=5, got %d", opts.Limit)
			}
			return []*kv.KeyValue{{Key: []byte("a")}}, 1, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	got, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{
		Limit:      5,
		SortOrder:  transport.SortAscend,
		SortTarget: transport.SortByKey,
	})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if got.Count != 1 {
		t.Errorf("got Count=%d, want 1", got.Count)
	}
}

func TestUnixRT_Compact(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	if err := tr.Compact(context.Background(), 100); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestUnixRT_CurrentRevision(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	rev, err := tr.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 42 {
		t.Errorf("got rev=%d, want 42", rev)
	}
}

func TestUnixRT_GetRevision_ServiceError(t *testing.T) {
	svc := &mockMVCCSvc{
		mockKVService: newMockKVService(),
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	_, err := tr.GetRevision(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestUnixRT_Compact_ServiceError(t *testing.T) {
	svc := &mockMVCCSvc{
		mockKVService: newMockKVService(),
		compactFn: func(_ context.Context, _ int64) error {
			return errors.New("compact failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithMVCC(svc))
	defer cleanup()

	if err := tr.Compact(context.Background(), 100); err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// mockTransactionSvc — service.TransactionServicer stub
// ---------------------------------------------------------------------------

type mockTransactionSvc struct {
	executeFn func(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
}

func (m *mockTransactionSvc) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, successOps, failureOps)
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  10,
		Responses: []service.OpResponse{{Succeeded: true, Count: 1}},
	}, nil
}

func (m *mockTransactionSvc) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return m.Execute(ctx, compares, successOps, failureOps)
}

func (m *mockTransactionSvc) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return &service.TransactionEntityResponse{}, nil
}

var _ service.TransactionServicer = (*mockTransactionSvc)(nil)

// ---------------------------------------------------------------------------
// Transaction round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_ExecuteTransaction(t *testing.T) {
	svc := &mockTransactionSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTransaction(svc))
	defer cleanup()

	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{{
			Key:    []byte("k"),
			Target: transport.CompareValue,
			Result: transport.CompareEqual,
			Value:  []byte("v"),
		}},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("new")}},
		FailureOps: []transport.Op{{Type: transport.OpGet, Key: []byte("k")}},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded || resp.Revision != 10 {
		t.Errorf("got %+v, want Succeeded=true Revision=10", resp)
	}
}

func TestUnixRT_ExecuteTransaction_PropagatesArgs(t *testing.T) {
	svc := &mockTransactionSvc{
		executeFn: func(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
			if len(compares) != 2 {
				t.Errorf("compares len=%d, want 2", len(compares))
			}
			if len(successOps) != 1 || len(failureOps) != 1 {
				t.Errorf("ops lens success=%d failure=%d", len(successOps), len(failureOps))
			}
			return &service.TransactionResponse{Succeeded: false, Revision: 7}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTransaction(svc))
	defer cleanup()

	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("a"), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: 1},
			{Key: []byte("b"), Target: transport.CompareModRevision, Result: transport.CompareGreater, ModRevision: 5},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("a"), Value: []byte("1")}},
		FailureOps: []transport.Op{{Type: transport.OpDelete, Key: []byte("a")}},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if resp.Succeeded || resp.Revision != 7 {
		t.Errorf("got %+v, want Succeeded=false Revision=7", resp)
	}
}

func TestUnixRT_ExecuteTransaction_ServiceError(t *testing.T) {
	svc := &mockTransactionSvc{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return nil, errors.New("tx aborted")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTransaction(svc))
	defer cleanup()

	_, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// mockBatchSvc — service.BatchServicer stub
// ---------------------------------------------------------------------------

type mockBatchSvc struct {
	executeFn func(ctx context.Context, ops []service.Op) ([]service.OpResponse, error)
}

func (m *mockBatchSvc) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, ops)
	}
	out := make([]service.OpResponse, len(ops))
	for i := range ops {
		out[i] = service.OpResponse{Succeeded: true, Count: 1}
	}
	return out, nil
}

func (m *mockBatchSvc) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return m.Execute(ctx, ops)
}

func (m *mockBatchSvc) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return &service.BatchEntityResponse{}, nil
}

func (m *mockBatchSvc) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Batch round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_ExecuteBatch(t *testing.T) {
	svc := &mockBatchSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBatch(svc))
	defer cleanup()

	resp, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: transport.OpPut, Key: []byte("k2"), Value: []byte("v2")},
		{Type: transport.OpGet, Key: []byte("k3")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if resp == nil || len(resp.Responses) != 3 {
		t.Errorf("got %+v, want 3 responses", resp)
	}
}

func TestUnixRT_ExecuteBatch_Empty(t *testing.T) {
	svc := &mockBatchSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBatch(svc))
	defer cleanup()

	resp, err := tr.ExecuteBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteBatch with empty ops: %v", err)
	}
	if resp == nil {
		t.Error("expected non-nil response")
	}
}

func TestUnixRT_ExecuteBatch_ServiceError(t *testing.T) {
	svc := &mockBatchSvc{
		executeFn: func(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
			return nil, errors.New("batch failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithBatch(svc))
	defer cleanup()

	_, err := tr.ExecuteBatch(context.Background(), []transport.Op{{Type: transport.OpPut}})
	if err == nil {
		t.Fatal("expected error from server")
	}
}
