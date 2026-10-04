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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNewGuardedKVStoreAdapter_NilTxFailsClosed covers the construction-time
// refusal: a nil transaction servicer must not silently yield a store that
// advertises kvstore.GuardedBatchKVStore and cannot honour it.
func TestNewGuardedKVStoreAdapter_NilTxFailsClosed(t *testing.T) {
	_, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, nil)
	if !errors.Is(err, ErrTransactionServiceRequired) {
		t.Fatalf("err = %v, want ErrTransactionServiceRequired", err)
	}
}

// TestNewGuardedKVStoreAdapter_Success confirms the returned store satisfies
// kvstore.GuardedBatchKVStore when a transaction servicer is supplied.
func TestNewGuardedKVStoreAdapter_Success(t *testing.T) {
	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, newEvaluatingTxService())
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	if _, ok := store.(kvstore.GuardedBatchKVStore); !ok {
		t.Fatal("expected the returned store to implement kvstore.GuardedBatchKVStore")
	}
}

// TestGuardedKVStoreAdapter_PutAllIfUnchanged exercises PutAllIfUnchanged
// against the REAL mvcc.Evaluate-backed evaluatingTxService (see
// kv_adapter_guard_test.go), not a canned mock, so a regression in the
// guard's own compare semantics — go-qrdb#56's empty-vs-absent hole, for
// instance — would be caught here exactly as it is for the seal backend's
// guarded verbs.
func TestGuardedKVStoreAdapter_PutAllIfUnchanged(t *testing.T) {
	ctx := context.Background()

	t.Run("applies when the guard key is absent", func(t *testing.T) {
		tx := newEvaluatingTxService()
		store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
		}
		guarded := store.(kvstore.GuardedBatchKVStore)

		applied, err := guarded.PutAllIfUnchanged(ctx, "claim", nil, []kvstore.KV{
			{Key: "primary", Value: []byte("role-data")},
			{Key: "index", Value: []byte("role-id")},
		})
		if err != nil {
			t.Fatalf("PutAllIfUnchanged: %v", err)
		}
		if !applied {
			t.Fatal("expected the batch to apply: guard key was absent")
		}
		if v, ok := tx.data["primary"]; !ok || string(v.Value) != "role-data" {
			t.Errorf("primary write did not land: %+v", tx.data["primary"])
		}
		if v, ok := tx.data["index"]; !ok || string(v.Value) != "role-id" {
			t.Errorf("index write did not land: %+v", tx.data["index"])
		}
	})

	t.Run("refuses when the guard key exists but holds an empty value", func(t *testing.T) {
		// The exact go-qrdb#56 hole this file's own doc comment claims to
		// cover, but round 3 of go-qrdb#555's review found PutAllIfUnchanged
		// itself was never tested against it: bytes.Equal(nil, []byte{}) is
		// true in Go, so a value compare against nil cannot distinguish
		// "never written" from "written once with an empty value". A
		// present-but-empty claim, username, or name-index key — reachable
		// from malformed or pre-migration data — must NOT satisfy a nil
		// ("must be absent") guard.
		tx := newEvaluatingTxService()
		tx.put("claim", []byte{})
		store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
		}
		guarded := store.(kvstore.GuardedBatchKVStore)

		applied, err := guarded.PutAllIfUnchanged(ctx, "claim", nil, []kvstore.KV{
			{Key: "primary", Value: []byte("role-data")},
		})
		if err != nil {
			t.Fatalf("PutAllIfUnchanged: %v", err)
		}
		if applied {
			t.Fatal("expected the batch to be refused: the guard key EXISTS, even though its value is empty")
		}
		if _, ok := tx.data["primary"]; ok {
			t.Error("primary write must not land when the guard refuses")
		}
	})

	t.Run("refuses when the guard key already holds a different value", func(t *testing.T) {
		tx := newEvaluatingTxService()
		tx.put("claim", []byte("already-held"))
		store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
		}
		guarded := store.(kvstore.GuardedBatchKVStore)

		applied, err := guarded.PutAllIfUnchanged(ctx, "claim", nil, []kvstore.KV{
			{Key: "primary", Value: []byte("role-data")},
		})
		if err != nil {
			t.Fatalf("PutAllIfUnchanged: %v", err)
		}
		if applied {
			t.Fatal("expected the batch to be refused: guard key was already held")
		}
		if _, ok := tx.data["primary"]; ok {
			t.Error("primary write must not land when the guard refuses")
		}
	})

	t.Run("propagates a transaction error", func(t *testing.T) {
		injected := errors.New("injected transaction failure")
		store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, &mockTransactionServicer{
			executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
				return nil, injected
			},
		})
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
		}
		guarded := store.(kvstore.GuardedBatchKVStore)

		_, err = guarded.PutAllIfUnchanged(ctx, "claim", nil, []kvstore.KV{{Key: "primary", Value: []byte("x")}})
		if !errors.Is(err, injected) {
			t.Fatalf("err = %v, want the injected error propagated", err)
		}
	})
}

// TestGuardCompares_NonNilValueAlsoRequiresExistence pins go-qrdb#555's
// round-4 review finding, the symmetric hole to the one
// TestGuardedKVStoreAdapter_PutAllIfUnchanged's "refuses when the guard key
// exists but holds an empty value" subtest closes for the single-guard path:
// a non-nil Guard.Value ("must be present and hold exactly this value") must
// compile to BOTH an existence compare and a value compare, not a value
// compare alone. A value compare by itself cannot refuse an absent key when
// the expected value is empty, because bytes.Equal(nil, []byte{}) is true.
func TestGuardCompares_NonNilValueAlsoRequiresExistence(t *testing.T) {
	compares := guardCompares([]kvstore.Guard{{Key: "claim", Value: []byte{}}})
	if len(compares) != 2 {
		t.Fatalf("len(compares) = %d, want 2 (existence + value)", len(compares))
	}
	if compares[0].Target != service.CompareVersion || compares[0].Result != service.CompareGreater || compares[0].Version != 0 {
		t.Errorf("compares[0] = %+v, want an existsGuard-shaped version>0 compare", compares[0])
	}
	if compares[1].Target != service.CompareValue || compares[1].Result != service.CompareEqual || len(compares[1].Value) != 0 {
		t.Errorf("compares[1] = %+v, want a value==[] compare", compares[1])
	}
}

// TestGuardCompares_NilValueStillJustOneCompare confirms the fix above did
// not widen the nil ("must be absent") case, which needs no existence
// compare of its own — the version-0 compare already means absent.
func TestGuardCompares_NilValueStillJustOneCompare(t *testing.T) {
	compares := guardCompares([]kvstore.Guard{{Key: "claim", Value: nil}})
	if len(compares) != 1 {
		t.Fatalf("len(compares) = %d, want 1", len(compares))
	}
	if compares[0].Target != service.CompareVersion || compares[0].Version != 0 {
		t.Errorf("compares[0] = %+v, want a version==0 compare", compares[0])
	}
}

// TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_NonNilEmptyValueRefusesAbsentKey
// is the end-to-end regression against the REAL evaluator: a guard expecting
// a PRESENT key with an empty value must refuse when the key is genuinely
// absent, not silently succeed because an absent key's value degrades to
// empty in a bare value compare.
func TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_NonNilEmptyValueRefusesAbsentKey(t *testing.T) {
	ctx := context.Background()
	tx := newEvaluatingTxService()
	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	guarded := store.(kvstore.MultiGuardedBatchKVStore)

	applied, err := guarded.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "claim", Value: []byte{}}}, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if applied {
		t.Fatal("expected the batch to be refused: the guard key is absent, not present-with-empty-value")
	}
	if _, ok := tx.data["primary"]; ok {
		t.Error("primary write must not land when the guard refuses")
	}
}

// TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_NonNilEmptyValueAppliesWhenPresent
// is the positive sibling: a present key genuinely holding an empty value
// must still satisfy the same guard, so the existence compare did not
// overcorrect into refusing the case it is supposed to allow.
func TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_NonNilEmptyValueAppliesWhenPresent(t *testing.T) {
	ctx := context.Background()
	tx := newEvaluatingTxService()
	tx.put("claim", []byte{})
	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	guarded := store.(kvstore.MultiGuardedBatchKVStore)

	applied, err := guarded.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "claim", Value: []byte{}}}, []kvstore.KV{
		{Key: "primary", Value: []byte("role-data")},
	})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the batch to apply: the guard key is present and holds the expected empty value")
	}
	if v, ok := tx.data["primary"]; !ok || string(v.Value) != "role-data" {
		t.Errorf("primary write did not land: %+v", tx.data["primary"])
	}
}

// TestGuardedKVStoreAdapter_CapabilityRefusalIsGuardedBatchUnsupported pins
// the translation the role/user routers' probe depends on: a state machine
// with no transaction subsystem ("memory", "concurrent") refuses with
// service.CapabilityUnsupportedError, and the adapter must report that as
// kvstore.GuardedBatchUnsupportedError — matching
// kvstore.ErrAtomicWriteUnsupported — so kvstore.ProbeGuardedBatch reads it
// as a definitive "not capable" rather than an inconclusive failure.
func TestGuardedKVStoreAdapter_CapabilityRefusalIsGuardedBatchUnsupported(t *testing.T) {
	refusal := &service.CapabilityUnsupportedError{Message: "statemachine: mvcc not configured"}
	tx := &mockTransactionServicer{executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
		return nil, refusal
	}}
	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	guarded := store.(kvstore.MultiGuardedBatchKVStore)

	_, err = guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim"}}, nil)
	var gbu *kvstore.GuardedBatchUnsupportedError
	if !errors.As(err, &gbu) {
		t.Fatalf("err = %v (%T), want *kvstore.GuardedBatchUnsupportedError", err, err)
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Error("errors.Is(err, kvstore.ErrAtomicWriteUnsupported) = false, want true")
	}
	var cue *service.CapabilityUnsupportedError
	if !errors.As(err, &cue) {
		t.Error("the backend's own refusal must stay reachable through Unwrap")
	}

	capable, perr := kvstore.ProbeGuardedBatch(context.Background(), guarded)
	if perr != nil || capable {
		t.Errorf("ProbeGuardedBatch = (%v, %v), want (false, nil) for a definitive refusal", capable, perr)
	}
}

// TestGuardedKVStoreAdapter_OtherErrorsPassThroughUntranslated is the
// control: an error that is NOT a capability refusal (here a lost leader)
// must reach the caller as-is, so the probe reports it as inconclusive.
func TestGuardedKVStoreAdapter_OtherErrorsPassThroughUntranslated(t *testing.T) {
	notLeader := &service.NotLeaderError{}
	tx := &mockTransactionServicer{executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
		return nil, notLeader
	}}
	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	guarded := store.(kvstore.MultiGuardedBatchKVStore)

	_, err = guarded.PutAllIfAllUnchanged(context.Background(), []kvstore.Guard{{Key: "claim"}}, nil)
	if !errors.Is(err, notLeader) || errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want the NotLeaderError untranslated", err)
	}
	if _, perr := kvstore.ProbeGuardedBatch(context.Background(), guarded); !errors.Is(perr, notLeader) {
		t.Errorf("ProbeGuardedBatch err = %v, want the inconclusive NotLeaderError", perr)
	}
}

// TestGuardCompares_NonEmptyValueIsExistencePlusValue pins the ordinary
// expected-value guard (pkg/custodian's single-guard PutAllIfUnchanged, for
// instance): an existence compare followed by a value compare on the exact
// expected bytes.
func TestGuardCompares_NonEmptyValueIsExistencePlusValue(t *testing.T) {
	compares := guardCompares([]kvstore.Guard{{Key: "owner", Value: []byte("alice")}})
	if len(compares) != 2 {
		t.Fatalf("len(compares) = %d, want 2 (existence + value)", len(compares))
	}
	if c := compares[1]; c.Target != service.CompareValue || c.Result != service.CompareEqual || string(c.Value) != "alice" || string(c.Key) != "owner" {
		t.Errorf("compares[1] = %+v, want value == \"alice\" on \"owner\"", c)
	}
}

// TestExecuteGuardedBatch_NilTransactionServiceRefuses pins the definite
// refusal for a hand-built adapter with no transaction plane: a typed error,
// never a panic and never an apparent success.
func TestExecuteGuardedBatch_NilTransactionServiceRefuses(t *testing.T) {
	applied, err := ExecuteGuardedBatch(context.Background(), nil, []kvstore.Guard{{Key: "claim"}}, []kvstore.KV{{Key: "k", Value: []byte("v")}})
	if applied || !errors.Is(err, ErrTransactionServiceRequired) {
		t.Fatalf("ExecuteGuardedBatch(nil tx) = (%v, %v), want (false, ErrTransactionServiceRequired)", applied, err)
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) || !errors.Is(err, kvstore.ErrNotApplied) {
		t.Errorf("err = %v, want it to read as an unsupported capability that wrote nothing", err)
	}
}

// TestExecuteGuardedBatch_SendsWritesAsSuccessOps confirms the proposal
// shape: one put per write, in order, as success operations only.
func TestExecuteGuardedBatch_SendsWritesAsSuccessOps(t *testing.T) {
	var gotSuccess, gotFailure []service.Op
	tx := &mockTransactionServicer{executeFn: func(_ context.Context, _ []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
		gotSuccess, gotFailure = success, failure
		return &service.TransactionResponse{Succeeded: true}, nil
	}}
	applied, err := ExecuteGuardedBatch(context.Background(), tx, nil, []kvstore.KV{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}})
	if err != nil || !applied {
		t.Fatalf("ExecuteGuardedBatch = (%v, %v), want (true, nil)", applied, err)
	}
	if len(gotFailure) != 0 || len(gotSuccess) != 2 || string(gotSuccess[0].Key) != "a" || string(gotSuccess[1].Value) != "2" || gotSuccess[0].Type != service.OpPut {
		t.Errorf("success = %+v, failure = %+v, want two puts a=1, b=2 and no failure ops", gotSuccess, gotFailure)
	}
}

// TestExecuteGuardedBatch_DeleteWriteBecomesOpDelete confirms a KV with
// Delete set translates to service.OpDelete, carrying no Value, rather than
// a put of whatever bytes happened to be set (go-qrdb#589): a batch that
// could only put would need the delete half of a move/rename as a second,
// non-atomic operation.
func TestExecuteGuardedBatch_DeleteWriteBecomesOpDelete(t *testing.T) {
	var gotSuccess []service.Op
	tx := &mockTransactionServicer{executeFn: func(_ context.Context, _ []service.Compare, success, _ []service.Op) (*service.TransactionResponse, error) {
		gotSuccess = success
		return &service.TransactionResponse{Succeeded: true}, nil
	}}
	applied, err := ExecuteGuardedBatch(context.Background(), tx, nil, []kvstore.KV{
		{Key: "old", Delete: true},
		{Key: "new", Value: []byte("v")},
	})
	if err != nil || !applied {
		t.Fatalf("ExecuteGuardedBatch = (%v, %v), want (true, nil)", applied, err)
	}
	if len(gotSuccess) != 2 {
		t.Fatalf("success ops = %+v, want 2", gotSuccess)
	}
	if gotSuccess[0].Type != service.OpDelete || string(gotSuccess[0].Key) != "old" || gotSuccess[0].Value != nil {
		t.Errorf("ops[0] = %+v, want OpDelete(old) with no value", gotSuccess[0])
	}
	if gotSuccess[1].Type != service.OpPut || string(gotSuccess[1].Key) != "new" || string(gotSuccess[1].Value) != "v" {
		t.Errorf("ops[1] = %+v, want OpPut(new)=v", gotSuccess[1])
	}
}

// TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_MovesAKeyAtomically is the
// move/rename shape go-qrdb#589 asked for: delete the old key and put the
// new one in the SAME guarded batch, backed by the real MVCC evaluator
// rather than a mock, so this would catch a regression in how the delete
// half is proposed, not just how it is translated.
func TestGuardedKVStoreAdapter_PutAllIfAllUnchanged_MovesAKeyAtomically(t *testing.T) {
	ctx := context.Background()
	tx := newEvaluatingTxService()
	tx.put("old-name", []byte("payload"))

	store, err := NewGuardedKVStoreAdapter(&mockKVServicer{}, nil, tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	multi := store.(kvstore.MultiGuardedBatchKVStore)

	applied, err := multi.PutAllIfAllUnchanged(ctx, nil, []kvstore.KV{
		{Key: "old-name", Delete: true},
		{Key: "new-name", Value: []byte("payload")},
	})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the move to apply")
	}
	if _, stillThere := tx.data["old-name"]; stillThere {
		t.Error("old-name is still present after the move")
	}
	if v, ok := tx.data["new-name"]; !ok || string(v.Value) != "payload" {
		t.Errorf("new-name = %+v, want payload", tx.data["new-name"])
	}
}

// TestExecuteGuardedBatch_ProvenNotStoredIsNotApplied pins the translation
// that lets role/user reconciliation return at once: any error the service
// layer marks service.ErrNotStored — a propose-time NotLeader here — reaches
// kvstore callers as kvstore.ErrNotApplied, with the cause intact. An
// unmarked error stays unmarked: its outcome is unknown.
func TestExecuteGuardedBatch_ProvenNotStoredIsNotApplied(t *testing.T) {
	notLeader := &service.NotLeaderError{ShardID: 1}
	unknown := errors.New("proposal timed out")
	for _, tc := range []struct {
		name        string
		err         error
		wantApplied bool
	}{
		{name: "not leader", err: notLeader, wantApplied: true},
		{name: "unmarked failure", err: unknown, wantApplied: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &mockTransactionServicer{executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
				return nil, tc.err
			}}
			_, err := ExecuteGuardedBatch(context.Background(), tx, []kvstore.Guard{{Key: "claim"}}, nil)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want the cause intact", err)
			}
			if got := errors.Is(err, kvstore.ErrNotApplied); got != tc.wantApplied {
				t.Errorf("errors.Is(err, kvstore.ErrNotApplied) = %v, want %v", got, tc.wantApplied)
			}
			if errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
				t.Error("neither is a capability refusal")
			}
		})
	}
}
