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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// evaluatingTxService is a transaction service that evaluates its compares
// with the REAL evaluator, mvcc.Evaluate.
//
// A mock returning a canned Succeeded would make every test below vacuous:
// the defect being pinned is that a particular compare EVALUATES the wrong
// way, so a double that never evaluates cannot see it. This one keeps the
// version metadata a real store keeps and hands each compare to the same
// function the MVCC engine calls (go-qrdb#56).
type evaluatingTxService struct {
	data map[string]*service.KeyValue
	rev  int64
}

func newEvaluatingTxService() *evaluatingTxService {
	return &evaluatingTxService{data: map[string]*service.KeyValue{}}
}

func (s *evaluatingTxService) put(key string, value []byte) {
	s.rev++
	if existing, ok := s.data[key]; ok {
		existing.Value = value
		existing.Version++
		existing.ModRevision = s.rev
		return
	}
	s.data[key] = &service.KeyValue{
		Key:            []byte(key),
		Value:          value,
		Version:        1,
		CreateRevision: s.rev,
		ModRevision:    s.rev,
	}
}

func (s *evaluatingTxService) Execute(
	_ context.Context,
	compares []service.Compare,
	successOps, failureOps []service.Op,
) (*service.TransactionResponse, error) {
	succeeded := true
	for i := range compares {
		// data[key] is nil for an absent key, which is what the engine passes
		// and what makes the version and value comparisons differ.
		if !mvcc.Evaluate(&compares[i], s.data[string(compares[i].Key)]) {
			succeeded = false
			break
		}
	}

	ops := successOps
	if !succeeded {
		ops = failureOps
	}
	for _, op := range ops {
		switch op.Type {
		case service.OpPut:
			s.put(string(op.Key), op.Value)
		case service.OpDelete:
			delete(s.data, string(op.Key))
		}
	}
	return &service.TransactionResponse{Succeeded: succeeded}, nil
}

func (s *evaluatingTxService) ExecuteWithMode(
	ctx context.Context,
	compares []service.Compare,
	successOps, failureOps []service.Op,
	_ writemode.Mode,
) (*service.TransactionResponse, error) {
	return s.Execute(ctx, compares, successOps, failureOps)
}

func (s *evaluatingTxService) ExecuteEntity(
	_ context.Context,
	_ *service.TransactionEntityRequest,
) (*service.TransactionEntityResponse, error) {
	return nil, ErrNoTransactionService
}

var _ service.TransactionServicer = (*evaluatingTxService)(nil)

// An EMPTY expected value must not turn a guarded verb into a create or a
// phantom delete.
//
// An absent key's value is nil, and nil compares equal to an empty slice, so a
// value compare alone is SATISFIED by a key that does not exist. pkg/store's
// evaluator says so outright — "Key does not exist: only equal-to-zero-length
// succeeds" — which means CompareAndSwap(key, []byte{}, next) executed its
// OpPut and created the key, contradicting both the AtomicKVStore contract and
// the comment that sat above the code claiming absence always fails.
//
// Both spellings of empty are covered because they are the same value to the
// comparison and different values to a reader: a caller who writes nil and a
// caller who writes []byte{} must get the same refusal.
func TestKVSealBackend_GuardedVerbs_EmptyExpectedDoesNotActOnAnAbsentKey(t *testing.T) {
	ctx := context.Background()

	for _, expectedName := range []string{"nil", "empty"} {
		var expected []byte
		if expectedName == "empty" {
			expected = []byte{}
		}

		t.Run("CompareAndSwap/"+expectedName, func(t *testing.T) {
			tx := newEvaluatingTxService()
			b := kvSealBackend{tx: tx}

			swapped, err := b.CompareAndSwap(ctx, "absent", expected, []byte("created"))
			if err != nil {
				t.Fatalf("CompareAndSwap: %v", err)
			}
			if swapped {
				t.Error("a swap reported success on an absent key")
			}
			if kv, ok := tx.data["absent"]; ok {
				t.Fatalf("a swap CREATED an absent key, storing %q — a swap edits, "+
					"it must never create", kv.Value)
			}
		})

		t.Run("CompareAndDelete/"+expectedName, func(t *testing.T) {
			tx := newEvaluatingTxService()
			b := kvSealBackend{tx: tx}

			deleted, err := b.CompareAndDelete(ctx, "absent", expected)
			if err != nil {
				t.Fatalf("CompareAndDelete: %v", err)
			}
			if deleted {
				t.Error("a delete reported having removed a key that was never there")
			}
		})
	}
}

// The existence guard must not break the operations it protects.
//
// A guard that refused everything would pass the test above for the wrong
// reason, so the ordinary paths are asserted alongside it.
func TestKVSealBackend_GuardedVerbs_StillActOnAPresentKey(t *testing.T) {
	ctx := context.Background()

	t.Run("swaps a matching value", func(t *testing.T) {
		tx := newEvaluatingTxService()
		tx.put("k", []byte("old"))
		b := kvSealBackend{tx: tx}

		swapped, err := b.CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
		if err != nil || !swapped {
			t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
		}
		if got := string(tx.data["k"].Value); got != "new" {
			t.Errorf("stored %q, want %q", got, "new")
		}
	})

	t.Run("refuses a stale value and changes nothing", func(t *testing.T) {
		tx := newEvaluatingTxService()
		tx.put("k", []byte("old"))
		b := kvSealBackend{tx: tx}

		swapped, err := b.CompareAndSwap(ctx, "k", []byte("stale"), []byte("new"))
		if err != nil || swapped {
			t.Fatalf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
		}
		if got := string(tx.data["k"].Value); got != "old" {
			t.Errorf("stored %q, want the holder's %q", got, "old")
		}
	})

	t.Run("deletes a matching value", func(t *testing.T) {
		tx := newEvaluatingTxService()
		tx.put("k", []byte("old"))
		b := kvSealBackend{tx: tx}

		deleted, err := b.CompareAndDelete(ctx, "k", []byte("old"))
		if err != nil || !deleted {
			t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
		}
		if _, ok := tx.data["k"]; ok {
			t.Error("a matching delete left the key in place")
		}
	})

	// An empty value STORED under a key is a real value, and a swap from it
	// must work. This is the case the existence guard has to leave alone: the
	// key exists, so the empty-value comparison is legitimate here.
	t.Run("swaps a stored empty value", func(t *testing.T) {
		tx := newEvaluatingTxService()
		tx.put("k", []byte{})
		b := kvSealBackend{tx: tx}

		swapped, err := b.CompareAndSwap(ctx, "k", []byte{}, []byte("new"))
		if err != nil || !swapped {
			t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil) — an empty value "+
				"that was actually stored is swappable", swapped, err)
		}
		if got := string(tx.data["k"].Value); got != "new" {
			t.Errorf("stored %q, want %q", got, "new")
		}
	})
}
