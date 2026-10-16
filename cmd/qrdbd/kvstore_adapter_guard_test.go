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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestGuardedKVServiceAdapter_PutAllIfUnchanged_EmptyValueNotAbsent is the
// end-to-end regression for go-qrdb#555 round 3: a guard key that EXISTS with
// an empty stored value must refuse a nil ("must be absent") guard, using
// the fixed mockTransactionServicer, which — like the real engines —
// evaluates a version-0 compare on EXISTENCE, not on whether the stored
// bytes happen to be empty.
func TestGuardedKVServiceAdapter_PutAllIfUnchanged_EmptyValueNotAbsent(t *testing.T) {
	store := map[string][]byte{"claim": {}}
	tx := newMockTransactionServicer(store)
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(store), tx, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.GuardedBatchKVStore)

	applied, err := guarded.PutAllIfUnchanged(context.Background(), "claim", nil, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfUnchanged: %v", err)
	}
	if applied {
		t.Fatal("expected the batch to be refused: the guard key EXISTS, even though its value is empty")
	}
	if _, ok := store["primary"]; ok {
		t.Error("primary write must not land when the guard refuses")
	}
}

// TestGuardedKVServiceAdapter_PutAllIfUnchanged_AbsentStillApplies is the
// sibling positive case: a genuinely absent guard key must still let the
// batch through after the fix, so the version-based compare did not
// overcorrect into refusing everything.
func TestGuardedKVServiceAdapter_PutAllIfUnchanged_AbsentStillApplies(t *testing.T) {
	store := map[string][]byte{}
	tx := newMockTransactionServicer(store)
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(store), tx, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.GuardedBatchKVStore)

	applied, err := guarded.PutAllIfUnchanged(context.Background(), "claim", nil, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the batch to apply: the guard key was genuinely absent")
	}
	if string(store["primary"]) != "role-data" {
		t.Errorf("primary = %q, want %q", store["primary"], "role-data")
	}
}

// TestGuardedKVServiceAdapter_PutAllIfAllUnchanged_NonNilEmptyValueRefusesAbsentKey
// is the end-to-end regression: a guard expecting a PRESENT key with an
// empty value must refuse when the key is genuinely absent, using the fixed
// mockTransactionServicer, which — like the real engines — now evaluates the
// added existence compare rather than only the value compare.
func TestGuardedKVServiceAdapter_PutAllIfAllUnchanged_NonNilEmptyValueRefusesAbsentKey(t *testing.T) {
	store := map[string][]byte{}
	tx := newMockTransactionServicer(store)
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(store), tx, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.MultiGuardedBatchKVStore)

	applied, err := guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim", Value: []byte{}}}, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if applied {
		t.Fatal("expected the batch to be refused: the guard key is absent, not present-with-empty-value")
	}
	if _, ok := store["primary"]; ok {
		t.Error("primary write must not land when the guard refuses")
	}
}

// TestGuardedKVServiceAdapter_PutAllIfAllUnchanged_NonNilEmptyValueAppliesWhenPresent
// is the positive sibling: a present key genuinely holding an empty value
// must still satisfy the same guard.
func TestGuardedKVServiceAdapter_PutAllIfAllUnchanged_NonNilEmptyValueAppliesWhenPresent(t *testing.T) {
	store := map[string][]byte{"claim": {}}
	tx := newMockTransactionServicer(store)
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(store), tx, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.MultiGuardedBatchKVStore)

	applied, err := guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim", Value: []byte{}}}, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the batch to apply: the guard key is present and holds the expected empty value")
	}
	if string(store["primary"]) != "role-data" {
		t.Errorf("primary = %q, want %q", store["primary"], "role-data")
	}
}

// scriptedErrTxServicer answers every transaction with err. It exists for
// the error-translation tests below; the evaluating double in
// adapters_test.go never fails.
type scriptedErrTxServicer struct {
	mockTxServicer
	err error
}

func (s *scriptedErrTxServicer) Execute(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
	return nil, s.err
}

// TestGuardedKVServiceAdapter_CapabilityRefusalIsGuardedBatchUnsupported is
// pkg/server/lifecycle's identical test for the daemon's adapter: a state
// machine without a transaction subsystem must surface as
// kvstore.GuardedBatchUnsupportedError, and the daemon's startup check must
// refuse on it.
func TestGuardedKVServiceAdapter_CapabilityRefusalIsGuardedBatchUnsupported(t *testing.T) {
	refusal := &service.CapabilityUnsupportedError{Message: "statemachine: mvcc not configured"}
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(map[string][]byte{}), &scriptedErrTxServicer{err: refusal}, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.MultiGuardedBatchKVStore)

	_, err = guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim"}}, nil)
	var gbu *kvstore.GuardedBatchUnsupportedError
	if !errors.As(err, &gbu) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v (%T), want *kvstore.GuardedBatchUnsupportedError matching ErrAtomicWriteUnsupported", err, err)
	}
	if err := requireGuardedBatch(context.Background(), guarded); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("requireGuardedBatch = %v, want the unsupported refusal (fail closed at startup)", err)
	}
}

// TestGuardedKVServiceAdapter_OtherErrorsPassThroughUntranslated is the
// control: a non-capability failure is returned as-is.
func TestGuardedKVServiceAdapter_OtherErrorsPassThroughUntranslated(t *testing.T) {
	notLeader := &service.NotLeaderError{}
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(map[string][]byte{}), &scriptedErrTxServicer{err: notLeader}, nil, nil)
	if err != nil {
		t.Fatalf("newGuardedKVServiceAdapter: %v", err)
	}
	guarded := adapter.(kvstore.MultiGuardedBatchKVStore)

	_, err = guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim"}}, nil)
	if !errors.Is(err, notLeader) || errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want the NotLeaderError untranslated", err)
	}
}

// TestGuardedKVServiceAdapter_NilTransactionServiceRefuses covers a
// hand-built adapter that bypassed newGuardedKVServiceAdapter's refusal: the
// shared implementation still refuses with a typed error.
func TestGuardedKVServiceAdapter_NilTransactionServiceRefuses(t *testing.T) {
	adapter := &guardedKVServiceAdapter{kvServiceAdapter: &kvServiceAdapter{svc: newMockKVStore(map[string][]byte{})}}
	applied, err := adapter.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim"}}, nil)
	if applied || !errors.Is(err, lifecycle.ErrTransactionServiceRequired) {
		t.Fatalf("PutAllIfAllUnchanged(nil tx) = (%v, %v), want (false, lifecycle.ErrTransactionServiceRequired)", applied, err)
	}
}

// TestRequireGuardedBatch covers the daemon's start-time decision over
// kvstore.ProbeGuardedBatch's three answers, plus the NotLeader exception.
func TestRequireGuardedBatch(t *testing.T) {
	transient := errors.New("transport: connection reset")
	for _, tc := range []struct {
		name     string
		txErr    error
		wantErr  error
		wantStep string
	}{
		{name: "capable", txErr: nil},
		{name: "follower: NotLeader is unanswerable here, not a refusal", txErr: &service.NotLeaderError{}},
		{name: "unsupported state machine", txErr: &service.CapabilityUnsupportedError{Message: "mvcc not configured"},
			wantErr: kvstore.ErrAtomicWriteUnsupported, wantStep: "atomic role/user creation requires an --sm state machine with transaction support"},
		{name: "inconclusive after the leader wait", txErr: transient, wantErr: transient, wantStep: "guarded batch capability probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tx service.TransactionServicer = &mockTxServicer{}
			if tc.txErr != nil {
				tx = &scriptedErrTxServicer{err: tc.txErr}
			}
			adapter, err := newGuardedKVServiceAdapter(newMockKVStore(map[string][]byte{}), tx, nil, nil)
			if err != nil {
				t.Fatalf("newGuardedKVServiceAdapter: %v", err)
			}
			err = requireGuardedBatch(context.Background(), adapter.(kvstore.MultiGuardedBatchKVStore))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("requireGuardedBatch = %v, want nil", err)
				}
				return
			}
			var abe *AuthBootstrapError
			if !errors.As(err, &abe) || abe.Step != tc.wantStep || !errors.Is(err, tc.wantErr) {
				t.Fatalf("requireGuardedBatch = %v, want AuthBootstrapError at %q wrapping %v", err, tc.wantStep, tc.wantErr)
			}
		})
	}
}

// TestNewGuardedKVServiceAdapter_RefusesANilTransactionService: a guarded
// store with no transaction plane would claim a capability it cannot keep, so
// the constructor refuses rather than build one.
func TestNewGuardedKVServiceAdapter_RefusesANilTransactionService(t *testing.T) {
	adapter, err := newGuardedKVServiceAdapter(newMockKVStore(map[string][]byte{}), nil, nil, nil)
	if !errors.Is(err, ErrTransactionServiceRequired) {
		t.Fatalf("err = %v, want ErrTransactionServiceRequired", err)
	}
	if adapter != nil {
		t.Fatalf("adapter = %T alongside the refusal, want nil", adapter)
	}
}

// TestKVServiceAdapter_CompareAndDeleteDelegatesToTheService: the condition is
// the KV service's, so the adapter passes the key and expected value through
// unchanged and reports the service's answer and error as its own.
func TestKVServiceAdapter_CompareAndDeleteDelegatesToTheService(t *testing.T) {
	refused := errors.New("not leader")
	for _, tc := range []struct {
		name        string
		deleted     bool
		err         error
		wantDeleted bool
	}{
		{name: "matched", deleted: true, wantDeleted: true},
		{name: "value changed", deleted: false},
		{name: "service error", err: refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newMockKVStore(map[string][]byte{})
			var gotKey string
			var gotExpected []byte
			svc.compareAndDeleteFn = func(_ context.Context, key string, expected []byte) (bool, error) {
				gotKey, gotExpected = key, expected
				return tc.deleted, tc.err
			}
			adapter, err := newGuardedKVServiceAdapter(svc, &mockTxServicer{}, nil, nil)
			if err != nil {
				t.Fatalf("newGuardedKVServiceAdapter: %v", err)
			}

			deleted, err := adapter.(kvstore.AtomicKVStore).CompareAndDelete(context.Background(), "lock/a", []byte("owner-1"))
			if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("CompareAndDelete err = %v, want %v", err, tc.err)
			}
			if deleted != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v", deleted, tc.wantDeleted)
			}
			if gotKey != "lock/a" || string(gotExpected) != "owner-1" {
				t.Errorf("service saw (%q, %q), want (lock/a, owner-1)", gotKey, gotExpected)
			}
		})
	}
}
