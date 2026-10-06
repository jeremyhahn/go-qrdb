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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// fakeTxn is a minimal TransactionServicer double recording whether
// Execute was reached, so a refusal can be proven to have
// short-circuited BEFORE the transaction was ever proposed.
type fakeTxn struct {
	TransactionServicer
	called bool
}

func (f *fakeTxn) Execute(_ context.Context, _ []Compare, _, _ []Op) (*TransactionResponse, error) {
	f.called = true
	return &TransactionResponse{Succeeded: true}, nil
}

func (f *fakeTxn) ExecuteWithMode(_ context.Context, _ []Compare, _, _ []Op, _ writemode.Mode) (*TransactionResponse, error) {
	f.called = true
	return &TransactionResponse{Succeeded: true}, nil
}

func (f *fakeTxn) ExecuteEntity(_ context.Context, _ *TransactionEntityRequest) (*TransactionEntityResponse, error) {
	f.called = true
	return &TransactionEntityResponse{Succeeded: true}, nil
}

// fakeBatch is fakeTxn's BatchServicer counterpart.
type fakeBatch struct {
	BatchServicer
	called bool
}

func (f *fakeBatch) Execute(_ context.Context, _ []Op) ([]OpResponse, error) {
	f.called = true
	return nil, nil
}

func (f *fakeBatch) ExecuteWithMode(_ context.Context, _ []Op, _ writemode.Mode) ([]OpResponse, error) {
	f.called = true
	return nil, nil
}

func (f *fakeBatch) ExecuteEntity(_ context.Context, _ *BatchEntityRequest) (*BatchEntityResponse, error) {
	f.called = true
	return &BatchEntityResponse{}, nil
}

func (f *fakeBatch) ImportCommit(_ context.Context, _ string) (*ImportResult, error) {
	f.called = true
	return &ImportResult{}, nil
}

func assertHardConfinedError(t *testing.T, err error) {
	t.Helper()
	var confErr *HardConfinedKeyError
	if !errors.As(err, &confErr) {
		t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
	}
}

func TestNewHardConfinedTransaction_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedTransaction(nil); got != nil {
		t.Fatalf("NewHardConfinedTransaction(nil) = %v, want nil", got)
	}
}

func TestNewHardConfinedBatch_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedBatch(nil); got != nil {
		t.Fatalf("NewHardConfinedBatch(nil) = %v, want nil", got)
	}
}

func TestHardConfinedTransaction_RefusesACompareAgainstTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	compares := []Compare{{Key: []byte(confinedKey), Result: CompareEqual}}
	if _, err := txn.Execute(ctx, compares, nil, nil); err == nil {
		t.Fatal("Execute with a compare against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("Execute reached the underlying servicer despite the refusal -- a caller could binary-search the compare result")
	}
}

func TestHardConfinedTransaction_RefusesAnOpAgainstTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	successOps := []Op{{Type: OpPut, Key: []byte(confinedKey), Value: []byte("x")}}
	if _, err := txn.Execute(ctx, nil, successOps, nil); err == nil {
		t.Fatal("Execute with a success op against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("Execute reached the underlying servicer despite the refusal")
	}

	failureOps := []Op{{Type: OpGet, Key: []byte(confinedKey)}}
	if _, err := txn.Execute(ctx, nil, nil, failureOps); err == nil {
		t.Fatal("Execute with a failure op against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("Execute reached the underlying servicer despite the refusal")
	}

	if _, err := txn.ExecuteWithMode(ctx, nil, successOps, nil, writemode.Sync); err == nil {
		t.Fatal("ExecuteWithMode with a success op against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("ExecuteWithMode reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedTransaction_RefusesARangeOpOverlappingTheConfinedNamespace(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	ops := []Op{{Type: OpRange, Key: []byte(InternalKeyPrefixSystem + "cluster_b"), RangeEnd: []byte(InternalKeyPrefixSystem + "cluster_d")}}
	if _, err := txn.Execute(ctx, nil, ops, nil); err == nil {
		t.Fatal("Execute with a range op spanning the confined namespace succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("Execute reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedTransaction_OrdinaryTransactionsPassThrough(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	compares := []Compare{{Key: []byte("ordinary/key"), Result: CompareEqual}}
	ops := []Op{{Type: OpPut, Key: []byte("ordinary/key"), Value: []byte("x")}}
	if _, err := txn.Execute(ctx, compares, ops, nil); err != nil {
		t.Fatalf("Execute with an ordinary compare and op: %v", err)
	}
	if !fake.called {
		t.Fatal("Execute did not reach the underlying servicer for an ordinary transaction")
	}
}

func TestHardConfinedBatch_RefusesAnOpAgainstTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := &fakeBatch{}
	batch := NewHardConfinedBatch(fake)

	ops := []Op{{Type: OpGet, Key: []byte(confinedKey)}}
	if _, err := batch.Execute(ctx, ops); err == nil {
		t.Fatal("Execute with an op against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("Execute reached the underlying servicer despite the refusal")
	}

	if _, err := batch.ExecuteWithMode(ctx, ops, writemode.Sync); err == nil {
		t.Fatal("ExecuteWithMode with an op against the confined key succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("ExecuteWithMode reached the underlying servicer despite the refusal")
	}
}

// TestHardConfinedTransaction_ExecuteEntityRefusesTheConfinedEntity is
// go-qrdb#555's B2b regression proof for ExecuteEntity: it proposes
// directly against the host and never calls Execute, so it needs its own
// confinement check rather than relying on Execute's.
func TestHardConfinedTransaction_ExecuteEntityRefusesTheConfinedEntity(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	req := &TransactionEntityRequest{
		Compares: []EntityCompare{{EntityType: confinedEntityType, ID: confinedEntityID, Result: CompareEqual}},
	}
	if _, err := txn.ExecuteEntity(ctx, req); err == nil {
		t.Fatal("ExecuteEntity with a compare against the confined entity succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("ExecuteEntity reached the underlying servicer despite the refusal")
	}

	req = &TransactionEntityRequest{
		Success: []TransactionEntityOp{{Type: "put", EntityType: confinedEntityType, ID: confinedEntityID, Data: []byte("x")}},
	}
	if _, err := txn.ExecuteEntity(ctx, req); err == nil {
		t.Fatal("ExecuteEntity with a success op against the confined entity succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("ExecuteEntity reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedTransaction_ExecuteEntityOrdinaryPassesThrough(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)

	req := &TransactionEntityRequest{
		Success: []TransactionEntityOp{{Type: "put", EntityType: "items", ID: "1", Data: []byte("x")}},
	}
	if _, err := txn.ExecuteEntity(ctx, req); err != nil {
		t.Fatalf("ExecuteEntity with an ordinary op: %v", err)
	}
	if !fake.called {
		t.Fatal("ExecuteEntity did not reach the underlying servicer for an ordinary request")
	}
}

// TestHardConfinedBatch_ExecuteEntityRefusesTheConfinedEntity is the
// BatchServicer counterpart.
func TestHardConfinedBatch_ExecuteEntityRefusesTheConfinedEntity(t *testing.T) {
	ctx := context.Background()
	fake := &fakeBatch{}
	batch := NewHardConfinedBatch(fake)

	req := &BatchEntityRequest{
		Ops: []BatchEntityOp{{Type: "put", EntityType: confinedEntityType, ID: confinedEntityID, Data: []byte("x")}},
	}
	if _, err := batch.ExecuteEntity(ctx, req); err == nil {
		t.Fatal("ExecuteEntity with an op against the confined entity succeeded, want a refusal")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("ExecuteEntity reached the underlying servicer despite the refusal")
	}
}

// importCommitViaSession drives a chunked import session (ImportBegin, one
// ImportAppend carrying the whole payload, ImportCommit) against batch, so
// these tests exercise the row-key guard through the SAME path a real
// caller reaches it by -- ImportCommit, not the retired single-message
// Import -- without each test managing session plumbing by hand.
func importCommitViaSession(t *testing.T, batch BatchServicer, data string, opts ImportOptions) (*ImportResult, error) {
	t.Helper()
	ctx := context.Background()
	sessionID, err := batch.ImportBegin(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := batch.ImportAppend(ctx, sessionID, 0, []byte(data)); err != nil {
		_ = batch.ImportAbort(ctx, sessionID)
		return nil, err
	}
	return batch.ImportCommit(ctx, sessionID)
}

// TestHardConfinedBatch_ImportRefusesARowNamingTheConfinedKey is go-qrdb#555's
// B2b regression proof for ImportCommit: it parses a caller-supplied file
// and proposes the resulting ops directly, never calling Execute, so a row
// naming the confined key had no interception point before
// NewHardConfinedBatch started installing a row-key guard on the wrapped
// *BatchService. This uses the REAL BatchService, not a fake: the guard is
// wired by type-asserting the wrapped value, which a fake does not satisfy.
func TestHardConfinedBatch_ImportRefusesARowNamingTheConfinedKey(t *testing.T) {
	host, store := newImportSessionHostWithStore()
	raw := NewBatchService(host, 1)
	batch := NewHardConfinedBatch(raw)

	csvData := "key,value\n" + confinedKey + ",overwrite\n"
	result, err := importCommitViaSession(t, batch, csvData, ImportOptions{
		Format:     "csv",
		KeyField:   "key",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if result.Total != 1 || result.Failed != 1 || result.Succeeded != 0 {
		t.Fatalf("ImportCommit result = %+v, want the confined row refused as failed, not succeeded", result)
	}
	if store.has(confinedKey) {
		t.Fatal("ImportCommit wrote the confined key despite the row-key guard")
	}
}

// TestHardConfinedBatch_ImportTransactionalAbortsOnTheConfinedKey proves a
// transactional import aborts the whole import (ImportTransactionAbortedError)
// rather than recording a per-row failure, matching every other per-row
// validation ImportCommit already enforces (key/value size limits).
func TestHardConfinedBatch_ImportTransactionalAbortsOnTheConfinedKey(t *testing.T) {
	host := newImportSessionHost()
	raw := NewBatchService(host, 1)
	batch := NewHardConfinedBatch(raw)

	csvData := "key,value\n" + confinedKey + ",overwrite\n"
	_, err := importCommitViaSession(t, batch, csvData, ImportOptions{
		Format:        "csv",
		KeyField:      "key",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})
	var aborted *ImportTransactionAbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("transactional ImportCommit of a confined row = %v, want *ImportTransactionAbortedError", err)
	}
}

// TestHardConfinedBatch_ImportOrdinaryRowsPassThrough proves an ordinary
// import is unaffected by the guard.
func TestHardConfinedBatch_ImportOrdinaryRowsPassThrough(t *testing.T) {
	host, store := newImportSessionHostWithStore()
	raw := NewBatchService(host, 1)
	batch := NewHardConfinedBatch(raw)

	csvData := "key,value\nordinary-1,v1\n"
	result, err := importCommitViaSession(t, batch, csvData, ImportOptions{
		Format:     "csv",
		KeyField:   "key",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 0 {
		t.Fatalf("ImportCommit result = %+v, want the ordinary row to succeed", result)
	}
	if !store.has("ordinary-1") {
		t.Fatal("ImportCommit did not write the ordinary row")
	}
}

// fakeBatchWithOwnGuard is a BatchServicer double that implements
// ImportKeyGuardSetter itself, standing in for a future alternate
// BatchServicer implementation (not *BatchService) that wants its import
// commit confined. It records the installed guard and applies it to every
// imported row, proving NewHardConfinedBatch's installation path works
// through the INTERFACE, not a concrete-type assertion.
type fakeBatchWithOwnGuard struct {
	BatchServicer
	guard func(key string) error
}

func (f *fakeBatchWithOwnGuard) SetImportKeyGuard(guard func(key string) error) {
	f.guard = guard
}

func (f *fakeBatchWithOwnGuard) ImportCommit(_ context.Context, _ string) (*ImportResult, error) {
	if f.guard == nil {
		return nil, errors.New("no guard installed")
	}
	if err := f.guard(confinedKey); err == nil {
		return nil, errors.New("guard did not refuse the confined key")
	}
	return &ImportResult{Total: 1, Succeeded: 1}, nil
}

// TestHardConfinedBatch_InstallsTheGuardThroughTheInterface proves
// NewHardConfinedBatch installs the hard-confinement guard on any
// BatchServicer satisfying ImportKeyGuardSetter, not only a concrete
// *BatchService -- the interface, not a type assertion to one production
// struct, is what a future alternate implementation opts into.
func TestHardConfinedBatch_InstallsTheGuardThroughTheInterface(t *testing.T) {
	fake := &fakeBatchWithOwnGuard{}
	batch := NewHardConfinedBatch(fake)

	if fake.guard == nil {
		t.Fatal("NewHardConfinedBatch did not install a guard on a BatchServicer satisfying ImportKeyGuardSetter")
	}
	if _, err := batch.ImportCommit(context.Background(), "arbitrary-session"); err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
}

// TestHardConfinedBatch_ImportRefusesWhenTheGuardCouldNotBeInstalled is the
// non-blocking review item: when the wrapped BatchServicer is not a
// *BatchService, NewHardConfinedBatch cannot install the row-key guard
// ImportCommit relies on, and ImportCommit must refuse outright rather than
// commit unguarded -- a fake (or a future alternate BatchServicer
// implementation) gets a typed refusal, not silent, unconfined passthrough.
// The refusal happens before sessionID is ever looked up, so this reaches
// ImportCommit directly rather than driving a whole session through
// ImportBegin/ImportAppend first.
func TestHardConfinedBatch_ImportRefusesWhenTheGuardCouldNotBeInstalled(t *testing.T) {
	ctx := context.Background()
	fake := &fakeBatch{}
	batch := NewHardConfinedBatch(fake)

	_, err := batch.ImportCommit(ctx, "arbitrary-session")
	var guardErr *HardConfinedImportGuardUnavailableError
	if !errors.As(err, &guardErr) {
		t.Fatalf("ImportCommit on a non-*BatchService wrapped value = %v, want *HardConfinedImportGuardUnavailableError", err)
	}
	if fake.called {
		t.Fatal("ImportCommit reached the underlying servicer despite the refusal")
	}
}

func TestHardConfinedBatch_OrdinaryBatchesPassThrough(t *testing.T) {
	ctx := context.Background()
	fake := &fakeBatch{}
	batch := NewHardConfinedBatch(fake)

	ops := []Op{{Type: OpPut, Key: []byte("ordinary/key"), Value: []byte("x")}}
	if _, err := batch.Execute(ctx, ops); err != nil {
		t.Fatalf("Execute with an ordinary op: %v", err)
	}
	if !fake.called {
		t.Fatal("Execute did not reach the underlying servicer for an ordinary batch")
	}
}
