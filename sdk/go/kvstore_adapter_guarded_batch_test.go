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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mockTransactionTransportKV embeds mockTransportKV and additionally
// implements transport.TransactionClient, modeling every production
// transport (REST, gRPC, QUIC, Unix, MCP, embedded all carry
// ExecuteTransaction today).
type mockTransactionTransportKV struct {
	*mockTransportKV
	executeFn func(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error)
}

func (m *mockTransactionTransportKV) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	return m.executeFn(ctx, req, opts...)
}

var _ transport.TransactionClient = (*mockTransactionTransportKV)(nil)

// TestKVStoreAdapter_MultiGuardedBatchKVStore_Forwards proves
// kvStoreAdapter.PutAllIfAllUnchanged translates guards and writes into the
// transport's ExecuteTransaction request and reports its Succeeded result
// (go-qrdb#589): a nil-Value guard becomes a version-equals-zero compare, a
// present guard becomes an exists compare plus a value compare, and a
// Delete write becomes an OpDelete.
func TestKVStoreAdapter_MultiGuardedBatchKVStore_Forwards(t *testing.T) {
	var captured *transport.TransactionRequest
	txKV := &mockTransactionTransportKV{
		mockTransportKV: newEmptyMockTransportKV(),
		executeFn: func(_ context.Context, req *transport.TransactionRequest, _ ...transport.WriteOption) (*transport.TransactionResponse, error) {
			captured = req
			return &transport.TransactionResponse{Succeeded: true}, nil
		},
	}

	adapter, err := NewKVStoreAdapter(txKV, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batchStore, ok := adapter.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		t.Fatal("kvStoreAdapter does not implement kvstore.MultiGuardedBatchKVStore")
	}

	applied, err := batchStore.PutAllIfAllUnchanged(context.Background(),
		[]kvstore.Guard{
			{Key: "absent-key", Value: nil},
			{Key: "present-key", Value: []byte("expected")},
		},
		[]kvstore.KV{
			{Key: "old-key", Delete: true},
			{Key: "new-key", Value: []byte("new-value")},
		},
	)
	if err != nil || !applied {
		t.Fatalf("PutAllIfAllUnchanged = (%v, %v), want (true, nil)", applied, err)
	}
	if captured == nil {
		t.Fatal("ExecuteTransaction was never called")
	}

	if len(captured.Compares) != 3 {
		t.Fatalf("Compares = %+v, want 3 (one for the absent guard, two for the present guard)", captured.Compares)
	}
	absent := captured.Compares[0]
	if string(absent.Key) != "absent-key" || absent.Target != transport.CompareVersion ||
		absent.Result != transport.CompareEqual || absent.Version != 0 {
		t.Errorf("absent guard compare = %+v, want version==0 on absent-key", absent)
	}
	exists, value := captured.Compares[1], captured.Compares[2]
	if string(exists.Key) != "present-key" || exists.Target != transport.CompareVersion ||
		exists.Result != transport.CompareGreater || exists.Version != 0 {
		t.Errorf("present guard exists-compare = %+v, want version>0 on present-key", exists)
	}
	if string(value.Key) != "present-key" || value.Target != transport.CompareValue ||
		value.Result != transport.CompareEqual || string(value.Value) != "expected" {
		t.Errorf("present guard value-compare = %+v, want value==%q on present-key", value, "expected")
	}

	if len(captured.SuccessOps) != 2 {
		t.Fatalf("SuccessOps = %+v, want 2", captured.SuccessOps)
	}
	del, put := captured.SuccessOps[0], captured.SuccessOps[1]
	if del.Type != transport.OpDelete || string(del.Key) != "old-key" {
		t.Errorf("first op = %+v, want OpDelete on old-key", del)
	}
	if put.Type != transport.OpPut || string(put.Key) != "new-key" || string(put.Value) != "new-value" {
		t.Errorf("second op = %+v, want OpPut new-key=new-value", put)
	}
}

// TestKVStoreAdapter_MultiGuardedBatchKVStore_ReportsGuardFailure proves a
// transaction whose compares did not all hold reports applied=false with a
// nil error -- a RESULT, not a refusal -- exactly as
// kvstore.MultiGuardedBatchKVStore documents.
func TestKVStoreAdapter_MultiGuardedBatchKVStore_ReportsGuardFailure(t *testing.T) {
	txKV := &mockTransactionTransportKV{
		mockTransportKV: newEmptyMockTransportKV(),
		executeFn: func(context.Context, *transport.TransactionRequest, ...transport.WriteOption) (*transport.TransactionResponse, error) {
			return &transport.TransactionResponse{Succeeded: false}, nil
		},
	}
	adapter, err := NewKVStoreAdapter(txKV, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batchStore := adapter.(kvstore.MultiGuardedBatchKVStore)

	applied, err := batchStore.PutAllIfAllUnchanged(context.Background(),
		[]kvstore.Guard{{Key: "k", Value: nil}},
		[]kvstore.KV{{Key: "k", Value: []byte("v")}})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged error = %v, want nil", err)
	}
	if applied {
		t.Fatal("PutAllIfAllUnchanged reported applied=true for a failed guard")
	}
}

// TestKVStoreAdapter_MultiGuardedBatchKVStore_RefusesEmptyKey proves the
// adapter catches an empty guard or write key BEFORE issuing the
// ExecuteTransaction RPC at all (go-qrdb#589's non-blocking ask): the
// refusal is a local *kvstore.EmptyKeyError, and the transport's
// executeFn is never called, over a transport that WOULD otherwise accept
// the request.
func TestKVStoreAdapter_MultiGuardedBatchKVStore_RefusesEmptyKey(t *testing.T) {
	reached := false
	txKV := &mockTransactionTransportKV{
		mockTransportKV: newEmptyMockTransportKV(),
		executeFn: func(context.Context, *transport.TransactionRequest, ...transport.WriteOption) (*transport.TransactionResponse, error) {
			reached = true
			return &transport.TransactionResponse{Succeeded: true}, nil
		},
	}
	adapter, err := NewKVStoreAdapter(txKV, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batchStore := adapter.(kvstore.MultiGuardedBatchKVStore)

	_, err = batchStore.PutAllIfAllUnchanged(context.Background(),
		[]kvstore.Guard{{Key: "", Value: nil}},
		[]kvstore.KV{{Key: "k", Value: []byte("v")}})
	var emptyKey *kvstore.EmptyKeyError
	if !errors.As(err, &emptyKey) {
		t.Fatalf("empty guard key: err = %v (%T), want *kvstore.EmptyKeyError", err, err)
	}
	if emptyKey.Field != "guard" || emptyKey.Index != 0 {
		t.Fatalf("empty guard key: err = %+v, want Field=guard Index=0", emptyKey)
	}
	if reached {
		t.Fatal("an empty guard key reached ExecuteTransaction")
	}

	_, err = batchStore.PutAllIfAllUnchanged(context.Background(),
		nil,
		[]kvstore.KV{{Key: "k", Value: []byte("v")}, {Key: ""}})
	if !errors.As(err, &emptyKey) {
		t.Fatalf("empty write key: err = %v (%T), want *kvstore.EmptyKeyError", err, err)
	}
	if emptyKey.Field != "write" || emptyKey.Index != 1 {
		t.Fatalf("empty write key: err = %+v, want Field=write Index=1", emptyKey)
	}
	if reached {
		t.Fatal("an empty write key reached ExecuteTransaction")
	}
}

// TestKVStoreAdapter_MultiGuardedBatchKVStore_RefusesWhenTransportLacksTheCapability
// proves the adapter still satisfies kvstore.MultiGuardedBatchKVStore when
// the underlying transport.KVClient is NOT a transport.TransactionClient,
// reporting kvstore.ErrAtomicWriteUnsupported from the call rather than
// failing the type assertion -- the same "has the method but not the
// capability" contract PutIfAbsent already honours.
func TestKVStoreAdapter_MultiGuardedBatchKVStore_RefusesWhenTransportLacksTheCapability(t *testing.T) {
	plain := newEmptyMockTransportKV()
	adapter, err := NewKVStoreAdapter(plain, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batchStore, ok := adapter.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		t.Fatal("kvStoreAdapter does not implement kvstore.MultiGuardedBatchKVStore")
	}

	if _, err := batchStore.PutAllIfAllUnchanged(context.Background(), nil, nil); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("PutAllIfAllUnchanged error = %v, want ErrAtomicWriteUnsupported", err)
	}
	if _, err := batchStore.PutAllIfUnchanged(context.Background(), "k", nil, nil); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("PutAllIfUnchanged error = %v, want ErrAtomicWriteUnsupported", err)
	}
}

// TestKVStoreAdapter_PutAllIfUnchanged_IsASingleGuardBatch proves
// PutAllIfUnchanged is PutAllIfAllUnchanged with exactly one guard.
func TestKVStoreAdapter_PutAllIfUnchanged_IsASingleGuardBatch(t *testing.T) {
	var captured *transport.TransactionRequest
	txKV := &mockTransactionTransportKV{
		mockTransportKV: newEmptyMockTransportKV(),
		executeFn: func(_ context.Context, req *transport.TransactionRequest, _ ...transport.WriteOption) (*transport.TransactionResponse, error) {
			captured = req
			return &transport.TransactionResponse{Succeeded: true}, nil
		},
	}
	adapter, err := NewKVStoreAdapter(txKV, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batchStore := adapter.(kvstore.MultiGuardedBatchKVStore)

	applied, err := batchStore.PutAllIfUnchanged(context.Background(), "guard-key", []byte("v1"),
		[]kvstore.KV{{Key: "k2", Value: []byte("v2")}})
	if err != nil || !applied {
		t.Fatalf("PutAllIfUnchanged = (%v, %v), want (true, nil)", applied, err)
	}
	if len(captured.Compares) != 2 || string(captured.Compares[0].Key) != "guard-key" {
		t.Fatalf("Compares = %+v, want the single guard-key's two compares", captured.Compares)
	}
}
