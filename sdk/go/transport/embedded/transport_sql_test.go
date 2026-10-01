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

//go:build qrdb_sql

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// sqlScopeRegistry is a scopeRegistry that also answers the lookups the SQL
// provider makes beside KV: this node serves no relational tables, no
// collections and no privilege catalog, each reported as the node's registry
// reports a subsystem it does not run.
type sqlScopeRegistry struct {
	*scopeRegistry
}

var _ service.Registry = sqlScopeRegistry{}

func (sqlScopeRegistry) TableService(string, string) (service.TableServicer, error) {
	return nil, &service.SubsystemNotConfiguredError{Subsystem: "table"}
}

func (sqlScopeRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return nil, nil
}

func (sqlScopeRegistry) PrivilegeService(string, string) (service.PrivilegeServicer, error) {
	return nil, &service.SubsystemNotConfiguredError{Subsystem: "privilege"}
}

func newSQLScopeTransport(t *testing.T, defaultKV *mockKVService, reg sqlScopeRegistry) *Transport {
	t.Helper()
	tr, err := New(defaultKV, &mockAdminService{},
		WithServiceRegistry(reg),
		WithDatabaseService(&mockDatabaseService{getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name, Engine: database.EngineKV, Status: database.StatusOpen}, nil
		}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// With a service registry, an embedded SQL query reads the named database
// through it -- the database's own store, unprefixed -- on the plain
// transport and on a database-scoped one alike. The query service used to be
// built over the node's default store with the "<tenant>/<db>/" prefix, so
// the plain transport read the shared store beside the database and a scoped
// transport prefixed its already-scoped store a second time.
func TestEmbeddedSQL_ResolvesThroughRegistry(t *testing.T) {
	ledger := tenant.SystemTenantID + "/ledger"
	reg := sqlScopeRegistry{newScopeRegistry(ledger)}
	if err := reg.stores[ledger].Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tr := newSQLScopeTransport(t, newMockKVService(), reg)
	scoped, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}

	for name, q := range map[string]*Transport{"plain": tr, "scoped": scoped.(*Transport)} {
		res, err := q.Query(context.Background(), "ledger", "SELECT `key` FROM kv")
		if err != nil {
			t.Errorf("%s Query: %v", name, err)
			continue
		}
		if res.RowCount != 1 || len(res.Rows) != 1 || res.Rows[0]["key"] != "k" {
			t.Errorf("%s Query rows = %v, want the one key in ledger's store", name, res.Rows)
		}
	}
}

// A sealed database is refused over embedded SQL: the query resolves the
// registry, which refuses it, instead of reading the node's default store
// under the database's prefix.
func TestEmbeddedSQL_SealedDatabaseRefused(t *testing.T) {
	ledger := tenant.SystemTenantID + "/ledger"
	reg := sqlScopeRegistry{newScopeRegistry(ledger)}
	reg.sealed[ledger] = true
	defaultKV := newMockKVService()
	defaultKV.store[tenant.ComposeNamespace(tenant.SystemTenantID, "ledger")+"raw"] = []byte("plaintext")
	tr := newSQLScopeTransport(t, defaultKV, reg)

	res, err := tr.Query(context.Background(), "ledger", "SELECT `key` FROM kv")
	var sealed *service.BarrierSealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("Query on a sealed database = %v, %v; want *service.BarrierSealedError", res, err)
	}
}
