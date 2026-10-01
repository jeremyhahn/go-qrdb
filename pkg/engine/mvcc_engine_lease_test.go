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

package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// applyLeaseCmd applies a single encoded command at index and returns its result,
// failing the test on an apply fault or a refusal.
func applyLeaseCmd(t *testing.T, eng *MVCCEngine, idx uint64, cmd []byte) sm.Result {
	t.Helper()
	results := make([]sm.Result, 1)
	if uerr := eng.Update(context.Background(), []sm.Entry{{Index: idx, Cmd: cmd}}, results); uerr != nil {
		t.Fatalf("Update: %v", uerr)
	}
	if msg, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("command at %d refused: %s", idx, msg)
	}
	return results[0]
}

// A revoke deletes its keys; a deleted key's secondary-index entries go with
// it, exactly as for any other delete.
func TestMVCCEngine_LeaseRevoke_RetractsIndexes(t *testing.T) {
	eng, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyLeaseCmd(t, eng, 1, mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false)))
	id := int64(applyLeaseCmd(t, eng, 2, mustEncode(protocol.EncodeLeaseGrant(60))).Value)
	applyLeaseCmd(t, eng, 3, mustEncode(protocol.EncodeMVCCPut([]byte("user:1"), []byte(`{"name":"alice"}`), id)))
	applyLeaseCmd(t, eng, 4, mustEncode(protocol.EncodeLeaseRevoke(id)))

	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name", Value: []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup (index query): %v", err)
	}
	if ids := got.([][]byte); len(ids) != 0 {
		t.Fatalf("index still returns %d id(s) for a key its lease's revoke deleted", len(ids))
	}
}

// A new engine over storage that already holds a lease table -- what a
// restart or snapshot recovery builds -- restores the lessor from it.
func TestNewMVCCEngine_RestoresLessorFromStorage(t *testing.T) {
	first, stg := newMVCCEngine(t, WithMVCCLessor(lease.NewLessor()))
	defer func() { _ = stg.Close() }()

	a := int64(applyLeaseCmd(t, first, 1, mustEncode(protocol.EncodeLeaseGrant(60))).Value)
	b := int64(applyLeaseCmd(t, first, 2, mustEncode(protocol.EncodeLeaseGrant(60))).Value)
	applyLeaseCmd(t, first, 3, mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("old"), a)))
	applyLeaseCmd(t, first, 4, mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("new"), b)))
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lr := lease.NewLessor()
	second, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine over a populated lease table: %v", err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	if !lr.Has(a) || !lr.Has(b) {
		t.Fatalf("restored lessor holds a=%v b=%v, want both", lr.Has(a), lr.Has(b))
	}
	if got := lr.LeaseOf([]byte("k")); got != b {
		t.Fatalf("k restored bound to %d, want %d", got, b)
	}
}

// A corrupt lease table refuses the engine rather than guessing which keys a
// lease owns.
func TestNewMVCCEngine_CorruptLeaseTableIsRefused(t *testing.T) {
	_, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	if err := stg.Set([]byte(lease.TablePrefix+"seq"), []byte{1}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err := NewMVCCEngine(stg, WithMVCCLessor(lease.NewLessor()))
	var cte *lease.CorruptTableError
	if !errors.As(err, &cte) {
		t.Fatalf("NewMVCCEngine over a corrupt lease table error = %v, want *lease.CorruptTableError", err)
	}
}

// A put naming a lease the engine has no lessor for is refused as an absent
// capability and writes nothing.
func TestMVCCEngine_PutNamingLeaseWithoutLessorIsRefused(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	cmd, err := protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 7)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, kind, ok := protocol.DecodeAppErrorKind(results[0].Data); !ok || kind != protocol.AppErrorCapability {
		t.Fatalf("result kind = %v (refused=%v), want a capability refusal", kind, ok)
	}
	var notFound *mvcc.KeyNotFoundError
	if v, gerr := eng.Store().Get([]byte("k")); !errors.As(gerr, &notFound) {
		t.Fatalf("after a refused put, Get = %+v, %v; want *mvcc.KeyNotFoundError", v, gerr)
	}
}
