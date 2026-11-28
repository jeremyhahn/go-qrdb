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

package sqlitevfs

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	pkgvfs "github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var errStub = errors.New("stub transport failure")

// stubMVCC fails Range with err; every other method is unimplemented.
type stubMVCC struct {
	transport.MVCCClient
	err error
}

func (s stubMVCC) Range(context.Context, []byte, []byte, transport.RangeOptions, ...transport.ReadOption) (*transport.RangeResponse, error) {
	return nil, s.err
}

// stubTxn answers every transaction with resp.
type stubTxn struct {
	transport.TransactionClient
	resp *transport.TransactionResponse
}

func (s stubTxn) ExecuteTransaction(context.Context, *transport.TransactionRequest, ...transport.WriteOption) (*transport.TransactionResponse, error) {
	return s.resp, nil
}

func TestReadError_MapsNotFound(t *testing.T) {
	if err := readError(nil, "k", 1); err != nil {
		t.Fatalf("readError(nil) = %v", err)
	}
	if err := readError(&transport.KeyNotFoundError{Key: "k"}, "k", 1); !isA[*service.KeyNotFoundError](err) {
		t.Fatalf("readError(key not found) = %v, want KeyNotFoundError", err)
	}
	if err := readError(&transport.RevisionNotFoundError{Revision: 7}, "k", 7); !isA[*service.RevisionNotFoundError](err) {
		t.Fatalf("readError(revision not found) = %v, want RevisionNotFoundError", err)
	}
	if err := readError(errStub, "k", 1); !errors.Is(err, errStub) {
		t.Fatalf("readError(other) = %v, want it unchanged", err)
	}
}

func TestToLock_Nil(t *testing.T) {
	if got := toLock(nil); got != nil {
		t.Fatalf("toLock(nil) = %+v, want nil", got)
	}
	got := toLock(&transport.LockInfo{Key: "k", FencingToken: 3, HolderID: "h"})
	if got == nil || got.Key != "k" || got.FencingToken != 3 || got.HolderID != "h" {
		t.Fatalf("toLock = %+v", got)
	}
}

func TestKVAdapter_RangeFailure(t *testing.T) {
	a := kvAdapter{c: stubMVCC{err: errStub}}
	if _, _, _, err := a.Range(t.Context(), []byte("a"), []byte("b")); !errors.Is(err, errStub) {
		t.Fatalf("Range = %v, want the transport failure", err)
	}
}

func TestAdapters_RefuseUnknownOpType(t *testing.T) {
	bad := []service.Op{{Type: lock.OpType(250), Key: []byte("k")}}
	if _, err := toWireOps(bad); err == nil {
		t.Fatal("toWireOps accepted an op of no wire form")
	}
	if _, err := (kvAdapter{}).MovePrefix(t.Context(), &service.MovePrefixRequest{Src: "a/", Dst: "b/", Ops: bad}); err == nil {
		t.Fatal("MovePrefix accepted an op of no wire form")
	}
	if _, err := (txnAdapter{}).Execute(t.Context(), nil, bad, nil); err == nil {
		t.Fatal("Execute accepted a success op of no wire form")
	}
	if _, err := (txnAdapter{}).Execute(t.Context(), nil, nil, bad); err == nil {
		t.Fatal("Execute accepted a failure op of no wire form")
	}
}

func TestTxnAdapter_CarriesResponses(t *testing.T) {
	a := txnAdapter{c: stubTxn{resp: &transport.TransactionResponse{
		Succeeded: true,
		Revision:  9,
		Responses: []transport.OpResponse{{Count: 1, Succeeded: true, Kvs: []*transport.MVCCKeyValue{{Key: []byte("k"), Value: []byte("v")}}}},
	}}}
	ops := []service.Op{{Type: lock.OpGet, Key: []byte("k")}}
	resp, err := a.Execute(t.Context(), nil, ops, ops)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Succeeded || resp.Revision != 9 || len(resp.Responses) != 1 || len(resp.Responses[0].Kvs) != 1 || string(resp.Responses[0].Kvs[0].Value) != "v" {
		t.Fatalf("Execute = %+v", resp)
	}
}

func TestAPI_Wrappers(t *testing.T) {
	if err := ValidateDatabaseName("app.db"); err != nil {
		t.Fatalf("ValidateDatabaseName(app.db): %v", err)
	}
	if err := ValidateDatabaseName(""); err == nil {
		t.Fatal("ValidateDatabaseName accepted an empty name")
	}
	if _, err := NewStore(nil, "app.db"); !errors.Is(err, pkgvfs.ErrNilBackend) {
		t.Fatalf("NewStore(nil) = %v, want ErrNilBackend", err)
	}
	b, err := NewBackend(stubMVCC{err: errStub}, stubTxn{})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if _, err := NewStore(b, "app.db", WithOptimistic(), WithStageThreshold(8192)); err != nil {
		t.Fatalf("NewStore(optimistic): %v", err)
	}
	if _, err := NewStore(b, "app.db", WithWriterLock(nil)); err == nil {
		t.Fatal("NewStore accepted a nil writer lock")
	}
	if err := Register(nil); err == nil {
		t.Fatal("Register(nil) succeeded")
	}
}

func isA[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}
