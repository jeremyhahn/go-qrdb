package volume

import (
	"context"
	"errors"
	"testing"
)

// The decision's "the CAS on admit under concurrent moves and creates (no
// over-reservation)": a tenant create (Reserve) or another tenant's admit
// that lands between AdmitMove's reads of the destination pool and its
// transaction fails the admit's compare on the pool record; the retry
// reads the new reservation and admits only within the pool's capacity.

// newRaceCatalog is newMoveTestCatalog with the bulk pool's capacity set.
func newRaceCatalog(t *testing.T, bulkCapacity int64) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := newTestCatalog()
	setupMoveCatalog(t, cat, kv, "", "", bulkCapacity)
	return cat, kv, tx
}

// A tenant create that takes the last of the destination pool between the
// admit's reads and its commit refuses the admit: no journal, and the pool
// holds only the create's reservation.
func TestCatalog_AdmitMove_RacesReserveOverCapacity(t *testing.T) {
	cat, kv, tx := newRaceCatalog(t, 300)
	ctx := context.Background()
	var reserveErr error
	tx.beforeExecute = func() { _, reserveErr = cat.Reserve(ctx, "beta", "bulk", "beta", 200) }
	_, err := cat.AdmitMove(ctx, admitAcme())
	if reserveErr != nil {
		t.Fatalf("interleaved Reserve: %v", reserveErr)
	}
	var exceeded *PoolCapacityExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("AdmitMove racing a create error = %v, want *PoolCapacityExceededError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written although the create took the capacity")
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200 (the create only)", got)
	}
}

// With room for both, the admit's retry succeeds and both reservations
// are counted.
func TestCatalog_AdmitMove_RacesReserveWithinCapacity(t *testing.T) {
	cat, kv, tx := newRaceCatalog(t, 1000)
	ctx := context.Background()
	var reserveErr error
	tx.beforeExecute = func() { _, reserveErr = cat.Reserve(ctx, "beta", "bulk", "beta", 200) }
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove racing a create within capacity: %v", err)
	}
	if reserveErr != nil {
		t.Fatalf("interleaved Reserve: %v", reserveErr)
	}
	if storedMove(t, kv, "acme") == nil {
		t.Fatal("no journal after the admit retried")
	}
	if got := poolReserved(t, kv, "bulk"); got != 400 {
		t.Fatalf("bulk reserved = %d, want 400", got)
	}
}

// The reverse order: an admit that lands between a create's reads and its
// commit leaves the create no room; the create is refused and the admit's
// reservation stands.
func TestCatalog_Reserve_RacesAdmitOverCapacity(t *testing.T) {
	cat, kv, tx := newRaceCatalog(t, 300)
	ctx := context.Background()
	var admitErr error
	tx.beforeExecute = func() { _, admitErr = cat.AdmitMove(ctx, admitAcme()) }
	_, err := cat.Reserve(ctx, "beta", "bulk", "beta", 200)
	if admitErr != nil {
		t.Fatalf("interleaved AdmitMove: %v", admitErr)
	}
	var exceeded *PoolCapacityExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("Reserve racing an admit error = %v, want *PoolCapacityExceededError", err)
	}
	if storedMove(t, kv, "acme") == nil {
		t.Fatal("the admitted move's journal is gone")
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200 (the admit only)", got)
	}
}

// Two tenants' moves race for the destination's last capacity: the one
// that commits first wins, the other is refused with no journal.
func TestCatalog_AdmitMove_TwoTenantsRaceForTheLastCapacity(t *testing.T) {
	cat, kv, tx := newRaceCatalog(t, 300)
	ctx := context.Background()
	if _, err := cat.Reserve(ctx, "beta", "fast", "beta", 200); err != nil {
		t.Fatalf("Reserve beta: %v", err)
	}
	var betaErr error
	tx.beforeExecute = func() {
		_, betaErr = cat.AdmitMove(ctx, MoveAdmitRequest{TenantID: "beta", ToPool: "bulk", ReplicaNodes: []string{"n1", "n2"}})
	}
	_, err := cat.AdmitMove(ctx, admitAcme())
	if betaErr != nil {
		t.Fatalf("interleaved AdmitMove beta: %v", betaErr)
	}
	var exceeded *PoolCapacityExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("AdmitMove acme racing beta error = %v, want *PoolCapacityExceededError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("acme's journal written although beta took the capacity")
	}
	if storedMove(t, kv, "beta") == nil {
		t.Fatal("beta's journal is gone")
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200 (beta only)", got)
	}
}
