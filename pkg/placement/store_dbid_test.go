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
