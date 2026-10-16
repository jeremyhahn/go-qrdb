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

package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ledgerEntry is an index-free entity, so a DAO over it needs nothing from a
// store but reads and writes.
type ledgerEntry struct {
	ID   uint64 `json:"id"`
	Memo string `json:"memo"`
}

func (e *ledgerEntry) EntityID() uint64      { return e.ID }
func (e *ledgerEntry) SetEntityID(id uint64) { e.ID = id }

func newLedgerEntry() *ledgerEntry { return &ledgerEntry{} }

// databaseStores is a DAOFactory resolving exactly the given database names,
// with a separate default store a test can check stayed untouched.
func databaseStores(dbs map[string]sdk.KVStore, fallback sdk.KVStore) *sdk.DAOFactory {
	return sdk.NewDAOFactoryWithConfig(&sdk.DAOFactoryConfig{
		KVStoreProvider:     func(uint64) sdk.KVStore { return fallback },
		PartitionedProvider: func(name string, _ uint64) sdk.KVStore { return dbs[name] },
	})
}

func TestSDK_NewDatabaseDAOWritesToTheNamedDatabase(t *testing.T) {
	ctx := context.Background()
	orders, fallback := sdk.NewMemoryKVStore(), sdk.NewMemoryKVStore()
	f := databaseStores(map[string]sdk.KVStore{"orders": orders}, fallback)

	d, err := sdk.NewDatabaseDAO(f, "orders", "ledger", newLedgerEntry)
	if err != nil {
		t.Fatalf("NewDatabaseDAO: %v", err)
	}
	if err := d.Save(ctx, &ledgerEntry{ID: 1, Memo: "opening"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	inDefault, err := sdk.NewDAO(fallback, "ledger", newLedgerEntry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inDefault.Get(ctx, 1); !sdk.IsDAONotFound(err) {
		t.Fatalf("the default store holds the orders entry (err %v)", err)
	}
}

func TestSDK_NewDatabaseDAORefusesAnUnresolvedDatabase(t *testing.T) {
	f := databaseStores(map[string]sdk.KVStore{}, sdk.NewMemoryKVStore())
	for _, build := range []func() error{
		func() error { _, err := sdk.NewDatabaseDAO(f, "orders", "ledger", newLedgerEntry); return err },
		func() error {
			_, err := sdk.NewDatabaseDAOTransactional(f, "orders", "ledger", newLedgerEntry)
			return err
		},
	} {
		var unresolved sdk.DAODatabaseStoreUnresolvedError
		if err := build(); !errors.As(err, &unresolved) || unresolved.Database != "orders" {
			t.Fatalf("error = %v, want DAODatabaseStoreUnresolvedError naming orders", err)
		}
	}
}

func TestSDK_NewDAOTransactionalBuildsOverAStore(t *testing.T) {
	ctx := context.Background()
	store := sdk.NewMemoryKVStore()
	var d sdk.DAOTransactional[*ledgerEntry]
	d, err := sdk.NewDAOTransactional(store, "ledger", newLedgerEntry)
	if err != nil {
		t.Fatalf("NewDAOTransactional: %v", err)
	}
	if err := d.Save(ctx, &ledgerEntry{ID: 7, Memo: "carried"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := d.Get(ctx, 7); err != nil || got.Memo != "carried" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := sdk.NewDAOTransactional[*ledgerEntry](nil, "ledger", newLedgerEntry); !errors.As(err, new(sdk.DAOInvalidConfigError)) {
		t.Fatalf("NewDAOTransactional(nil store) error = %v, want DAOInvalidConfigError", err)
	}
}

func TestSDK_NewDatabaseDAOTransactionalWritesToTheNamedDatabase(t *testing.T) {
	ctx := context.Background()
	orders := sdk.NewMemoryKVStore()
	f := databaseStores(map[string]sdk.KVStore{"orders": orders}, sdk.NewMemoryKVStore())
	d, err := sdk.NewDatabaseDAOTransactional(f, "orders", "ledger", newLedgerEntry)
	if err != nil {
		t.Fatalf("NewDatabaseDAOTransactional: %v", err)
	}
	if err := d.Save(ctx, &ledgerEntry{ID: 3, Memo: "posted"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plain, err := sdk.NewDAO(orders, "ledger", newLedgerEntry)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := plain.Get(ctx, 3); err != nil || got.Memo != "posted" {
		t.Fatalf("orders Get = %+v, %v", got, err)
	}
}

// TestSDK_NewClientDAOFactoryPlacesTheDAOInTheScopedDatabase drives the
// client-backed factory over the embedded transport: the DAO's write must land
// in the named database's service, never the default one, and a client that
// cannot scope refuses the database outright.
func TestSDK_NewClientDAOFactoryPlacesTheDAOInTheScopedDatabase(t *testing.T) {
	ctx := context.Background()

	t.Run("scoped", func(t *testing.T) {
		defaultKV, db2 := newMockKVService(), newMockKVService()
		tr, err := embedded.New(defaultKV, &mockAdminService{},
			embedded.WithServiceRegistry(&scopedKVRegistry{db: "db2", store: db2}))
		if err != nil {
			t.Fatalf("embedded.New: %v", err)
		}
		c, _ := sdk.NewClient(tr)
		d, err := sdk.NewDatabaseDAO(sdk.NewClientDAOFactory(c), "db2", "ledger", newLedgerEntry)
		if err != nil {
			t.Fatalf("NewDatabaseDAO: %v", err)
		}
		if err := d.Save(ctx, &ledgerEntry{ID: 1, Memo: "scoped"}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if len(defaultKV.store) != 0 {
			t.Fatalf("the default database was written: %v", defaultKV.store)
		}
		if len(db2.store) == 0 {
			t.Fatal("db2 holds nothing after the save")
		}
	})

	t.Run("unscopable", func(t *testing.T) {
		tr, err := embedded.New(newMockKVService(), &mockAdminService{})
		if err != nil {
			t.Fatalf("embedded.New: %v", err)
		}
		c, _ := sdk.NewClient(tr)
		_, err = sdk.NewDatabaseDAO(sdk.NewClientDAOFactory(c), "db2", "ledger", newLedgerEntry)
		var unresolved sdk.DAODatabaseStoreUnresolvedError
		if !errors.As(err, &unresolved) {
			t.Fatalf("error = %v, want DAODatabaseStoreUnresolvedError", err)
		}
	})
}

// ExampleNewDatabaseDAO builds a DAO for one named database from a factory
// that resolves database names to stores. Against a node, the factory is
// sdk.NewClientDAOFactory(node.Client()).
func ExampleNewDatabaseDAO() {
	ctx := context.Background()
	orders := sdk.NewMemoryKVStore()
	factory := sdk.NewDAOFactoryWithConfig(&sdk.DAOFactoryConfig{
		PartitionedProvider: func(database string, _ uint64) sdk.KVStore {
			if database == "orders" {
				return orders
			}
			return nil
		},
	})

	ledger, err := sdk.NewDatabaseDAO(factory, "orders", "ledger", newLedgerEntry)
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = ledger.Save(ctx, &ledgerEntry{ID: 1, Memo: "opening"})
	got, _ := ledger.Get(ctx, 1)
	fmt.Println(got.Memo)

	_, err = sdk.NewDatabaseDAO(factory, "billing", "ledger", newLedgerEntry)
	fmt.Println(err)
	// Output:
	// opening
	// dao: cannot resolve the store of database "billing": the partitioned provider has no store for it
}

// ExampleNewDatabaseDAOTransactional builds a transactional DAO the same way.
func ExampleNewDatabaseDAOTransactional() {
	orders := sdk.NewMemoryKVStore()
	factory := sdk.NewDAOFactoryWithConfig(&sdk.DAOFactoryConfig{
		PartitionedProvider: func(string, uint64) sdk.KVStore { return orders },
	})
	ledger, err := sdk.NewDatabaseDAOTransactional(factory, "orders", "ledger", newLedgerEntry)
	fmt.Println(ledger != nil, err)
	// Output: true <nil>
}
