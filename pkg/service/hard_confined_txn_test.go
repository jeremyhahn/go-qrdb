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
