package server

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A tenant's and a database's transaction decorators unwrap to the service
// they decorate, so a lock served through them reaches the shard whose
// featuregate.LockCreateRevisionToken decides the fencing_token field.
func TestQuotaTransactionService_Unwrap(t *testing.T) {
	inner := service.NewTransactionService(nil, 1)
	if got := newQuotaTransactionService(inner, nil, "t1", 100).Unwrap(); got != inner {
		t.Fatalf("Unwrap = %v, want the decorated service", got)
	}
	if got := newQuotaTransactionService(nil, nil, "t1", 100).Unwrap(); got != nil {
		t.Fatalf("Unwrap over nothing = %v, want nil", got)
	}
}

func TestNamespacedTransactionService_Unwrap(t *testing.T) {
	inner := service.NewTransactionService(nil, 1)
	if got := newNamespacedTransactionService(inner, "t/db/").Unwrap(); got != inner {
		t.Fatalf("Unwrap = %v, want the decorated service", got)
	}
	if got := newNamespacedTransactionService(nil, "t/db/").Unwrap(); got != nil {
		t.Fatalf("Unwrap over nothing = %v, want nil", got)
	}
}

// The registry stacks a tenant's quota over a database's namespace; the
// chain still ends at the TransactionService.
func TestTransactionDecorators_UnwrapChain(t *testing.T) {
	inner := service.NewTransactionService(nil, 1)
	var tx service.TransactionServicer = newQuotaTransactionService(newNamespacedTransactionService(inner, "t/db/"), nil, "t1", 100)
	for range 2 {
		u, ok := tx.(service.TransactionUnwrapper)
		if !ok {
			t.Fatalf("%T does not unwrap", tx)
		}
		tx = u.Unwrap()
	}
	if tx != inner {
		t.Fatalf("chain ends at %v, want the TransactionService", tx)
	}
}
