package volume

import (
	"context"
	"errors"
	"testing"
)

// newReservedCatalog returns a catalog holding one pool, "fast", with
// tenant "acme" reserved against it for 10 bytes.
func newReservedCatalog(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := newTestCatalog()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 10); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return cat, kv, tx
}

func TestCatalog_AdjustReservation_PropagatesKVFailure(t *testing.T) {
	cat, kv, _ := newReservedCatalog(t)
	kv.failGet = errors.New("injected get failure")
	if _, err := cat.AdjustReservation(context.Background(), "acme", 20); !errors.Is(err, kv.failGet) {
		t.Fatalf("AdjustReservation(kv failure) error = %v, want %v", err, kv.failGet)
	}
}

func TestCatalog_AdjustReservation_CorruptAssignmentRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	if err := kv.Put(context.Background(), AssignmentKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := cat.AdjustReservation(context.Background(), "acme", 20)
	var target *CorruptRecordError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(corrupt assignment) error = %v, want *CorruptRecordError", err)
	}
}

func TestCatalog_AdjustReservation_RefusesInterimRecord(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	if err := kv.Put(context.Background(), AssignmentKey("acme"), []byte(`{"pool":"fast","slot":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err := cat.AdjustReservation(context.Background(), "acme", 20)
	var target *InvalidAssignmentPathError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(interim record) error = %v, want *InvalidAssignmentPathError", err)
	}
}

func TestCatalog_AdjustReservation_UnchangedSizeIsNoOp(t *testing.T) {
	cat, _, tx := newReservedCatalog(t)
	// A no-op must not reach the transaction: a forced failure there
	// would surface as a conflict.
	tx.forceFailures = maxCASRetries
	got, err := cat.AdjustReservation(context.Background(), "acme", 10)
	if err != nil {
		t.Fatalf("AdjustReservation(unchanged): %v", err)
	}
	if got.ReservedBytes != 10 {
		t.Fatalf("ReservedBytes = %d, want 10", got.ReservedBytes)
	}
}

func TestCatalog_AdjustReservation_PoolMissing(t *testing.T) {
	cat, kv, _ := newReservedCatalog(t)
	ctx := context.Background()
	raw, err := kv.Get(ctx, PoolKey("fast"))
	if err != nil || raw == nil {
		t.Fatalf("expected the fast pool record to exist, got raw=%v err=%v", raw, err)
	}
	if _, err := kv.CompareAndDelete(ctx, PoolKey("fast"), raw); err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	_, err = cat.AdjustReservation(ctx, "acme", 20)
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(pool missing) error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_AdjustReservation_ExhaustsRetries(t *testing.T) {
	cat, _, tx := newReservedCatalog(t)
	tx.forceFailures = maxCASRetries
	_, err := cat.AdjustReservation(context.Background(), "acme", 5)
	var target *ReservationConflictError
	if !errors.As(err, &target) {
		t.Fatalf("AdjustReservation(persistent conflict) error = %v, want *ReservationConflictError", err)
	}
}

func TestCatalog_Release_PropagatesKVFailure(t *testing.T) {
	cat, kv, _ := newReservedCatalog(t)
	kv.failGet = errors.New("injected get failure")
	if err := cat.Release(context.Background(), "acme"); !errors.Is(err, kv.failGet) {
		t.Fatalf("Release(kv failure) error = %v, want %v", err, kv.failGet)
	}
}

func TestCatalog_Release_ExhaustsRetries(t *testing.T) {
	cat, kv, tx := newReservedCatalog(t)
	tx.forceFailures = maxCASRetries
	err := cat.Release(context.Background(), "acme")
	var target *ReservationConflictError
	if !errors.As(err, &target) {
		t.Fatalf("Release(persistent conflict) error = %v, want *ReservationConflictError", err)
	}
	if raw, _ := kv.Get(context.Background(), AssignmentKey("acme")); raw == nil {
		t.Fatal("a release that never committed must leave the assignment in place")
	}
}

func TestCatalog_ListAll_PropagatesScanFailure(t *testing.T) {
	cat, kv, _ := newReservedCatalog(t)
	kv.failScan = errors.New("injected scan failure")
	if _, err := cat.ListAll(context.Background()); !errors.Is(err, kv.failScan) {
		t.Fatalf("ListAll(scan failure) error = %v, want %v", err, kv.failScan)
	}
}
