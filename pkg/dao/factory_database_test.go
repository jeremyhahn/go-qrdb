//go:build codec_json

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Named-database construction tests. A DAO built for a database must land in
// that database's store or not be built at all: the default store is never an
// acceptable answer for a name the caller gave.

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// databaseFactory is a factory whose partitioned provider knows exactly the
// given databases and whose default provider is a separate store, so a test
// can tell a fallback from a resolution.
func databaseFactory(stores map[string]kvstore.KVStore, fallback kvstore.KVStore) *Factory {
	return NewFactoryWithConfig(&FactoryConfig{
		KVStoreProvider: func(uint64) kvstore.KVStore { return fallback },
		PartitionedProvider: func(name string, _ uint64) kvstore.KVStore {
			return stores[name]
		},
	})
}

func TestDatabaseKVStoreResolvesTheNamedDatabase(t *testing.T) {
	orders := newMemoryKVStore()
	var gotShard uint64
	f := NewFactoryWithConfig(&FactoryConfig{
		PartitionedProvider: func(name string, shard uint64) kvstore.KVStore {
			gotShard = shard
			if name == "orders" {
				return orders
			}
			return nil
		},
	})

	got, err := f.DatabaseKVStore("orders", 9)
	if err != nil {
		t.Fatalf("DatabaseKVStore: %v", err)
	}
	if got != orders || gotShard != 9 {
		t.Fatalf("DatabaseKVStore = (%v, shard %d), want the orders store on shard 9", got, gotShard)
	}
}

func TestDatabaseKVStoreNeverFallsBackToTheDefault(t *testing.T) {
	fallback := newMemoryKVStore()

	tests := []struct {
		name     string
		factory  *Factory
		database string
	}{
		{"no database named", databaseFactory(nil, fallback), ""},
		{"no partitioned provider", NewFactory(func(uint64) kvstore.KVStore { return fallback }), "orders"},
		{"the provider has no such database", databaseFactory(map[string]kvstore.KVStore{}, fallback), "orders"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.factory.DatabaseKVStore(tc.database, 0)
			var unresolved DatabaseStoreUnresolvedError
			if !errors.As(err, &unresolved) {
				t.Fatalf("DatabaseKVStore error = %v, want DatabaseStoreUnresolvedError", err)
			}
			if got != nil {
				t.Fatal("a refused resolution returned a store")
			}
			if unresolved.Database != tc.database || unresolved.Error() == "" {
				t.Fatalf("error = %+v, want it to name database %q", unresolved, tc.database)
			}
		})
	}
}

func TestNewDatabaseDAOWritesToTheNamedDatabase(t *testing.T) {
	ctx := context.Background()
	orders, billing, fallback := newMemoryKVStore(), newMemoryKVStore(), newMemoryKVStore()
	f := databaseFactory(map[string]kvstore.KVStore{"orders": orders, "billing": billing}, fallback)

	d, err := NewDatabaseDAO(f, "orders", "customers", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("NewDatabaseDAO: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for name, store := range map[string]kvstore.KVStore{"billing": billing, "default": fallback} {
		other, err := New(store, "customers", func() *TestEntity { return &TestEntity{} })
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if _, err := other.Get(ctx, 1); !IsNotFound(err) {
			t.Fatalf("the %s store holds the orders entity (err %v)", name, err)
		}
	}
	got, err := d.Get(ctx, 1)
	if err != nil || got.Name != "alice" {
		t.Fatalf("Get = %+v, %v; want alice from the orders store", got, err)
	}
}

func TestNewDatabaseDAORefusesAnUnresolvedDatabase(t *testing.T) {
	f := databaseFactory(map[string]kvstore.KVStore{}, newMemoryKVStore())
	d, err := NewDatabaseDAO(f, "orders", "customers", func() *TestEntity { return &TestEntity{} })
	var unresolved DatabaseStoreUnresolvedError
	if !errors.As(err, &unresolved) || d != nil {
		t.Fatalf("NewDatabaseDAO = %v, %v; want DatabaseStoreUnresolvedError and no DAO", d, err)
	}
}

func TestNewDatabaseDAORefusesANilFactory(t *testing.T) {
	_, err := NewDatabaseDAO(nil, "orders", "customers", func() *TestEntity { return &TestEntity{} })
	var invalid InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Field != "factory" {
		t.Fatalf("NewDatabaseDAO(nil) error = %v, want InvalidConfigError on factory", err)
	}
}

func TestNewDatabaseTransactionalCommitsToTheNamedDatabase(t *testing.T) {
	ctx := context.Background()
	orders := newMemoryKVStore()
	f := databaseFactory(map[string]kvstore.KVStore{"orders": orders}, newMemoryKVStore())

	d, err := NewDatabaseTransactional(f, "orders", "customers", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("NewDatabaseTransactional: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{ID: 2, Name: "bob", Email: "bob@example.com"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := New(orders, "customers", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, err := plain.Get(ctx, 2); err != nil || got.Name != "bob" {
		t.Fatalf("orders store Get = %+v, %v; want bob", got, err)
	}
}

func TestNewDatabaseTransactionalRefusesAnUnresolvedDatabase(t *testing.T) {
	_, err := NewDatabaseTransactional(NewFactory(nil), "orders", "customers", func() *TestEntity { return &TestEntity{} })
	var unresolved DatabaseStoreUnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("NewDatabaseTransactional error = %v, want DatabaseStoreUnresolvedError", err)
	}
	if _, err := NewDatabaseTransactional[*TestEntity](nil, "orders", "customers", nil); !errors.As(err, new(InvalidConfigError)) {
		t.Fatalf("NewDatabaseTransactional(nil) error = %v, want InvalidConfigError", err)
	}
}
