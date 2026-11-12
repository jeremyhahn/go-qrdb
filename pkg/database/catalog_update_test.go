package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// swapHookStore is a catalog store whose CompareAndSwap runs beforeSwap
// first, once, and fails with swapErr when it is set: a concurrent writer
// landing between Update's read and its swap, made deterministic.
type swapHookStore struct {
	*kvstore.MemoryKVStore
	beforeSwap func()
	swaps      int
	swapErr    error
}

func (s *swapHookStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	s.swaps++
	if s.swapErr != nil {
		return false, s.swapErr
	}
	if s.beforeSwap != nil {
		hook := s.beforeSwap
		s.beforeSwap = nil
		hook()
	}
	return s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func mustMarshalMeta(t *testing.T, meta DatabaseMeta) []byte {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func newSwapHookCatalog(t *testing.T) (*KVCatalog, *swapHookStore) {
	t.Helper()
	store := &swapHookStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	cat, err := NewKVCatalog(store, nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	return cat, store
}

func TestKVCatalog_UpdateRefusesAnAbsentRecordOrAnotherIncarnation(t *testing.T) {
	ctx := context.Background()
	cat, _ := newSwapHookCatalog(t)
	var nf *NotFoundError

	if err := cat.Update(ctx, "", "orders", "one", markDropping); !errors.As(err, &nf) {
		t.Fatalf("Update of an absent record = %v, want *NotFoundError", err)
	}
	if _, err := cat.Get(ctx, "", "orders"); !errors.As(err, &nf) {
		t.Fatalf("Update created an absent record: Get = %v", err)
	}

	live := DatabaseMeta{ID: "test-id", Name: "orders", Backend: BackendMemory, Incarnation: "two"}
	if err := cat.Create(ctx, live); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cat.Update(ctx, "", "orders", "one", markDropping); !errors.As(err, &nf) {
		t.Fatalf("Update of another incarnation = %v, want *NotFoundError", err)
	}
	got, err := cat.Get(ctx, "", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Incarnation != "two" || got.Dropping {
		t.Fatalf("record after a stale update = %+v, want incarnation two, not dropping", got)
	}
}

// replaceRecord is a Catalog.Update mutation that replaces the record with
// meta, for a test that plants a record whole.
func replaceRecord(meta DatabaseMeta) func(*DatabaseMeta) error {
	return func(recorded *DatabaseMeta) error {
		*recorded = meta
		return nil
	}
}

// markDropping is a Catalog.Update mutation that sets the drop fence.
func markDropping(meta *DatabaseMeta) error {
	meta.Dropping = true
	return nil
}

func TestKVCatalog_UpdateRetriesASwapLostToTheSameIncarnation(t *testing.T) {
	ctx := context.Background()
	cat, store := newSwapHookCatalog(t)
	meta := DatabaseMeta{ID: "test-id", Name: "orders", Backend: BackendMemory, Incarnation: "one"}
	if err := cat.Create(ctx, meta); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Another update of the same incarnation lands between the read and
	// the swap: the swap loses, and Update applies its mutation again to
	// the new record, keeping the field the other update wrote.
	store.beforeSwap = func() {
		other := meta
		other.DataDir = "elsewhere"
		data := mustMarshalMeta(t, other)
		if err := store.MemoryKVStore.Put(ctx, CatalogKey("", "orders"), data); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	if err := cat.Update(ctx, "", "orders", "one", markDropping); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if store.swaps != 2 {
		t.Fatalf("swaps = %d, want 2 (one lost, one retried)", store.swaps)
	}
	if got, _ := cat.Get(ctx, "", "orders"); !got.Dropping || got.DataDir != "elsewhere" {
		t.Fatalf("record after the retried update = %+v, want dropping with the concurrent data dir kept", got)
	}

	// A swap whose outcome is unknown is reported, never retried blind.
	boom := errors.New("proposal timed out")
	store.swapErr = boom
	var writeErr *CatalogWriteError
	if err := cat.Update(ctx, "", "orders", "one", markDropping); !errors.As(err, &writeErr) || !errors.Is(err, boom) {
		t.Fatalf("Update with a failing swap = %v, want *CatalogWriteError wrapping the cause", err)
	}
}

// TestKVCatalog_UpdateReturnsTheMutationsErrorUnwritten checks a mutation
// that refuses the record leaves it as it was.
func TestKVCatalog_UpdateReturnsTheMutationsErrorUnwritten(t *testing.T) {
	ctx := context.Background()
	cat, store := newSwapHookCatalog(t)
	meta := DatabaseMeta{ID: "test-id", Name: "orders", Backend: BackendMemory, Incarnation: "one"}
	if err := cat.Create(ctx, meta); err != nil {
		t.Fatalf("Create: %v", err)
	}
	refused := errors.New("refused")
	err := cat.Update(ctx, "", "orders", "one", func(meta *DatabaseMeta) error {
		meta.Dropping = true
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Update = %v, want the mutation's error", err)
	}
	if store.swaps != 0 {
		t.Fatalf("swaps = %d, want 0", store.swaps)
	}
	if got, _ := cat.Get(ctx, "", "orders"); got.Dropping {
		t.Fatalf("record after a refused mutation = %+v, want unchanged", got)
	}
}

// A database dropped through one node stays dropped when a node still
// holding it updates its configuration.
func TestManager_UpdateThroughAStaleHandleDoesNotResurrectADrop(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	a := NewManager(nil, t.TempDir())
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir())
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	if _, err := b.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if err := a.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on A: %v", err)
	}

	var nf *NotFoundError
	pess := &SQLConfig{Concurrency: ConcurrencyPessimistic}
	if err := b.UpdateSQLForTenant(ctx, "orders", pess); !errors.As(err, &nf) {
		t.Fatalf("UpdateSQLForTenant on B after the drop = %v, want *NotFoundError", err)
	}
	if _, err := cat.Get(ctx, "tenant-a", "orders"); !errors.As(err, &nf) {
		t.Fatalf("catalog Get after B's update = %v, want *NotFoundError (the drop stands)", err)
	}
}

// A database dropped and created again through one node keeps its new
// record when a node still holding the old incarnation updates it.
func TestManager_UpdateThroughAStaleHandleKeepsTheNewIncarnation(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	spy := &spyReclaimer{}
	a := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	stale, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if err := a.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on A: %v", err)
	}
	recreated, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	if err != nil {
		t.Fatalf("re-create on A: %v", err)
	}
	if recreated.Incarnation() == stale.Incarnation() {
		t.Fatal("the re-created database shares the dropped one's incarnation")
	}

	var nf *NotFoundError
	pess := &SQLConfig{Concurrency: ConcurrencyPessimistic}
	// Resolving the name on B now finds the successor (GetOwned
	// revalidates), so the stale handle is written through directly.
	if err := b.rewriteMeta(stale, func(meta *DatabaseMeta) { meta.SQL = pess }); !errors.As(err, &nf) {
		t.Fatalf("update through the stale handle = %v, want *NotFoundError", err)
	}
	got, err := cat.Get(ctx, "tenant-a", "orders")
	if err != nil {
		t.Fatalf("catalog Get: %v", err)
	}
	if got.Incarnation != recreated.Incarnation() || got.SQL != nil {
		t.Fatalf("record after the stale update = %+v, want the re-created incarnation, unchanged", got)
	}
}
