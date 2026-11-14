package placement

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestStore_PlacementKeysCarryOnlyTheDatabaseID is the row's scan test: the
// placement table is keyed __sys:placement/{tenant}/{dbid}, so a scan of the
// system store's placement keys finds each database's opaque id and never
// its name. The entry is reached by the id, not the name.
func TestStore_PlacementKeysCarryOnlyTheDatabaseID(t *testing.T) {
	kv := newFakeKV()
	s, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	ids := map[string]string{
		"orders":   "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4",
		"invoices": "a1b2c3d4e5f60718293a4b5c6d7e8f90",
	}
	shard := uint64(1000)
	for name, id := range ids {
		p := samplePlacement("acme", name, shard)
		p.DatabaseID = id
		if err := s.Put(ctx, p); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
		shard++
	}

	var keys []string
	if err := kv.Scan(ctx, PlacementPrefix, func(key string, _ []byte) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(keys) != len(ids) {
		t.Fatalf("scan found %d placement keys, want %d: %v", len(keys), len(ids), keys)
	}
	for _, key := range keys {
		for name, id := range ids {
			if strings.Contains(key, name) {
				t.Fatalf("placement key %q carries the database name %q", key, name)
			}
			if key == PlacementPrefix+"acme/"+id {
				delete(ids, name)
			}
		}
	}
	if len(ids) != 0 {
		t.Fatalf("no placement key for %v: keys %v", ids, keys)
	}

	got, err := s.Get(ctx, "acme", "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4")
	if err != nil {
		t.Fatalf("Get by id: %v", err)
	}
	if got.Database != "orders" || got.DatabaseID != "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4" {
		t.Fatalf("Get by id = %+v", got)
	}
	if _, err := s.Get(ctx, "acme", "orders"); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("Get by name: err = %v, want ErrPlacementNotFound (the table is keyed by id)", err)
	}
}

// TestStore_PutRefusesAnEntryWithoutADatabaseID: an entry with a name but no
// id has no key to be stored under.
func TestStore_PutRefusesAnEntryWithoutADatabaseID(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p := samplePlacement("acme", "orders", 1000)
	p.DatabaseID = ""
	if err := s.Put(context.Background(), p); !errors.Is(err, ErrEmptyDatabaseID) {
		t.Fatalf("Put without an id: err = %v, want ErrEmptyDatabaseID", err)
	}
}

// TestStore_GetByNameFindsTheTenantsEntry: a caller holding only a name (a
// follower with no catalog record) finds the entry that names the database
// among the tenant's own; another tenant's entry of that name is not it.
func TestStore_GetByNameFindsTheTenantsEntry(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	mine := samplePlacement("acme", "orders", 1000)
	mine.DatabaseID = "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4"
	theirs := samplePlacement("globex", "orders", 1001)
	theirs.DatabaseID = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	for _, p := range []*Placement{mine, theirs} {
		if err := s.Put(ctx, p); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	got, err := s.GetByName(ctx, "acme", "orders")
	if err != nil || got.DatabaseID != mine.DatabaseID {
		t.Fatalf("GetByName = %+v, %v; want acme's entry", got, err)
	}
	if _, err := s.GetByName(ctx, "acme", "invoices"); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("GetByName of an unplaced name: err = %v, want ErrPlacementNotFound", err)
	}
	if _, err := s.GetByName(ctx, "acme", ""); !errors.Is(err, ErrEmptyDatabase) {
		t.Fatalf("GetByName of an empty name: err = %v, want ErrEmptyDatabase", err)
	}
}

// putNamed stores a placement for tenant acme naming database orders under
// the given id, with the drop fence set as asked.
func putNamed(t *testing.T, s *Store, id string, shard uint64, dropping bool) {
	t.Helper()
	p := samplePlacement("acme", "orders", shard)
	p.DatabaseID = id
	p.Dropping = dropping
	if err := s.Put(context.Background(), p); err != nil {
		t.Fatalf("Put(%s): %v", id, err)
	}
}

// TestStore_GetByNamePrefersTheLiveEntry is #1517 Codex finding 2: a drop
// whose placement delete failed leaves a fenced entry naming the database,
// and a re-create under the same name stores a second, live entry. The
// lookup must resolve the live one whichever id sorts first in the scan.
func TestStore_GetByNamePrefersTheLiveEntry(t *testing.T) {
	const low = "00000000000000000000000000000001"
	const high = "ffffffffffffffffffffffffffffff01"
	for _, tc := range []struct {
		name          string
		droppingID    string
		liveID        string
		droppingShard uint64
		liveShard     uint64
	}{
		{"dropping id sorts first", low, high, 1000, 1001},
		{"live id sorts first", high, low, 1000, 1001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewStore(newFakeKV())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			putNamed(t, s, tc.droppingID, tc.droppingShard, true)
			putNamed(t, s, tc.liveID, tc.liveShard, false)
			got, err := s.GetByName(context.Background(), "acme", "orders")
			if err != nil {
				t.Fatalf("GetByName: %v", err)
			}
			if got.DatabaseID != tc.liveID || got.Dropping {
				t.Fatalf("GetByName = id %s dropping=%v; want the live entry %s", got.DatabaseID, got.Dropping, tc.liveID)
			}
		})
	}
}

// TestStore_GetByNameFindsAFencedEntryAlone proves a resumed drop still
// finds its own entry: when only a Dropping entry names the database, the
// lookup returns it.
func TestStore_GetByNameFindsAFencedEntryAlone(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const id = "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4"
	putNamed(t, s, id, 1000, true)
	got, err := s.GetByName(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.DatabaseID != id || !got.Dropping {
		t.Fatalf("GetByName = id %s dropping=%v; want the fenced entry %s", got.DatabaseID, got.Dropping, id)
	}
}

// TestStore_GetBoundKeysByTheCatalogDatabaseID proves GetBound returns the
// entry the catalog record's id keys even when a dropped predecessor's entry,
// which GetByName lists first, shares the name; refuses an entry under the id
// that names another database; and reports a missing id as not found.
func TestStore_GetBoundKeysByTheCatalogDatabaseID(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	const predecessor = "00000000000000000000000000000001"
	const successor = "ffffffffffffffffffffffffffffff01"
	putNamed(t, s, predecessor, 1000, true)
	putNamed(t, s, successor, 1001, true)
	// By name the two fenced entries are ambiguous: GetByName refuses rather
	// than hand back whichever it lists first, so nothing retires the wrong
	// shard; GetBound names exactly one.
	if got, err := s.GetByName(ctx, "acme", "orders"); !errors.Is(err, ErrAmbiguousPlacement) {
		t.Fatalf("GetByName = %+v, %v; want ErrAmbiguousPlacement", got, err)
	}
	got, err := s.GetBound(ctx, "acme", successor, "orders")
	if err != nil || got.DatabaseID != successor || got.ShardID != 1001 {
		t.Fatalf("GetBound = %+v, %v; want the successor's entry", got, err)
	}

	other := samplePlacement("acme", "other", 1002)
	other.DatabaseID = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	if err := s.Put(ctx, other); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var mismatch *IdentityMismatchError
	if _, err := s.GetBound(ctx, "acme", other.DatabaseID, "orders"); !errors.As(err, &mismatch) ||
		mismatch.GotDatabase != "other" || mismatch.DatabaseID != other.DatabaseID {
		t.Fatalf("GetBound of another database's entry: err = %v, want *IdentityMismatchError", err)
	}
	if _, err := s.GetBound(ctx, "acme", "5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e", "orders"); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("GetBound of an unplaced id: err = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_GetByNameRefusesTwoLiveEntries proves the lookup fails closed
// when two live entries name the database, rather than pick one by scan
// order.
func TestStore_GetByNameRefusesTwoLiveEntries(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	putNamed(t, s, "00000000000000000000000000000001", 1000, false)
	putNamed(t, s, "ffffffffffffffffffffffffffffff01", 1001, false)
	_, err = s.GetByName(context.Background(), "acme", "orders")
	if !errors.Is(err, ErrAmbiguousPlacement) {
		t.Fatalf("GetByName: err = %v, want ErrAmbiguousPlacement", err)
	}
	var se *StoreError
	if !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("GetByName: err = %#v, want a *StoreError with Op get", err)
	}
}
