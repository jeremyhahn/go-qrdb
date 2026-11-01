package service

import (
	"testing"
)

// decoratedTx is a TransactionServicer decorator, the shape of a tenant's
// quota or a database's namespace wrapper: it unwraps to the service it
// decorates.
type decoratedTx struct {
	TransactionServicer
}

func (d *decoratedTx) Unwrap() TransactionServicer { return d.TransactionServicer }

var _ TransactionUnwrapper = (*decoratedTx)(nil)

// opaqueTx is a TransactionServicer that decorates without unwrapping.
type opaqueTx struct {
	TransactionServicer
}

// TestBaseTransactionService checks the lock token gate's host and shard are
// found under any chain of decorators, and that nothing else is mistaken for
// them.
func TestBaseTransactionService(t *testing.T) {
	base := NewTransactionService(&mockHost{}, 7).(*TransactionService)
	t.Run("the service itself", func(t *testing.T) {
		got, ok := baseTransactionService(base)
		if !ok || got != base {
			t.Fatalf("baseTransactionService = %p, %v, want %p, true", got, ok, base)
		}
	})
	t.Run("under two decorators", func(t *testing.T) {
		got, ok := baseTransactionService(&decoratedTx{&decoratedTx{base}})
		if !ok || got != base {
			t.Fatalf("baseTransactionService = %p, %v, want %p, true", got, ok, base)
		}
	})
	t.Run("a decorator that does not unwrap", func(t *testing.T) {
		if got, ok := baseTransactionService(&decoratedTx{&opaqueTx{base}}); ok {
			t.Fatalf("baseTransactionService = %p, true, want false", got)
		}
	})
	t.Run("a decorator over nothing", func(t *testing.T) {
		if got, ok := baseTransactionService(&decoratedTx{}); ok {
			t.Fatalf("baseTransactionService = %p, true, want false", got)
		}
	})
	t.Run("a nil service", func(t *testing.T) {
		if got, ok := baseTransactionService((*TransactionService)(nil)); ok {
			t.Fatalf("baseTransactionService = %p, true, want false", got)
		}
		if got, ok := baseTransactionService(nil); ok {
			t.Fatalf("baseTransactionService = %p, true, want false", got)
		}
	})
}

// TestBaseTransactionService_DecoratedGateKeepsTokenField checks a lock
// served through a decorated transaction service reads its own shard's gate:
// while featuregate.LockCreateRevisionToken is inactive there, the record
// keeps its fencing_token field for a replica that still reads it.
func TestBaseTransactionService_DecoratedGateKeepsTokenField(t *testing.T) {
	host := &gatedHost{mockHost: &mockHost{}, enabled: map[uint64]bool{7: false}}
	tx, ok := baseTransactionService(&decoratedTx{NewTransactionService(host, 7)})
	if !ok {
		t.Fatal("baseTransactionService found no TransactionService under the decorator")
	}
	if lockTokenFieldRetired(tx.host, tx.shardID) {
		t.Fatal("inactive gate behind a decorator retired the field, want it kept")
	}
	host.enabled[7] = true
	if !lockTokenFieldRetired(tx.host, tx.shardID) {
		t.Fatal("active gate behind a decorator kept the field, want it retired")
	}
}
